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
// out to users (not done here).
type GlobalTPPool struct {
	// Singleton is the primary key, always true — enforces a single row.
	Singleton bool `gorm:"primaryKey" json:"-"`

	// AccruedUSD / AccruedTP are the cumulative amounts ever routed into the
	// pool. AccruedTP = AccruedUSD · TPPerUSD at accrual time.
	AccruedUSD float64 `gorm:"not null;default:0" json:"accruedUsd"`
	AccruedTP  float64 `gorm:"not null;default:0" json:"accruedTp"`

	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

// TableName overrides the default table name.
func (GlobalTPPool) TableName() string {
	return "global_tp_pool"
}

// GlobalTPPoolSingletonID is the fixed primary-key value of the single
// GlobalTPPool row.
const GlobalTPPoolSingletonID = true

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
