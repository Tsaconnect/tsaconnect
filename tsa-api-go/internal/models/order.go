package models

import (
	"time"

	"github.com/google/uuid"
)

type Order struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BuyerID   uuid.UUID `gorm:"type:uuid;not null;index" json:"buyerId"`
	SellerID  uuid.UUID `gorm:"type:uuid;not null;index" json:"sellerId"`
	ProductID uuid.UUID `gorm:"type:uuid;not null" json:"productId"`
	// Kind distinguishes a physical-product order ("product", default) from a
	// service order ("service"). A service skips ship/deliver: it goes
	// pending_payment → escrowed → completed (buyer confirms or timeout
	// auto-releases). Escrow/UI/reconciler/watchdog branch on this instead of
	// re-joining the product to read its type.
	Kind                   string     `gorm:"default:'product';index" json:"kind"`
	Quantity               int        `gorm:"not null;default:1" json:"quantity"`
	Token                  string     `gorm:"not null" json:"token"`
	ProductAmount          string     `gorm:"not null" json:"productAmount"`
	ShippingAmount         string     `gorm:"not null;default:'0'" json:"shippingAmount"`
	PlatformFee            string     `gorm:"not null;default:'0'" json:"platformFee"`
	TotalAmount            string     `gorm:"not null" json:"totalAmount"`
	ShippingZone           string     `json:"shippingZone"`
	ShippingCity           string     `json:"shippingCity,omitempty"`
	ShippingState          string     `json:"shippingState,omitempty"`
	ShippingCountry        string     `json:"shippingCountry,omitempty"`
	ContractOrderID        string     `json:"contractOrderId,omitempty"`
	EscrowContractAddress  string     `json:"escrowContractAddress,omitempty"`
	EscrowTxHash           string     `json:"escrowTxHash,omitempty"`
	ApproveTxHash          string     `json:"approveTxHash,omitempty"`
	ReleaseTxHash          string     `json:"releaseTxHash,omitempty"`
	BuyerUpline            string     `json:"buyerUpline,omitempty"`
	DeliveryProofURL       string     `json:"deliveryProofUrl,omitempty"`
	TrackingNumber         string     `json:"trackingNumber,omitempty"`
	MerchantApprovedRefund bool       `gorm:"default:false" json:"merchantApprovedRefund"`
	CancelReason           string     `json:"cancelReason,omitempty"`
	CancelRequestedAt      *time.Time `json:"cancelRequestedAt,omitempty"`
	MerchantApprovedCancel bool       `gorm:"default:false" json:"merchantApprovedCancel"`
	DisputedAt             *time.Time `json:"disputedAt,omitempty"`
	DisputeReason          string     `json:"disputeReason,omitempty"`
	DisputeRaisedBy        string     `json:"disputeRaisedBy,omitempty"`
	Status                 string     `gorm:"not null;default:'pending_payment';index" json:"status"`
	BuyerConfirmedAt       *time.Time `json:"buyerConfirmedAt,omitempty"`
	SellerShippedAt        *time.Time `json:"sellerShippedAt,omitempty"`
	SellerDeliveredAt      *time.Time `json:"sellerDeliveredAt,omitempty"`
	EscrowExpiresAt        *time.Time `json:"escrowExpiresAt,omitempty"`
	TPDistributedAt        *time.Time `json:"tpDistributedAt,omitempty"`
	Notes                  string     `json:"notes,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
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
	OrderStatusCancelRequested = "cancel_requested"
	OrderStatusRefunded        = "refunded"
	OrderStatusCancelled       = "cancelled"
)

// Order kind constants for Order.Kind.
const (
	OrderKindProduct = "product"
	OrderKindService = "service"
)

// ValidNextStatuses returns valid transitions from the current status for a
// physical-product order (the default).
func ValidNextStatuses(current string) []string {
	return ValidNextStatusesForKind(current, OrderKindProduct)
}

// ValidNextStatusesForKind returns valid transitions for an order of the given
// Kind. A service order skips the shipping leg: it may go straight from
// escrowed to completed (the buyer confirms after the service is rendered), and
// it never enters the "shipped" state. The "delivered" state still exists for
// services (the provider marks the service rendered, which flips a refund from
// an instant buyer self-refund to admin adjudication — mirroring the product
// post-delivery rule on-chain).
func ValidNextStatusesForKind(current, kind string) []string {
	if kind == OrderKindService {
		switch current {
		case OrderStatusPendingPayment:
			return []string{OrderStatusEscrowed, OrderStatusCancelled}
		case OrderStatusEscrowed:
			// No "shipped"; confirm may happen directly (escrowed → completed).
			return []string{OrderStatusDelivered, OrderStatusCompleted, OrderStatusRefunded, OrderStatusCancelled, OrderStatusCancelRequested}
		case OrderStatusDelivered:
			return []string{OrderStatusCompleted, OrderStatusRefundRequested}
		case OrderStatusRefundRequested:
			return []string{OrderStatusRefunded, OrderStatusDelivered, OrderStatusCompleted}
		case OrderStatusCancelRequested:
			return []string{OrderStatusCancelled, OrderStatusEscrowed, OrderStatusRefunded}
		default:
			return []string{}
		}
	}

	switch current {
	case OrderStatusPendingPayment:
		return []string{OrderStatusEscrowed, OrderStatusCancelled}
	case OrderStatusEscrowed:
		return []string{OrderStatusShipped, OrderStatusDelivered, OrderStatusRefunded, OrderStatusCancelled, OrderStatusCancelRequested}
	case OrderStatusShipped:
		return []string{OrderStatusDelivered, OrderStatusRefundRequested, OrderStatusRefunded}
	case OrderStatusDelivered:
		return []string{OrderStatusCompleted, OrderStatusRefundRequested}
	case OrderStatusRefundRequested:
		// Admin can refund; merchant rejection returns to delivered; admin can also mark completed.
		return []string{OrderStatusRefunded, OrderStatusDelivered, OrderStatusCompleted}
	case OrderStatusCancelRequested:
		// Seller approval cancels the order; rejection returns to escrowed; admin can also refund.
		return []string{OrderStatusCancelled, OrderStatusEscrowed, OrderStatusRefunded}
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
