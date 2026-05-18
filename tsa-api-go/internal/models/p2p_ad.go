package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// P2PAd represents a seller's offer to sell `Amount` of `Token` (Phase 1: sell
// ads only). The lifecycle mirrors the on-chain Ad in P2PEscrow.sol:
//
//	pending  → active (escrow tx confirmed)
//	active   → paused | exhausted (remaining == 0) | cancelled (closeAd)
//	paused   ↔ active
//	cancelled is terminal
//
// On-chain identity: OnChainAdID is the bytes32 used in the contract
// (keccak256 of ID by convention; stored hex-encoded with 0x prefix).
// EscrowTxHash records the createAd transaction that flipped the ad from
// pending → active.
//
// Rate is the fiat-per-token unit (e.g. NGN per USDT). Rate is locked at ad
// creation and inherited by every trade opened against this ad; this avoids
// per-trade rate disputes.
//
// Token amounts are stored as strings (numeric in DB) so we never lose
// precision on USDT (6 dec), USDC (6 dec), or MCGP (18 dec).
type P2PAd struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	SellerID         uuid.UUID      `gorm:"type:uuid;not null;index" json:"sellerId"`
	Side             string         `gorm:"size:8;not null;index;default:'sell'" json:"side"`
	Token            string         `gorm:"size:16;not null;index" json:"token"`
	ChainID          int64          `gorm:"not null;default:146" json:"chainId"`
	Amount           string         `gorm:"type:numeric(78,0);not null" json:"amount"`
	Remaining        string         `gorm:"type:numeric(78,0);not null" json:"remaining"`
	Reserved         string         `gorm:"type:numeric(78,0);not null;default:'0'" json:"reserved"`
	FiatCurrency     string         `gorm:"size:3;not null;index" json:"fiatCurrency"`
	Rate             string         `gorm:"type:numeric(38,10);not null" json:"rate"`
	MinLimit         string         `gorm:"type:numeric(78,0)" json:"minLimit,omitempty"`
	MaxLimit         string         `gorm:"type:numeric(78,0)" json:"maxLimit,omitempty"`
	PaymentMethodIDs datatypes.JSONSlice[string] `gorm:"type:jsonb;not null" json:"paymentMethodIds"`
	Terms            string         `gorm:"type:text" json:"terms,omitempty"`
	OnChainAdID      string         `gorm:"size:66" json:"onChainAdId,omitempty"`
	EscrowTxHash     string         `gorm:"size:66;index" json:"escrowTxHash,omitempty"`
	Status           string         `gorm:"size:16;not null;default:'pending';index" json:"status"`
	ExpiresAt        *time.Time     `gorm:"index" json:"expiresAt,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	DeletedAt        *time.Time     `gorm:"index" json:"deletedAt,omitempty"`
}

func (P2PAd) TableName() string {
	return "p2p_ads"
}

const (
	P2PAdSideSell = "sell"
	P2PAdSideBuy  = "buy"

	P2PAdStatusPending   = "pending"
	P2PAdStatusActive    = "active"
	P2PAdStatusPaused    = "paused"
	P2PAdStatusExhausted = "exhausted"
	P2PAdStatusCancelled = "cancelled"
)
