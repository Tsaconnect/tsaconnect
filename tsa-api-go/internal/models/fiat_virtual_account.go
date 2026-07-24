package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FiatVirtualAccount is a permanent per-user static virtual account for IT
// bank-transfer buys (Spec 1). One row per (user, currency). Attribution keys
// on FwCustomerID (primary) and Reference (FW create RESPONSE reference).
const (
	FiatVAStatusActive   = "active"
	FiatVAStatusInactive = "inactive"

	FiatVAProviderFlutterwave = "flutterwave"
)

// FiatVirtualAccount stores the standing pay-into account for a user. BVN/NIN
// are never stored — they are forwarded to Flutterwave only at activate time.
type FiatVirtualAccount struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_fiat_va_user_currency" json:"userId"`

	Currency string `gorm:"size:8;not null;uniqueIndex:uq_fiat_va_user_currency" json:"currency"` // "NGN"
	Provider string `gorm:"size:16;not null" json:"provider"`                                     // "flutterwave"

	FwCustomerID string `gorm:"size:64;not null;index" json:"fwCustomerId"`
	FwVAID       string `gorm:"size:64" json:"fwVaId,omitempty"`
	// Reference is the create-RESPONSE reference (what charge.completed echoes),
	// not the request reference we sent.
	Reference string `gorm:"size:128;not null;index" json:"reference"`

	AccountNumber string `gorm:"size:20;not null" json:"accountNumber"`
	BankName      string `gorm:"size:64" json:"bankName"`
	AccountName   string `gorm:"size:128" json:"accountName"`

	Status string `gorm:"size:16;not null;default:'active';index" json:"status"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (v *FiatVirtualAccount) BeforeCreate(tx *gorm.DB) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	return nil
}
