package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Payment-merchant status values.
const (
	PaymentMerchantStatusPending = "pending" // approved but not yet configured/activated
	PaymentMerchantStatusActive  = "active"  // live: automation runs
	PaymentMerchantStatusPaused  = "paused"  // kill switch: no new trades, in-flight drain
)

// PaymentMerchant is a corporate body that provides AUTOMATED P2P liquidity,
// settled via its own Nomba account (design §4). Phase 1 holds exactly one row
// (TSA), seeded manually. Secrets (Nomba client id/secret, the operator private
// key) live in a secret manager — only references/addresses are stored here.
type PaymentMerchant struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	Name   string    `gorm:"size:128;not null" json:"name"`

	Status string `gorm:"size:16;not null;default:'pending'" json:"status"`

	// Nomba (per-PM). NombaCredsRef points at the secret-store entry holding the
	// client id/secret; the raw creds are NEVER stored in this table.
	NombaAccountID       string `gorm:"size:64" json:"nombaAccountId"`
	NombaCredsRef        string `gorm:"size:128" json:"-"`
	InboundAccountNumber string `gorm:"size:32" json:"inboundAccountNumber"`
	InboundBank          string `gorm:"size:64" json:"inboundBank"`

	// OperatorWalletAddress is the EVM address whose key (held in the secret
	// manager) the backend uses to sign release/deposit. The key is NEVER stored.
	OperatorWalletAddress string `gorm:"size:42" json:"operatorWalletAddress"`

	// Caps (USD). 0 = no cap.
	PerTradeCapUSD float64 `gorm:"not null;default:0" json:"perTradeCapUsd"`
	DailyCapUSD    float64 `gorm:"not null;default:0" json:"dailyCapUsd"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BeforeCreate sets the UUID if not already assigned. This ensures uniqueness
// whether the row is inserted via the Go service (UUID from app layer) or by
// a direct DB seed (falls back to a new random UUID).
func (m *PaymentMerchant) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

// IsActive reports whether the merchant's automation should run.
func (m *PaymentMerchant) IsActive() bool {
	return m != nil && m.Status == PaymentMerchantStatusActive
}
