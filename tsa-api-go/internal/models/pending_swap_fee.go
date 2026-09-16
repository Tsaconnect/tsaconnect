package models

import (
	"time"

	"github.com/google/uuid"
)

// Pending swap-fee statuses.
const (
	PendingSwapFeePending   = "pending"
	PendingSwapFeeSettled   = "settled"
	PendingSwapFeeAbandoned = "abandoned"
)

// PendingSwapFee records that a broadcast swap owes a platform fee that this
// request could not compute.
//
// An aggregator swap's `amount` is a quantity of the from-token, which the
// submit path has no way to value, so the fee is settled afterwards against a
// re-queried USD figure (POST /swap/lifi/settle). That leaves a window: the
// swap is irreversibly on-chain, and the client that owes the follow-up call
// may be killed, backgrounded, or offline before it makes it. Without a row
// here the debt would simply vanish, with nothing to reconcile from.
//
// The sweeper (internal/jobs/swap_fee_sweeper.go) settles anything still
// pending after a grace period. Both paths accrue under the same idempotency
// key — deterministicUUID("allswap:"+TxHash) — so a client settle and a sweep
// of the same swap collapse into one fee event rather than double-charging.
type PendingSwapFee struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	// TxHash is the source-chain transaction, and the settle idempotency key.
	// Unique: one fee per executed swap, however many times submit is retried.
	TxHash string `gorm:"size:128;not null;uniqueIndex" json:"txHash"`
	// Feature is the fee bucket — allswap (bridge) or spot (same-chain desk).
	Feature string `gorm:"size:48;not null" json:"feature"`
	// TokenSymbol + AmountHuman are what the sweeper values the swap from. The
	// amount is in WHOLE TOKENS here, not base units, because that is what the
	// submit request carries and mixing the two is how a 1,000 USDC swap once
	// turned into a $35,000,000 fee.
	TokenSymbol string  `gorm:"size:16;not null" json:"tokenSymbol"`
	AmountHuman float64 `gorm:"not null" json:"amountHuman"`
	ChainID     int64   `gorm:"not null" json:"chainId"`

	Status   string `gorm:"size:16;not null;default:'pending';index" json:"status"`
	Attempts int    `gorm:"not null;default:0" json:"attempts"`
	LastErr  string `gorm:"size:512" json:"lastErr,omitempty"`

	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	SettledAt *time.Time `json:"settledAt,omitempty"`
}

func (PendingSwapFee) TableName() string { return "pending_swap_fees" }
