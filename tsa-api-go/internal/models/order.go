package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type OrderItem struct {
	ProductID uuid.UUID `json:"productId"`
	Name      string    `json:"name"`
	Quantity  int       `json:"quantity"`
	UnitPrice float64   `json:"unitPrice"`
	Total     float64   `json:"total"`
}

type Order struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BuyerID         uuid.UUID      `gorm:"type:uuid;index" json:"buyerId"`
	SellerID        uuid.UUID      `gorm:"type:uuid;index" json:"sellerId"`
	Items           datatypes.JSON `gorm:"type:jsonb" json:"items"`
	Total           float64        `json:"total"`
	Currency        string         `gorm:"default:'NGN'" json:"currency"`
	Status          string         `gorm:"default:'pending'" json:"status"`
	ShippingAddress string         `json:"shippingAddress,omitempty"`
	Notes           string         `json:"notes,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

func (Order) TableName() string {
	return "orders"
}

const (
	OrderStatusPending    = "pending"
	OrderStatusProcessing = "processing"
	OrderStatusShipped    = "shipped"
	OrderStatusCompleted  = "completed"
	OrderStatusCancelled  = "cancelled"
)

// ValidNextStatuses returns valid transitions from the current status.
func ValidNextStatuses(current string) []string {
	switch current {
	case OrderStatusPending:
		return []string{OrderStatusProcessing, OrderStatusCancelled}
	case OrderStatusProcessing:
		return []string{OrderStatusShipped, OrderStatusCancelled}
	case OrderStatusShipped:
		return []string{OrderStatusCompleted, OrderStatusCancelled}
	case OrderStatusCompleted:
		return []string{}
	case OrderStatusCancelled:
		return []string{}
	default:
		return []string{}
	}
}

func (o *Order) GetItems() []OrderItem {
	var items []OrderItem
	if o.Items != nil {
		_ = json.Unmarshal(o.Items, &items)
	}
	return items
}

func (o *Order) SetItems(items []OrderItem) {
	data, _ := json.Marshal(items)
	o.Items = data
}
