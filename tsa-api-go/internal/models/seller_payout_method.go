package models

import (
	"time"

	"github.com/google/uuid"
)

// Seller payout kinds. A seller with no row is paid in crypto to their wallet.
const (
	SellerPayoutWallet      = "wallet"
	SellerPayoutBank        = "bank"
	SellerPayoutMobileMoney = "mobile_money"
)

// SellerPayoutMethod is where a marketplace seller is paid fiat once the buyer
// confirms. One row per seller. Institution codes are Paycrest's vocabulary
// (NIBSS codes for NGN), so the destination can be handed to the offramp as
// stored. It is not a P2PPaymentMethod: those are keyed on the P2P provider
// catalog, which cannot express a Paycrest institution.
type SellerPayoutMethod struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID          uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"userId"`
	Kind            string    `gorm:"size:16;not null" json:"kind"`
	Currency        string    `gorm:"size:3;not null" json:"currency"`
	InstitutionCode string    `gorm:"size:64;not null" json:"institutionCode"`
	InstitutionName string    `gorm:"size:128;not null" json:"institutionName"`
	AccountNumber   string    `gorm:"size:32;not null" json:"accountNumber"`
	// AccountName is the bank's name enquiry where one exists (NGN), never the
	// seller's own typing; Verified says which.
	AccountName string    `gorm:"size:128;not null" json:"accountName"`
	Verified    bool      `gorm:"not null;default:false" json:"verified"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (SellerPayoutMethod) TableName() string { return "seller_payout_methods" }
