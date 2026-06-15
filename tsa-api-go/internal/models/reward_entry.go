package models

import (
	"time"

	"github.com/google/uuid"
)

// Payout-computation models (fee/TP overhaul SP5).
//
// SP3/SP4 accrue TP (tp_balance) and cashback (cashback_earnings, in USD). SP5
// turns those into a per-user CLAIMABLE MCGP balance that SP6's merkle
// distributor will publish on-chain:
//
//   - RewardEntry      — append-only ledger; one row per (user, level) level
//                        reward and one per converted cashback row. The MCGP
//                        amount + the OTC rate it was LOCKED at are stored, so
//                        once an entry is written, later MCGP volatility can
//                        never change what that entry credited.
//
// ⚠️ RATE-LOCK TIMING (diverges from design §5/§9 — pending PM sign-off): the
// OTC rate is locked at the SP5 reward-PROCESSING time (when the periodic job
// first converts the accrued TP/cashback), NOT at SP3/SP4 accrual time. The
// accrual rows (TPEarning, CashbackEarning) carry NO MCGP rate, and a level
// crossing has no accrual row at all, so accrual-time locking is not structurally
// possible as built. Consequence: MCGP volatility BETWEEN accrual and first
// successful processing DOES move the owed MCGP — so the design §9 invariant
// "USD→MCGP rate locked at accrual (payout-time volatility can't change owed
// amounts)" is only PARTIALLY satisfied (it holds from first-processing onward,
// not from accrual). The lock is still deterministic per entry, idempotent
// (each level/cashback converts exactly once), and OTC-outage-safe (a bad/zero
// rate DEFERS rather than locking). Aligning to true accrual-time locking
// requires snapshotting a rate on each accrual event (a structural change to
// SP3/SP4) and is tracked separately; do not claim accrual-time semantics here.
//   - RewardClaimable  — per-user CUMULATIVE claimable MCGP = Σ RewardEntry.
//                        MCGP. This is the number SP6 publishes; SP6 tracks
//                        claimed-so-far separately (cumulative-minus-claimed).
//   - UserRewardState  — per-user processing checkpoint: highest level already
//                        awarded (so re-crossing awards nothing), a dirty flag /
//                        last-activity marker the periodic job uses to avoid
//                        scanning all users, and the last-processed timestamp.
//   - PrizeDue         — one row per crossed L6+ level; manual/operational
//                        fulfilment (never auto-paid).
//
// All SP5 tables back the fee/TP reward-payout pipeline.

// Reward-entry kinds recorded on RewardEntry.Kind.
const (
	// RewardKindLevel is a cumulative level reward (one per crossed level).
	RewardKindLevel = "level_reward"
	// RewardKindCashback is a converted CashbackEarning (one per cashback row).
	RewardKindCashback = "cashback"
)

// RewardEntry is one append-only credit into a user's claimable-MCGP ledger.
//
// Idempotency is enforced by a partial-unique key per kind:
//   - level rewards   : unique (user_id, kind, source_level)
//   - cashback        : unique (user_id, kind, source_id)
//
// Both are expressed via a single composite unique index over
// (user_id, kind, source_level, source_id); for a level reward SourceID is
// uuid.Nil and SourceLevel is the level, for a cashback SourceLevel is 0 and
// SourceID is the cashback_earnings row id. The two never collide.
type RewardEntry struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index:idx_reward_entry_user_created,priority:1;uniqueIndex:idx_reward_entry_idem,priority:1" json:"userId"`

	// Kind is RewardKindLevel or RewardKindCashback.
	Kind string `gorm:"size:24;not null;uniqueIndex:idx_reward_entry_idem,priority:2" json:"kind"`

	// SourceLevel is the crossed level for a level reward (1..15); 0 for cashback.
	SourceLevel int `gorm:"not null;default:0;uniqueIndex:idx_reward_entry_idem,priority:3" json:"sourceLevel"`

	// SourceID is the cashback_earnings row id for a cashback entry; uuid.Nil
	// for a level reward.
	SourceID uuid.UUID `gorm:"type:uuid;not null;default:'00000000-0000-0000-0000-000000000000';uniqueIndex:idx_reward_entry_idem,priority:4" json:"sourceId"`

	// RewardUSD is the USD value that was converted (level reward USD, or the
	// cashback's AmountUSD). Recorded for audit alongside the locked MCGP.
	RewardUSD float64 `gorm:"not null" json:"rewardUsd"`

	// MCGPAmount is the MCGP credited = RewardUSD / RateUSDPerMCGP, locked at SP5
	// PROCESSING time (when this entry is first written — see the type doc's
	// rate-lock note; this is NOT accrual time). Flows into the cumulative
	// claimable balance and is immutable thereafter.
	MCGPAmount float64 `gorm:"not null" json:"mcgpAmount"`

	// RateUSDPerMCGP is the OTC buy price (USD per 1 MCGP) locked at SP5 PROCESSING
	// time (see the type doc's rate-lock note — NOT accrual time). Stored so the
	// conversion is auditable and immutable. Always > 0 (a zero / unavailable rate
	// DEFERS the conversion — no row is written).
	RateUSDPerMCGP float64 `gorm:"not null" json:"rateUsdPerMcgp"`

	// PayoutToken / PayoutChain are the admin-resolved payout target for this
	// reward (RewardPayoutConfigService). PayoutAmount is the amount in that token
	// — RewardUSD for a 1:1 stablecoin (USDC/USDT), or RewardUSD/RateUSDPerMCGP for
	// MCGP. Defaults preserve pre-multi-token behavior (MCGP/sonic) for legacy
	// rows. MCGPAmount stays populated only for MCGP payouts (0 otherwise) so the
	// MCGP claim path / ClaimableMCGP is unaffected by stablecoin payouts.
	PayoutToken  string  `gorm:"size:16;not null;default:'MCGP'" json:"payoutToken"`
	PayoutChain  string  `gorm:"size:32;not null;default:'sonic'" json:"payoutChain"`
	PayoutAmount float64 `gorm:"not null;default:0" json:"payoutAmount"`

	CreatedAt time.Time `gorm:"not null;index:idx_reward_entry_user_created,priority:2" json:"createdAt"`
}

// TableName overrides the default table name.
func (RewardEntry) TableName() string {
	return "reward_entries"
}

// RewardClaimable is the per-user CUMULATIVE claimable-MCGP balance: the running
// sum of every RewardEntry.MCGPAmount for the user. One row per user. This is
// the cumulative figure SP6's distributor publishes in its merkle root; SP6
// tracks how much has actually been claimed on-chain separately, and a user can
// claim (ClaimableMCGP − alreadyClaimed).
type RewardClaimable struct {
	UserID uuid.UUID `gorm:"type:uuid;primaryKey" json:"userId"`

	// ClaimableMCGP is the cumulative MCGP owed = Σ RewardEntry.MCGPAmount.
	// Monotonic non-decreasing (entries are append-only); SP6 never decrements
	// it (claims are tracked claimed-so-far, not by reducing this).
	ClaimableMCGP float64 `gorm:"not null;default:0" json:"claimableMcgp"`

	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

// TableName overrides the default table name.
func (RewardClaimable) TableName() string {
	return "reward_claimables"
}

// UserRewardState is the per-user SP5 processing checkpoint. One row per user
// that has ever been processed (created lazily on first ProcessUserRewards).
type UserRewardState struct {
	UserID uuid.UUID `gorm:"type:uuid;primaryKey" json:"userId"`

	// HighestAwardedLevel is the highest level whose reward has been recorded for
	// this user. ProcessUserRewards awards every level in
	// (HighestAwardedLevel, currentLevel] and advances this. Re-crossing an
	// already-awarded level awards nothing (idempotent).
	HighestAwardedLevel int `gorm:"not null;default:0" json:"highestAwardedLevel"`

	// Dirty marks the user as having recent TP/cashback activity that may have
	// crossed a new level or accrued new cashback — the periodic job processes
	// only dirty users (not a blind full-table scan). Set by the accrual path,
	// cleared after a successful ProcessUserRewards run.
	Dirty bool `gorm:"not null;default:false;index:idx_user_reward_dirty" json:"dirty"`

	// LastProcessedAt is when ProcessUserRewards last completed for this user.
	LastProcessedAt *time.Time `json:"lastProcessedAt,omitempty"`

	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

// TableName overrides the default table name.
func (UserRewardState) TableName() string {
	return "user_reward_states"
}

// PrizeDue is one physical-prize obligation, recorded when a user crosses an
// L6+ level. Manual / operational fulfilment — the engine emits an admin-notify
// event and never auto-pays a prize. One row per (user, level), idempotent.
type PrizeDue struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_prize_due_user_level,priority:1" json:"userId"`

	// Level is the crossed L6+ level (>= TPLevelPhysicalPrizeFloor).
	Level int `gorm:"not null;uniqueIndex:idx_prize_due_user_level,priority:2" json:"level"`

	// Prize is the physical-prize label (PrizeForLevel(Level)).
	Prize string `gorm:"size:64;not null" json:"prize"`

	// Status tracks manual fulfilment. Starts "pending"; admin tooling moves it.
	Status string `gorm:"size:24;not null;default:'pending'" json:"status"`

	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
}

// TableName overrides the default table name.
func (PrizeDue) TableName() string {
	return "prize_dues"
}

// PrizeDueStatusPending is the initial (un-fulfilled) prize state.
const PrizeDueStatusPending = "pending"
