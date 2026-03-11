package models

import (
	"math"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CartItemAttribute represents a selected attribute on a cart item.
type CartItemAttribute struct {
	Name  string `bson:"name" json:"name"`
	Value string `bson:"value" json:"value"`
}

// CartItem represents an individual item in a shopping cart.
type CartItem struct {
	ID                 primitive.ObjectID  `bson:"_id,omitempty" json:"id"`
	Product            primitive.ObjectID  `bson:"product" json:"product"`
	Seller             primitive.ObjectID  `bson:"seller" json:"seller"`
	Quantity           int                 `bson:"quantity" json:"quantity"`
	Price              float64             `bson:"price" json:"price"`
	SelectedAttributes []CartItemAttribute `bson:"selectedAttributes,omitempty" json:"selectedAttributes,omitempty"`
	Notes              string              `bson:"notes,omitempty" json:"notes,omitempty"`
	AddedAt            time.Time           `bson:"addedAt" json:"addedAt"`
	UpdatedAt          time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// CartSummary holds aggregated cart totals.
type CartSummary struct {
	TotalItems    int     `bson:"totalItems" json:"totalItems"`
	TotalQuantity int     `bson:"totalQuantity" json:"totalQuantity"`
	Subtotal      float64 `bson:"subtotal" json:"subtotal"`
	Shipping      float64 `bson:"shipping" json:"shipping"`
	Tax           float64 `bson:"tax" json:"tax"`
	Discount      float64 `bson:"discount" json:"discount"`
	Total         float64 `bson:"total" json:"total"`
}

// Address represents a shipping address.
type Address struct {
	Name        string `bson:"name,omitempty" json:"name,omitempty"`
	PhoneNumber string `bson:"phoneNumber,omitempty" json:"phoneNumber,omitempty"`
	Address     string `bson:"address,omitempty" json:"address,omitempty"`
	City        string `bson:"city,omitempty" json:"city,omitempty"`
	State       string `bson:"state,omitempty" json:"state,omitempty"`
	Country     string `bson:"country,omitempty" json:"country,omitempty"`
	PostalCode  string `bson:"postalCode,omitempty" json:"postalCode,omitempty"`
	IsDefault   bool   `bson:"isDefault,omitempty" json:"isDefault,omitempty"`
}

// BillingAddress represents a billing address with a same-as-shipping flag.
type BillingAddress struct {
	SameAsShipping bool   `bson:"sameAsShipping" json:"sameAsShipping"`
	Name           string `bson:"name,omitempty" json:"name,omitempty"`
	PhoneNumber    string `bson:"phoneNumber,omitempty" json:"phoneNumber,omitempty"`
	Address        string `bson:"address,omitempty" json:"address,omitempty"`
	City           string `bson:"city,omitempty" json:"city,omitempty"`
	State          string `bson:"state,omitempty" json:"state,omitempty"`
	Country        string `bson:"country,omitempty" json:"country,omitempty"`
	PostalCode     string `bson:"postalCode,omitempty" json:"postalCode,omitempty"`
}

// PaymentDetails holds payment-related details for the cart.
type PaymentDetails struct {
	CardLastFour  string `bson:"cardLastFour,omitempty" json:"cardLastFour,omitempty"`
	CardBrand     string `bson:"cardBrand,omitempty" json:"cardBrand,omitempty"`
	BankName      string `bson:"bankName,omitempty" json:"bankName,omitempty"`
	AccountNumber string `bson:"accountNumber,omitempty" json:"accountNumber,omitempty"`
	WalletAddress string `bson:"walletAddress,omitempty" json:"walletAddress,omitempty"`
}

// AppliedCoupon represents a coupon applied to a cart.
type AppliedCoupon struct {
	Code          string    `bson:"code,omitempty" json:"code,omitempty"`
	DiscountType  string    `bson:"discountType,omitempty" json:"discountType,omitempty"`
	DiscountValue float64   `bson:"discountValue,omitempty" json:"discountValue,omitempty"`
	MaxDiscount   float64   `bson:"maxDiscount,omitempty" json:"maxDiscount,omitempty"`
	MinPurchase   float64   `bson:"minPurchase,omitempty" json:"minPurchase,omitempty"`
	ExpiresAt     time.Time `bson:"expiresAt,omitempty" json:"expiresAt,omitempty"`
	AppliedAt     time.Time `bson:"appliedAt,omitempty" json:"appliedAt,omitempty"`
}

// EstimatedDelivery represents estimated delivery dates.
type EstimatedDelivery struct {
	From time.Time `bson:"from,omitempty" json:"from,omitempty"`
	To   time.Time `bson:"to,omitempty" json:"to,omitempty"`
}

// Cart represents a user's shopping cart.
type Cart struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	User              primitive.ObjectID `bson:"user" json:"user"`
	Items             []CartItem         `bson:"items" json:"items"`
	Summary           CartSummary        `bson:"summary" json:"summary"`
	ShippingAddress   *Address           `bson:"shippingAddress,omitempty" json:"shippingAddress,omitempty"`
	BillingAddress    *BillingAddress    `bson:"billingAddress,omitempty" json:"billingAddress,omitempty"`
	PaymentMethod     string             `bson:"paymentMethod" json:"paymentMethod"`
	PaymentDetails    *PaymentDetails    `bson:"paymentDetails,omitempty" json:"paymentDetails,omitempty"`
	AppliedCoupon     *AppliedCoupon     `bson:"appliedCoupon,omitempty" json:"appliedCoupon,omitempty"`
	ShippingMethod    string             `bson:"shippingMethod" json:"shippingMethod"`
	ShippingProvider  string             `bson:"shippingProvider,omitempty" json:"shippingProvider,omitempty"`
	EstimatedDelivery *EstimatedDelivery `bson:"estimatedDelivery,omitempty" json:"estimatedDelivery,omitempty"`
	SessionID         string             `bson:"sessionId,omitempty" json:"sessionId,omitempty"`
	Currency          string             `bson:"currency" json:"currency"`
	Language          string             `bson:"language" json:"language"`
	Status            string             `bson:"status" json:"status"`
	LastActivity      time.Time          `bson:"lastActivity" json:"lastActivity"`
	AbandonedAt       *time.Time         `bson:"abandonedAt,omitempty" json:"abandonedAt,omitempty"`
	ConvertedAt       *time.Time         `bson:"convertedAt,omitempty" json:"convertedAt,omitempty"`
	ExpiresAt         time.Time          `bson:"expiresAt" json:"expiresAt"`
	CreatedAt         time.Time          `bson:"createdAt" json:"createdAt"`
	UpdatedAt         time.Time          `bson:"updatedAt" json:"updatedAt"`
}

// Cart payment method constants.
const (
	PaymentMethodCard            = "card"
	PaymentMethodBankTransfer    = "bank_transfer"
	PaymentMethodWallet          = "wallet"
	PaymentMethodCashOnDelivery  = "cash_on_delivery"
	PaymentMethodCrypto          = "crypto"
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

// CalculateSummary recalculates the cart summary based on current items and coupon.
func (c *Cart) CalculateSummary() CartSummary {
	summary := CartSummary{
		TotalItems: len(c.Items),
		Shipping:   c.Summary.Shipping,
	}

	for _, item := range c.Items {
		summary.TotalQuantity += item.Quantity
		summary.Subtotal += item.Price * float64(item.Quantity)
	}

	// Tax: 10% of subtotal
	summary.Tax = summary.Subtotal * 0.1

	// Apply discount if coupon is applied
	if c.AppliedCoupon != nil && c.AppliedCoupon.Code != "" {
		coupon := c.AppliedCoupon
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

	summary.Total = summary.Subtotal + summary.Shipping + summary.Tax - summary.Discount
	if summary.Total < 0 {
		summary.Total = 0
	}

	return summary
}
