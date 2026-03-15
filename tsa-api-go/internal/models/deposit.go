package models

import (
	"time"

	"github.com/google/uuid"
)

type Deposit struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID     uuid.UUID  `gorm:"type:uuid;index" json:"userId"`
	Amount     float64    `json:"amount"`
	Currency   string     `gorm:"default:'NGN'" json:"currency"`
	Status     string     `gorm:"default:'pending'" json:"status"`
	ProofURL   string     `json:"proofUrl,omitempty"`
	AdminNote  string     `json:"adminNote,omitempty"`
	ReviewedBy *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (Deposit) TableName() string {
	return "deposits"
}

const (
	DepositStatusPending  = "pending"
	DepositStatusApproved = "approved"
	DepositStatusRejected = "rejected"
)
