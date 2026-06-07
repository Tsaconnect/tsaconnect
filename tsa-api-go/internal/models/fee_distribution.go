package models

import (
	"time"

	"github.com/google/uuid"
)

// Fee-distribution v2 (SP3) models.
//
// These back the new fee/TP engine (services.FeeDistributionService). They are
// purely additive — the legacy DistributeTPEarnings path (tp_handler.go) and
// its tp_earnings/cashback_earnings tables are untouched. The v2 engine writes
// into the SAME tp_earnings / cashback_earnings ledgers (reusing TPEarning /
// CashbackEarning) and the SAME tp_balance / cashback_balance columns, but adds:
//
//   - FeeDistribution: a per-(source_type, source_id) idempotency guard row,
//     mirroring order.TPDistributedAt but generalized so EVERY fee-settled path
//     (checkout, service, swap, withdrawal, P2P release) can be guarded the same
//     way. A unique index on (source_type, source_id) makes double-distribution
//     impossible even under concurrent calls.
//   - GlobalTPPool: a single running-balance row that ACCRUES the TP-pool share
//     of generations with no live ancestor (plus the upline cashback when there
//     is no direct upline). SP4 owns settlement of this pool to users; SP3 only
//     accrues.
//   - GlobalTPAccrual: an append-only ledger of every accrual into the global
//     pool, for audit + SP4's cumulative-index settlement.

// FeeDistribution is the idempotency guard + audit summary for one settled fee
// event under the v2 engine. Exactly one row exists per (SourceType, SourceID);
// the unique index guarantees OnFeeSettled distributes a given source once.
type FeeDistribution struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	SourceType string    `gorm:"size:48;not null;uniqueIndex:idx_fee_distribution_source,priority:1" json:"sourceType"`
	SourceID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_fee_distribution_source,priority:2" json:"sourceId"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	Feature    string    `gorm:"size:48;not null" json:"feature"`
	Token      string    `gorm:"size:16;not null" json:"token"`

	// Split components, all in USD. They sum to FeeUSD.
	FeeUSD            float64 `gorm:"not null" json:"feeUsd"`
	SystemUSD         float64 `gorm:"not null" json:"systemUsd"`         // 50% — treasury retains, recorded only
	BuyerCashbackUSD  float64 `gorm:"not null" json:"buyerCashbackUsd"`  // 10% — buyer cashback
	UplineCashbackUSD float64 `gorm:"not null" json:"uplineCashbackUsd"` // 10% — direct-upline cashback (or global pool)
	TPPoolUSD         float64 `gorm:"not null" json:"tpPoolUsd"`         // 30% — distributed gen0..gen10 (+ global pool)

	// GlobalPoolUSD is the portion of this event routed to the global pool
	// (missing-ancestor TP shares + missing-upline cashback). Recorded for audit.
	GlobalPoolUSD float64 `gorm:"not null;default:0" json:"globalPoolUsd"`

	// TPPerUSD is the minting-rate constant in effect when this event settled,
	// captured so historical accruals are auditable even if the constant is
	// retuned later.
	TPPerUSD float64 `gorm:"not null;default:1" json:"tpPerUsd"`

	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
}

// TableName overrides the default table name.
func (FeeDistribution) TableName() string {
	return "fee_distributions"
}

// GlobalTPPool is the single running-balance row for the global TP pool. There
// is exactly one row (Singleton == true). SP3 accrues into it; SP4 settles it
// out to users via the cumulative-index accumulator below.
type GlobalTPPool struct {
	// Singleton is the primary key, always true — enforces a single row.
	Singleton bool `gorm:"primaryKey" json:"-"`

	// AccruedUSD / AccruedTP are the cumulative amounts ever routed into the
	// pool. AccruedTP = AccruedUSD · TPPerUSD at accrual time. Monotonic — SP4
	// settlement NEVER decrements these; it instead tracks how much has been
	// folded into the index via IndexedTP below.
	AccruedUSD float64 `gorm:"not null;default:0" json:"accruedUsd"`
	AccruedTP  float64 `gorm:"not null;default:0" json:"accruedTp"`

	// ── SP4 cumulative-index settlement state ──
	//
	// PerUserIndexScaled is the MasterChef-style accRewardPerShare accumulator:
	// the cumulative TP owed to EVERY user who has been registered for every
	// epoch, expressed as a SCALED INTEGER (units of 1/IndexScale TP — i.e.
	// pico-TP at IndexScale=1e12). A scaled int64, NOT a float: per-user shares
	// can be tiny (a $0.30 pool delta across 1M users is 3e-7 TP each) and
	// float32/float64 accumulation would round them to zero / drift over epochs.
	// Integer division (floor) makes each epoch's fold exact and leaves rounding
	// dust in the pool — it can never mint TP that wasn't accrued.
	//
	// A user's owed global TP = (PerUserIndexScaled − user.last_settled_global_index)
	// / IndexScale, computed lazily on read/claim — zero per-user writes per epoch.
	PerUserIndexScaled int64 `gorm:"not null;default:0" json:"perUserIndexScaled"`

	// IndexedTP is the cumulative pool TP (a float, matching AccruedTP's unit)
	// that has ALREADY been folded into PerUserIndexScaled. The next epoch's
	// fold delta = AccruedTP − IndexedTP. The high-water mark that makes
	// settlement idempotent: re-running an epoch with no new accrual folds a
	// zero delta. IndexedTP ≤ AccruedTP always; the difference (plus per-epoch
	// integer-division dust) is the un-distributed remainder still in the pool.
	IndexedTP float64 `gorm:"not null;default:0" json:"indexedTp"`

	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

// TableName overrides the default table name.
func (GlobalTPPool) TableName() string {
	return "global_tp_pool"
}

// GlobalTPPoolSingletonID is the fixed primary-key value of the single
// GlobalTPPool row.
const GlobalTPPoolSingletonID = true

// GlobalTPIndexScale is the fixed-point scale of GlobalTPPool.PerUserIndexScaled
// and User.LastSettledGlobalIndex: the index is stored in units of 1/1e12 TP
// (pico-TP). 1e12 gives ~12 significant decimal digits of per-user precision,
// far below any realistic per-user share, while keeping the index well within
// int64 range: even at the platform's full theoretical lifetime TP throughput
// the accumulator stays many orders of magnitude under math.MaxInt64.
const GlobalTPIndexScale = 1_000_000_000_000 // 1e12

// GlobalTPEpoch is one append-only settlement-epoch ledger row, recorded each
// time SettleGlobalPoolEpoch folds the pool delta into the cumulative index.
// One row per non-empty epoch; the audit trail for the global-pool index.
type GlobalTPEpoch struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

	// EpochAt is the settlement timestamp (when the fold ran).
	EpochAt time.Time `gorm:"not null;index" json:"epochAt"`

	// DeltaTP is the pool TP folded this epoch = pool AccruedTP − prior IndexedTP.
	DeltaTP float64 `gorm:"not null" json:"deltaTp"`

	// UserCount is the denominator at this epoch: registered users
	// (deleted_at IS NULL). The split is equal across all of them.
	UserCount int64 `gorm:"not null" json:"userCount"`

	// PerUserScaled is the per-user increment applied to the index this epoch =
	// floor(DeltaTP · IndexScale / UserCount), in the same scaled-int units as
	// GlobalTPPool.PerUserIndexScaled.
	PerUserScaled int64 `gorm:"not null" json:"perUserScaled"`

	// IndexAfterScaled is the cumulative index value after this epoch's fold,
	// for audit (== prior index + PerUserScaled).
	IndexAfterScaled int64 `gorm:"not null" json:"indexAfterScaled"`

	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
}

// TableName overrides the default table name.
func (GlobalTPEpoch) TableName() string {
	return "global_tp_epochs"
}

// GlobalTPAccrual is one append-only accrual into the global pool, for audit
// and SP4's settlement. Source identifies the originating fee event and the
// reason (missing TP generation vs missing upline cashback).
type GlobalTPAccrual struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	SourceType string    `gorm:"size:48;not null;index" json:"sourceType"`
	SourceID   uuid.UUID `gorm:"type:uuid;not null;index" json:"sourceId"`

	// Reason is "tp_generation" (a gen i share with no live ancestor) or
	// "upline_cashback" (no direct upline to receive the 10% cashback).
	Reason string `gorm:"size:32;not null" json:"reason"`

	// Generation is the TP generation whose share was routed (0..10); -1 for
	// the upline-cashback reason.
	Generation int `gorm:"not null;default:-1" json:"generation"`

	AmountUSD float64 `gorm:"not null" json:"amountUsd"`
	AmountTP  float64 `gorm:"not null" json:"amountTp"`

	CreatedAt time.Time `gorm:"not null;index" json:"createdAt"`
}

// TableName overrides the default table name.
func (GlobalTPAccrual) TableName() string {
	return "global_tp_accruals"
}

// Reasons recorded on GlobalTPAccrual.Reason.
const (
	GlobalAccrualReasonTPGeneration   = "tp_generation"
	GlobalAccrualReasonUplineCashback = "upline_cashback"
)
