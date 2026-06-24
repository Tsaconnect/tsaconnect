package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Payment-merchant status values.
const (
	PaymentMerchantStatusPending  = "pending"  // awaiting admin approval/provisioning
	PaymentMerchantStatusActive   = "active"   // live: automation runs
	PaymentMerchantStatusPaused   = "paused"   // kill switch: no new trades, in-flight drain
	PaymentMerchantStatusRejected = "rejected" // declined at onboarding; never activated
)

// PMProvisioningStatus values track the lifecycle of a PM's Nomba sub-account.
const (
	PMProvisioningPending     = "pending"
	PMProvisioningProvisioned = "provisioned"
	PMProvisioningActive      = "active"
	PMProvisioningFailed      = "failed"
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
	NombaWalletID        string `gorm:"size:64;index" json:"nombaWalletId"`
	NombaCredsRef        string `gorm:"size:128" json:"-"`
	InboundAccountNumber string `gorm:"size:32" json:"inboundAccountNumber"`
	InboundBank          string `gorm:"size:64" json:"inboundBank"`

	// NombaAccountRef is the deterministic virtual-account (VA) reference under
	// TSA's single parent Nomba business. Funds paid into this PM's VA land in the
	// parent balance and are attributed back to the PM by this accountRef — it is
	// the VA-model match key. InboundAccountNumber holds the VA's pay-into number.
	NombaAccountRef string `gorm:"size:64;index" json:"nombaAccountRef"`

	// OperatorWalletAddress is the EVM address whose key (held in the secret
	// manager) the backend uses to sign release/deposit. The key is NEVER stored.
	OperatorWalletAddress string `gorm:"size:42" json:"operatorWalletAddress"`

	// Deprecated: NombaSubAccountID is no longer set under the VA-only corporate
	// model — corporate PMs are virtual accounts under the single parent, not
	// heavyweight sub-accounts. The column is retained (not migrate-dropped) for
	// back-compat with any seeded rows; provisioning never populates it now.
	NombaSubAccountID string `gorm:"size:64;index" json:"nombaSubAccountId"`

	// Settlement bank fields — where the PM's NGN proceeds are swept.
	SettlementBankCode      string `gorm:"size:16" json:"settlementBankCode"`
	SettlementAccountNumber string `gorm:"size:32" json:"settlementAccountNumber"`
	SettlementAccountName   string `gorm:"size:128" json:"settlementAccountName"`

	// NGN ledger — available and reserved naira balances (decimal strings, 2dp).
	// available_ngn is the total deposited/credited; reserved_ngn is the portion
	// ear-marked for in-flight sell trades. The free (spendable) balance is
	// available_ngn - reserved_ngn.
	AvailableNGN string `gorm:"type:numeric(38,2);not null;default:0" json:"availableNgn"`
	ReservedNGN  string `gorm:"type:numeric(38,2);not null;default:0" json:"reservedNgn"`

	// ProvisioningStatus tracks the Nomba sub-account lifecycle.
	ProvisioningStatus string `gorm:"size:16;not null;default:'pending'" json:"provisioningStatus"`

	// Caps (USD). 0 = no cap.
	PerTradeCapUSD float64 `gorm:"not null;default:0" json:"perTradeCapUsd"`
	DailyCapUSD    float64 `gorm:"not null;default:0" json:"dailyCapUsd"`

	// Admin review audit — who last actioned this PM's lifecycle (approve /
	// reject / pause / resume), when, and the note (required on reject).
	ReviewedBy uuid.UUID  `gorm:"type:uuid" json:"reviewedBy"`
	ReviewedAt *time.Time `json:"reviewedAt"`
	ReviewNote string     `gorm:"size:512" json:"reviewNote"`

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
