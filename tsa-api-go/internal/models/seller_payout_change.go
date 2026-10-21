package models

import (
	"time"

	"github.com/google/uuid"
)

// SellerPayoutChange is a seller's new payout destination waiting on the code
// emailed to them. A password alone could be a stolen login redirecting the
// seller's money, so nothing is applied until the owner reads their email. One
// per seller (a new request replaces it); deleted once used, exhausted or
// expired. Payload is the validated SellerPayoutMethod as JSON.
type SellerPayoutChange struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"-"`
	Payload   string    `gorm:"type:text;not null" json:"-"`
	CodeHash  string    `gorm:"size:64;not null" json:"-"`
	Attempts  int       `gorm:"not null;default:0" json:"-"`
	ExpiresAt time.Time `gorm:"not null" json:"-"`
	CreatedAt time.Time `json:"-"`
}

func (SellerPayoutChange) TableName() string { return "seller_payout_changes" }
