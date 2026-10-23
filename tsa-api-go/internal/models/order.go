package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
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
	Kind            string `gorm:"default:'product';index" json:"kind"`
	Quantity        int    `gorm:"not null;default:1" json:"quantity"`
	Token           string `gorm:"not null" json:"token"`
	ProductAmount   string `gorm:"not null" json:"productAmount"`
	ShippingAmount  string `gorm:"not null;default:'0'" json:"shippingAmount"`
	PlatformFee     string `gorm:"not null;default:'0'" json:"platformFee"`
	TotalAmount     string `gorm:"not null" json:"totalAmount"`
	ShippingZone    string `json:"shippingZone"`
	ShippingCity    string `json:"shippingCity,omitempty"`
	ShippingState   string `json:"shippingState,omitempty"`
	ShippingCountry string `json:"shippingCountry,omitempty"`
	// Snapshot of the buyer's saved delivery address at checkout. Empty on
	// service orders and on orders placed before saved addresses existed.
	ShippingRecipientName string `json:"shippingRecipientName,omitempty"`
	ShippingPhone         string `json:"shippingPhone,omitempty"`
	ShippingStreet        string `json:"shippingStreet,omitempty"`
	ContractOrderID       string `json:"contractOrderId,omitempty"`
	EscrowContractAddress string `json:"escrowContractAddress,omitempty"`
	// EscrowSellerWallet is the seller wallet the buyer was told to escrow to
	// (set at prepare-escrow), so a wallet change afterwards cannot make a
	// correct payment look like a mismatch.
	EscrowSellerWallet string `gorm:"size:42" json:"-"`
	// PayoutFee is the part of ShippingAmount that pays the offramp for a
	// seller's bank payout (base units). The seller's own shipping is the rest.
	PayoutFee              string     `gorm:"size:78" json:"payoutFee,omitempty"`
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
	// PaymentSource is how the order is funded. Empty is the ProductEscrow
	// contract (the buyer's wallet approved and deposited the token); every
	// order placed before Spendable MCGP checkout reads empty and keeps its
	// on-chain lifecycle. OrderPaymentSpendableMCGP is an operator debit of the
	// buyer's Spendable MCGP (see OrderMcgpPayment), set when that debit starts.
	PaymentSource string `gorm:"type:varchar(24);not null;default:''" json:"paymentSource,omitempty"`
	// CheckoutGroupID is shared by the orders one cart checkout created. A
	// checkout funded by swapping another token pays the group's total in one
	// swap (services/marketplace_funding.go), so the server sizes that swap from
	// these orders — never from a client's amount. Nil on older orders and on
	// single service orders.
	CheckoutGroupID *uuid.UUID `gorm:"type:uuid;index" json:"checkoutGroupId,omitempty"`
	// FundWithSwap marks orders created to be paid by a funding swap: their
	// buyer was short of the order token, so they hold stock with no funds
	// behind them yet. A buyer may have at most one such checkout awaiting
	// payment (CheckoutHandler.CreateOrderFromCart).
	FundWithSwap bool `gorm:"not null;default:false" json:"fundWithSwap,omitempty"`
	// MenuSelections records the ordered items for an Order-type service order as
	// a JSON array of OrderMenuSelection. Kept in a dedicated column (NOT Notes,
	// which ship/refund handlers overwrite) so the merchant can fulfil the order.
	// Empty for products, contact and booking services.
	MenuSelections datatypes.JSON `gorm:"type:jsonb" json:"menuSelections,omitempty"`
	// SelectedAttributes carries the buyer's chosen product specs (colour/size/
	// other) for a PRODUCT order — a JSON array of OrderAttribute — so the merchant
	// knows how to arrange the purchase. Copied from the cart item at checkout.
	SelectedAttributes datatypes.JSON `gorm:"type:jsonb" json:"selectedAttributes,omitempty"`
	// PurchaseNote is the buyer's free-text arrangement note, captured at checkout.
	// Kept SEPARATE from Notes (which ship/refund handlers overwrite with
	// merchant-side notes) so the buyer's instructions are never clobbered.
	PurchaseNote string `json:"purchaseNote,omitempty"`
	// ServiceAnswers records the buyer's submitted booking-form answers for a
	// service order (unified booking engine) as a JSON object keyed by the
	// service's FieldConfig field keys. Empty for products and legacy services.
	ServiceAnswers datatypes.JSON `gorm:"type:jsonb" json:"serviceAnswers,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// OrderAttribute is one selected product spec on an order (e.g. Color → "White, Blue").
type OrderAttribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SetSelectedAttributes serializes the selected specs into the JSONB column.
func (o *Order) SetSelectedAttributes(attrs []OrderAttribute) {
	if len(attrs) == 0 {
		o.SelectedAttributes = nil
		return
	}
	data, _ := json.Marshal(attrs)
	o.SelectedAttributes = data
}

// GetSelectedAttributes deserializes the JSONB column.
func (o *Order) GetSelectedAttributes() []OrderAttribute {
	var attrs []OrderAttribute
	if o.SelectedAttributes != nil {
		_ = json.Unmarshal(o.SelectedAttributes, &attrs)
	}
	return attrs
}

// OrderMenuSelection is one line of a menu order: the menu item, the quantity,
// and the server-recomputed unit price (USD) captured at checkout time.
type OrderMenuSelection struct {
	ItemID       uuid.UUID `json:"itemId"`
	Name         string    `json:"name"`
	Quantity     int       `json:"quantity"`
	UnitPriceUSD float64   `json:"unitPriceUsd"`
}

// SetMenuSelections serializes selections into the JSONB column.
func (o *Order) SetMenuSelections(sel []OrderMenuSelection) {
	if len(sel) == 0 {
		o.MenuSelections = nil
		return
	}
	data, _ := json.Marshal(sel)
	o.MenuSelections = data
}

// GetMenuSelections deserializes the JSONB column.
func (o *Order) GetMenuSelections() []OrderMenuSelection {
	var sel []OrderMenuSelection
	if o.MenuSelections != nil {
		_ = json.Unmarshal(o.MenuSelections, &sel)
	}
	return sel
}

// SetServiceAnswers serializes the booking-form answers into the JSONB column.
func (o *Order) SetServiceAnswers(answers map[string]interface{}) {
	if len(answers) == 0 {
		o.ServiceAnswers = nil
		return
	}
	data, _ := json.Marshal(answers)
	o.ServiceAnswers = data
}

// GetServiceAnswers deserializes the JSONB column.
func (o *Order) GetServiceAnswers() map[string]interface{} {
	answers := map[string]interface{}{}
	if o.ServiceAnswers != nil {
		_ = json.Unmarshal(o.ServiceAnswers, &answers)
	}
	return answers
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

// OrderPaymentSpendableMCGP marks an order paid from the buyer's Spendable
// MCGP (the entitlement contract's premium balance) instead of ProductEscrow.
const OrderPaymentSpendableMCGP = "spendable_mcgp"

// IsSpendableMCGP reports whether the order is funded by a Spendable MCGP hold.
func (o *Order) IsSpendableMCGP() bool { return o.PaymentSource == OrderPaymentSpendableMCGP }

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
