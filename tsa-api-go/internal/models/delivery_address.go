package models

import (
	"time"

	"github.com/google/uuid"
)

// DeliveryAddress is a buyer's saved "ship to" address, picked at checkout.
// An order snapshots the fields it needs (Order.Shipping*), so editing or
// deleting an address never rewrites where a past order was sent.
type DeliveryAddress struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	// The partial unique index allows at most one default per user, so two
	// concurrent saves cannot both win.
	UserID        uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_addresses_one_default,where:is_default" json:"userId"`
	Label         string    `gorm:"size:40" json:"label,omitempty"`
	RecipientName string    `gorm:"size:120;not null" json:"recipientName"`
	Phone         string    `gorm:"size:32;not null" json:"phone"`
	Street        string    `gorm:"size:255;not null" json:"street"`
	City          string    `gorm:"size:100;not null" json:"city"`
	State         string    `gorm:"size:100;not null" json:"state"`
	Country       string    `gorm:"size:100;not null" json:"country"`
	IsDefault     bool      `gorm:"default:false" json:"isDefault"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (DeliveryAddress) TableName() string {
	return "delivery_addresses"
}
