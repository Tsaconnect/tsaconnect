package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PPaymentMethod is a user-owned bank or mobile-money account they will
// receive/send fiat through during a P2P trade. The seller links one (or more)
// of these to a posted ad; the buyer sees them when opening a trade.
//
// Phase 1: AccountName is captured manually by the user. Phase 2 will hit
// Flutterwave's accountResolve API and populate it server-side after
// validating the user-typed AccountNumber against the bank code.
//
// Verified flags whether we've successfully resolved the account via
// Flutterwave (or any future verifier). Phase 1 defaults to false; future
// admin tooling or Phase-2 onboarding may flip it.
type P2PPaymentMethod struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID        uuid.UUID  `gorm:"type:uuid;not null;index" json:"userId"`
	Type          string     `gorm:"size:16;not null" json:"type"`
	Country       string     `gorm:"size:2;not null;index" json:"country"`
	Currency      string     `gorm:"size:3;not null;index" json:"currency"`
	BankName      string     `gorm:"size:128;not null" json:"bankName"`
	BankCode      string     `gorm:"size:32" json:"bankCode,omitempty"`
	AccountNumber string     `gorm:"size:32;not null" json:"accountNumber"`
	AccountName   string     `gorm:"size:128;not null" json:"accountName"`
	Verified      bool       `gorm:"default:false" json:"verified"`
	IsDefault     bool       `gorm:"default:false" json:"isDefault"`
	DeletedAt     *time.Time `gorm:"index" json:"deletedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (P2PPaymentMethod) TableName() string {
	return "p2p_payment_methods"
}

const (
	P2PPaymentTypeBank        = "bank"
	P2PPaymentTypeMobileMoney = "mobile_money"
)
