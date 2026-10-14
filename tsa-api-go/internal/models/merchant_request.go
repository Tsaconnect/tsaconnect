package models

import (
	"time"

	"github.com/google/uuid"
)

const (
	MerchantRequestStatusPending  = "pending"
	MerchantRequestStatusApproved = "approved"
	MerchantRequestStatusRejected = "rejected"
)

const (
	BusinessTypeGeneralProducts   = "general_products"
	BusinessTypeDigitalProducts   = "digital_products"
	BusinessTypeP2PMerchant       = "p2p_merchant"
	BusinessTypeServiceProvider   = "service_provider"
	BusinessTypePaymentAcceptance = "payment_acceptance"
	// BusinessTypeLiquidityProvider was the slice pool's LP application. The
	// shared pool activates LPs instantly (spec 2026-10-14 §5); old rows keep
	// the value, new requests are refused.
	BusinessTypeLiquidityProvider = "liquidity_provider"
)

var ValidBusinessTypes = []string{
	BusinessTypeGeneralProducts,
	BusinessTypeDigitalProducts,
	BusinessTypeP2PMerchant,
	BusinessTypeServiceProvider,
	BusinessTypePaymentAcceptance,
}

// PaymentMerchantKindFor maps a request type to the payment_merchants kind it
// approves into; ok=false for types that make no PaymentMerchant row.
func PaymentMerchantKindFor(businessType string) (kind string, ok bool) {
	switch businessType {
	case BusinessTypeP2PMerchant:
		return PaymentMerchantKindMerchant, true
	}
	return "", false
}

type MerchantRequest struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID              uuid.UUID  `gorm:"type:uuid;index;not null" json:"userId"`
	BusinessType        string     `gorm:"not null" json:"businessType"`
	BusinessName        string     `gorm:"not null" json:"businessName"`
	BusinessDescription string     `gorm:"type:text" json:"businessDescription"`
	Address             string     `gorm:"not null" json:"address"`
	City                string     `gorm:"not null" json:"city"`
	State               string     `gorm:"not null" json:"state"`
	Country             string     `gorm:"not null" json:"country"`
	Phone               string     `gorm:"not null" json:"phone"`
	RegistrationNumber  string     `json:"registrationNumber,omitempty"`
	Website             string     `gorm:"size:255" json:"website,omitempty"`
	Status              string     `gorm:"not null;default:'pending';index" json:"status"`
	AdminNote           string     `json:"adminNote,omitempty"`
	ReviewedBy          *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewedAt          *time.Time `json:"reviewedAt,omitempty"`
	User                User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Reviewer            *User      `gorm:"foreignKey:ReviewedBy" json:"reviewer,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`

	// Corporate PM (Payment-Merchant) application fields. These are captured at
	// request time and consumed during approval/provisioning (later tasks).
	// Zero/empty means "not provided" — they are nullable in practice.
	SettlementBankCode      string  `gorm:"size:16" json:"settlementBankCode,omitempty"`
	SettlementAccountNumber string  `gorm:"size:32" json:"settlementAccountNumber,omitempty"`
	SettlementAccountName   string  `gorm:"size:128" json:"settlementAccountName,omitempty"`
	MakerWalletAddress      string  `gorm:"size:42" json:"makerWalletAddress,omitempty"`
	RequestedPerTradeCapUSD float64 `gorm:"default:0" json:"requestedPerTradeCapUsd,omitempty"`
	RequestedDailyCapUSD    float64 `gorm:"default:0" json:"requestedDailyCapUsd,omitempty"`
}

func (MerchantRequest) TableName() string {
	return "merchant_requests"
}
