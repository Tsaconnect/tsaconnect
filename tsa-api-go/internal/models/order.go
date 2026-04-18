package models

import (
	"time"

	"github.com/google/uuid"
)

type Order struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BuyerID          uuid.UUID  `gorm:"type:uuid;not null;index" json:"buyerId"`
	SellerID         uuid.UUID  `gorm:"type:uuid;not null;index" json:"sellerId"`
	ProductID        uuid.UUID  `gorm:"type:uuid;not null" json:"productId"`
	Quantity         int        `gorm:"not null;default:1" json:"quantity"`
	Token            string     `gorm:"not null" json:"token"`
	ProductAmount    string     `gorm:"not null" json:"productAmount"`
	ShippingAmount   string     `gorm:"not null;default:'0'" json:"shippingAmount"`
	PlatformFee      string     `gorm:"not null;default:'0'" json:"platformFee"`
	TotalAmount      string     `gorm:"not null" json:"totalAmount"`
	ShippingZone     string     `json:"shippingZone"`
	ShippingCity     string     `json:"shippingCity,omitempty"`
	ShippingState    string     `json:"shippingState,omitempty"`
	ShippingCountry  string     `json:"shippingCountry,omitempty"`
	ContractOrderID  string     `json:"contractOrderId,omitempty"`
	EscrowTxHash     string     `json:"escrowTxHash,omitempty"`
	ApproveTxHash    string     `json:"approveTxHash,omitempty"`
	ReleaseTxHash    string     `json:"releaseTxHash,omitempty"`
	BuyerUpline      string     `json:"buyerUpline,omitempty"`
	DeliveryProofURL string     `json:"deliveryProofUrl,omitempty"`
	TrackingNumber   string     `json:"trackingNumber,omitempty"`
	MerchantApprovedRefund bool `gorm:"default:false" json:"merchantApprovedRefund"`
	Status           string     `gorm:"not null;default:'pending_payment';index" json:"status"`
	BuyerConfirmedAt *time.Time `json:"buyerConfirmedAt,omitempty"`
	SellerShippedAt  *time.Time `json:"sellerShippedAt,omitempty"`
	SellerDeliveredAt *time.Time `json:"sellerDeliveredAt,omitempty"`
	EscrowExpiresAt  *time.Time `json:"escrowExpiresAt,omitempty"`
	TPDistributedAt  *time.Time `json:"tpDistributedAt,omitempty"`
	Notes            string     `json:"notes,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (Order) TableName() string {
	return "orders"
}

const (
	OrderStatusPendingPayment  = "pending_payment"
	OrderStatusEscrowed        = "escrowed"
	OrderStatusShipped         = "shipped"
	OrderStatusDelivered       = "delivered"
	OrderStatusCompleted       = "completed"
	OrderStatusRefundRequested = "refund_requested"
	OrderStatusRefunded        = "refunded"
	OrderStatusCancelled       = "cancelled"
)

// ValidNextStatuses returns valid transitions from the current status.
func ValidNextStatuses(current string) []string {
	switch current {
	case OrderStatusPendingPayment:
		return []string{OrderStatusEscrowed, OrderStatusCancelled}
	case OrderStatusEscrowed:
		return []string{OrderStatusShipped, OrderStatusDelivered, OrderStatusRefunded, OrderStatusCancelled}
	case OrderStatusShipped:
		return []string{OrderStatusDelivered, OrderStatusRefundRequested, OrderStatusRefunded}
	case OrderStatusDelivered:
		return []string{OrderStatusCompleted, OrderStatusRefundRequested}
	case OrderStatusRefundRequested:
		// Admin can refund; merchant rejection returns to delivered; admin can also mark completed.
		return []string{OrderStatusRefunded, OrderStatusDelivered, OrderStatusCompleted}
	case OrderStatusCompleted:
		return []string{}
	case OrderStatusRefunded:
		return []string{}
	case OrderStatusCancelled:
		return []string{}
	default:
		return []string{}
	}
}
