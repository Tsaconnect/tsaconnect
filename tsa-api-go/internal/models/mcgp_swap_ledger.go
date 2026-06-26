package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MCGP swap ledger directions.
const (
	MCGPLedgerBuyPremium  = "buy_premium"  // USDC in  -> +premium balance
	MCGPLedgerSellPremium = "sell_premium" // -premium balance -> USDC out
	MCGPLedgerSpendOut    = "spend_out"    // payer side of a spend
	MCGPLedgerSpendIn     = "spend_in"     // recipient side of a spend
	MCGPLedgerSellLegacy  = "sell_legacy"  // legacy MCGP in -> USDC out (redeem-only)
	MCGPLedgerWithdraw    = "withdraw"     // -premium balance -> wallet MCGP
)

// MCGP swap ledger statuses.
const (
	MCGPLedgerPending   = "pending"   // prepared/broadcast, awaiting on-chain confirm
	MCGPLedgerConfirmed = "confirmed" // event verified on-chain
	MCGPLedgerFailed    = "failed"    // tx reverted / abandoned
)

// MCGPSwapLedgerEntry is an append-only record of one McgpEntitlementSwap
// ("Swap Contract B") operation. On-chain is the source of truth; this table is
// the audit trail + idempotency key (RefHash unique) + the fast-read history.
// Amounts are base-unit decimal strings to avoid float drift (MCGP 18dp,
// USDC 6dp).
type MCGPSwapLedgerEntry struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	// CounterpartyUserID is set for spend entries (the other party in a
	// buyer<->merchant / peer pay move). Null otherwise.
	CounterpartyUserID *uuid.UUID `gorm:"type:uuid;index" json:"counterpartyUserId,omitempty"`

	Direction string `gorm:"size:16;not null;index" json:"direction"`

	// Amounts as base-unit decimal strings. McgpAmount always set; UsdcAmount
	// set for buy/sell/legacy legs (empty for spend/withdraw).
	McgpAmount string `gorm:"type:numeric(78,0);not null" json:"mcgpAmount"`
	UsdcAmount string `gorm:"type:numeric(38,0)" json:"usdcAmount,omitempty"`

	// RefHash is the on-chain bytes32 ref (hex, 0x-prefixed) for spend entries —
	// the idempotency key. Unique (partial) index enforced in AutoMigrate so a
	// re-dispatched confirm can't double-apply.
	RefHash string `gorm:"size:66;index" json:"refHash,omitempty"`

	TxHash string `gorm:"size:66;index" json:"txHash,omitempty"`
	Status string `gorm:"size:16;not null;default:'pending';index" json:"status"`

	// OrderID links a marketplace-spend entry to the order it paid for.
	OrderID *uuid.UUID `gorm:"type:uuid;index" json:"orderId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BeforeCreate assigns a UUID if not set (parity with other models).
func (e *MCGPSwapLedgerEntry) BeforeCreate(tx *gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
}
