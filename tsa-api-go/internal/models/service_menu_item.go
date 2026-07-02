package models

import (
	"time"

	"github.com/google/uuid"
)

// ServiceMenuItem is one selectable item on an Order-type service's menu (e.g. a
// dish for a restaurant, a wash cycle for a laundry). The buyer picks items and
// quantities at checkout; the server recomputes the total from these rows and
// never trusts client-sent prices.
type ServiceMenuItem struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ProductID   uuid.UUID `gorm:"type:uuid;not null;index" json:"productId"`
	Name        string    `gorm:"not null" json:"name"`
	Description string    `json:"description,omitempty"`
	PriceUSD    float64   `gorm:"not null;default:0" json:"priceUsd"`
	ImageURL    string    `json:"imageUrl,omitempty"`
	IsAvailable bool      `gorm:"default:true" json:"isAvailable"`
	SortOrder   int       `gorm:"default:0" json:"sortOrder"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (ServiceMenuItem) TableName() string {
	return "service_menu_items"
}
