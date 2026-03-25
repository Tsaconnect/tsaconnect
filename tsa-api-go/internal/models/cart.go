package models

import (
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// CartItemAttribute represents a selected attribute on a cart item.
type CartItemAttribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CartItem represents an individual item in a shopping cart.
type CartItem struct {
	ID                 uuid.UUID           `json:"id"`
	Product            uuid.UUID           `json:"product"`
	Seller             uuid.UUID           `json:"seller"`
	Quantity           int                 `json:"quantity"`
	Price              float64             `json:"price"`
	SelectedAttributes []CartItemAttribute `json:"selectedAttributes,omitempty"`
	Notes              string              `json:"notes,omitempty"`
	AddedAt            time.Time           `json:"addedAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
}

// CartSummary holds aggregated cart totals.
type CartSummary struct {
	TotalItems    int     `json:"totalItems"`
	TotalQuantity int     `json:"totalQuantity"`
	Subtotal      float64 `json:"subtotal"`
	Shipping      float64 `json:"shipping"`
	PlatformFee   float64 `json:"platformFee"`
	Discount      float64 `json:"discount"`
	Total         float64 `json:"total"`
}

// Fee configuration — matches smart contract ProductEscrow.sol constants.
const (
	PlatformFeeBPS     = 1000 // 10% for USDC/USDT
	MCGPPlatformFeeBPS = 0    // 0% for MCGP
	BuyerCashbackBPS   = 250  // 2.5%
	UplineFeeBPS       = 250  // 2.5%
)

// FeeConfig returns the platform fee schedule for the frontend.
func GetFeeConfig() map[string]interface{} {
	return map[string]interface{}{
		"platformFeeBPS":     PlatformFeeBPS,
		"mcgpPlatformFeeBPS": MCGPPlatformFeeBPS,
		"buyerCashbackBPS":   BuyerCashbackBPS,
		"uplineFeeBPS":       UplineFeeBPS,
		"platformFeePercent": float64(PlatformFeeBPS) / 100.0,   // 10.0
		"buyerCashbackPercent": float64(BuyerCashbackBPS) / 100.0, // 2.5
		"uplineFeePercent":   float64(UplineFeeBPS) / 100.0,     // 2.5
	}
}

// Address represents a shipping address.
type Address struct {
	Name        string `json:"name,omitempty"`
	PhoneNumber string `json:"phoneNumber,omitempty"`
	Address     string `json:"address,omitempty"`
	City        string `json:"city,omitempty"`
	State       string `json:"state,omitempty"`
	Country     string `json:"country,omitempty"`
	PostalCode  string `json:"postalCode,omitempty"`
	IsDefault   bool   `json:"isDefault,omitempty"`
}

// BillingAddress represents a billing address with a same-as-shipping flag.
type BillingAddress struct {
	SameAsShipping bool   `json:"sameAsShipping"`
	Name           string `json:"name,omitempty"`
	PhoneNumber    string `json:"phoneNumber,omitempty"`
	Address        string `json:"address,omitempty"`
	City           string `json:"city,omitempty"`
	State          string `json:"state,omitempty"`
	Country        string `json:"country,omitempty"`
	PostalCode     string `json:"postalCode,omitempty"`
}

// PaymentDetails holds payment-related details for the cart.
type PaymentDetails struct {
	CardLastFour  string `json:"cardLastFour,omitempty"`
	CardBrand     string `json:"cardBrand,omitempty"`
	BankName      string `json:"bankName,omitempty"`
	AccountNumber string `json:"accountNumber,omitempty"`
	WalletAddress string `json:"walletAddress,omitempty"`
}

// AppliedCoupon represents a coupon applied to a cart.
type AppliedCoupon struct {
	Code          string    `json:"code,omitempty"`
	DiscountType  string    `json:"discountType,omitempty"`
	DiscountValue float64   `json:"discountValue,omitempty"`
	MaxDiscount   float64   `json:"maxDiscount,omitempty"`
	MinPurchase   float64   `json:"minPurchase,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt,omitempty"`
	AppliedAt     time.Time `json:"appliedAt,omitempty"`
}

// EstimatedDelivery represents estimated delivery dates.
type EstimatedDelivery struct {
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
}

// Cart represents a user's shopping cart.
type Cart struct {
	ID                uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID            uuid.UUID      `gorm:"type:uuid;column:user_id;index" json:"user"`
	Items             datatypes.JSON `gorm:"type:jsonb" json:"items"`
	Summary           datatypes.JSON `gorm:"type:jsonb" json:"summary"`
	ShippingAddress   datatypes.JSON `gorm:"type:jsonb" json:"shippingAddress,omitempty"`
	BillingAddress    datatypes.JSON `gorm:"type:jsonb" json:"billingAddress,omitempty"`
	PaymentMethod     string         `json:"paymentMethod"`
	PaymentDetails    datatypes.JSON `gorm:"type:jsonb" json:"paymentDetails,omitempty"`
	AppliedCoupon     datatypes.JSON `gorm:"type:jsonb" json:"appliedCoupon,omitempty"`
	ShippingMethod    string         `json:"shippingMethod"`
	ShippingProvider  string         `json:"shippingProvider,omitempty"`
	EstimatedDelivery datatypes.JSON `gorm:"type:jsonb" json:"estimatedDelivery,omitempty"`
	SessionID         string         `json:"sessionId,omitempty"`
	Currency          string         `json:"currency"`
	Language          string         `json:"language"`
	Status            string         `json:"status"`
	LastActivity      time.Time      `json:"lastActivity"`
	AbandonedAt       *time.Time     `json:"abandonedAt,omitempty"`
	ConvertedAt       *time.Time     `json:"convertedAt,omitempty"`
	ExpiresAt         time.Time      `json:"expiresAt"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Cart) TableName() string {
	return "carts"
}

// Cart payment method constants.
const (
	PaymentMethodCard           = "card"
	PaymentMethodBankTransfer   = "bank_transfer"
	PaymentMethodWallet         = "wallet"
	PaymentMethodCashOnDelivery = "cash_on_delivery"
	PaymentMethodCrypto         = "crypto"
)

// Cart shipping method constants.
const (
	ShippingMethodStandard = "standard"
	ShippingMethodExpress  = "express"
	ShippingMethodNextDay  = "next_day"
	ShippingMethodPickup   = "pickup"
)

// Cart status constants.
const (
	CartStatusActive    = "active"
	CartStatusAbandoned = "abandoned"
	CartStatusConverted = "converted"
	CartStatusExpired   = "expired"
	DefaultCartStatus   = CartStatusActive
)

// GetItems deserializes the Items JSONB field.
func (c *Cart) GetItems() []CartItem {
	var items []CartItem
	if c.Items != nil {
		_ = json.Unmarshal(c.Items, &items)
	}
	return items
}

// SetItems serializes the []CartItem slice into the JSONB field.
func (c *Cart) SetItems(items []CartItem) {
	data, _ := json.Marshal(items)
	c.Items = data
}

// GetSummary deserializes the Summary JSONB field.
func (c *Cart) GetSummary() CartSummary {
	var s CartSummary
	if c.Summary != nil {
		_ = json.Unmarshal(c.Summary, &s)
	}
	return s
}

// SetSummary serializes the CartSummary struct into the JSONB field.
func (c *Cart) SetSummary(s CartSummary) {
	data, _ := json.Marshal(s)
	c.Summary = data
}

// GetAppliedCoupon deserializes the AppliedCoupon JSONB field.
func (c *Cart) GetAppliedCoupon() *AppliedCoupon {
	if c.AppliedCoupon == nil {
		return nil
	}
	var ac AppliedCoupon
	if err := json.Unmarshal(c.AppliedCoupon, &ac); err != nil {
		return nil
	}
	if ac.Code == "" {
		return nil
	}
	return &ac
}

// SetAppliedCoupon serializes the AppliedCoupon struct into the JSONB field.
func (c *Cart) SetAppliedCoupon(ac *AppliedCoupon) {
	if ac == nil {
		c.AppliedCoupon = nil
		return
	}
	data, _ := json.Marshal(ac)
	c.AppliedCoupon = data
}

// CalculateCartSummary computes the cart summary from deserialized items and coupon.
// Platform fee matches the smart contract: 10% of subtotal for USDC/USDT, 0% for MCGP.
// The token parameter controls which fee rate to use; defaults to non-MCGP rate.
func CalculateCartSummary(items []CartItem, coupon *AppliedCoupon, currentShipping float64) CartSummary {
	summary := CartSummary{
		TotalItems: len(items),
		Shipping:   currentShipping,
	}

	for _, item := range items {
		summary.TotalQuantity += item.Quantity
		summary.Subtotal += item.Price * float64(item.Quantity)
	}

	// Platform fee: 10% of subtotal (matches ProductEscrow.sol PLATFORM_FEE_BPS = 1000)
	// This is the default (non-MCGP) rate shown in the cart.
	// The checkout screen adjusts to 0% when MCGP is selected.
	summary.PlatformFee = summary.Subtotal * float64(PlatformFeeBPS) / 10000.0

	// Apply discount if coupon is applied
	if coupon != nil && coupon.Code != "" {
		switch coupon.DiscountType {
		case "percentage":
			discount := summary.Subtotal * (coupon.DiscountValue / 100)
			if coupon.MaxDiscount > 0 {
				discount = math.Min(discount, coupon.MaxDiscount)
			}
			summary.Discount = discount
		case "fixed":
			summary.Discount = math.Min(coupon.DiscountValue, summary.Subtotal)
		case "free_shipping":
			summary.Discount = summary.Shipping
			summary.Shipping = 0
		}
	}

	summary.Total = summary.Subtotal + summary.Shipping + summary.PlatformFee - summary.Discount
	if summary.Total < 0 {
		summary.Total = 0
	}

	return summary
}
