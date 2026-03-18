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
	BusinessTypeGeneralProducts = "general_products"
	BusinessTypeDigitalProducts = "digital_products"
	BusinessTypeP2PMerchant     = "p2p_merchant"
	BusinessTypeServiceProvider = "service_provider"
)

var ValidBusinessTypes = []string{
	BusinessTypeGeneralProducts,
	BusinessTypeDigitalProducts,
	BusinessTypeP2PMerchant,
	BusinessTypeServiceProvider,
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
	Status              string     `gorm:"not null;default:'pending';index" json:"status"`
	AdminNote           string     `json:"adminNote,omitempty"`
	ReviewedBy          *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewedAt          *time.Time `json:"reviewedAt,omitempty"`
	User                User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Reviewer            *User      `gorm:"foreignKey:ReviewedBy" json:"reviewer,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

func (MerchantRequest) TableName() string {
	return "merchant_requests"
}
