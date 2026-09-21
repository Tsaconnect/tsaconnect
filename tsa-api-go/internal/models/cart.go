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
// Buyer pays 0% platform fee. The 2% merchant fee is baked into listing price.
// Buyer only pays a flat gas fee per order for on-chain transaction costs.
//
// MerchantFee/GasFee are the two surfaced fee line items (design §6): the
// percent merchant component (FeatureProductMerchant, MCGP-exempt → $0) and the
// flat gas component (FeatureProduct flat). Both are sourced from the unified
// fee-config service via the injected lookups (see CartFeeLookups), so the cart
// QUOTE equals the on-chain CHARGE.
type CartSummary struct {
	TotalItems    int     `json:"totalItems"`
	TotalQuantity int     `json:"totalQuantity"`
	Subtotal      float64 `json:"subtotal"`
	Shipping      float64 `json:"shipping"`
	GasFee        float64 `json:"gasFee"`
	MerchantFee   float64 `json:"merchantFee"` // percent merchant component (informational; baked into listing price)
	PlatformFee   float64 `json:"platformFee"` // 0 for buyer — merchant fee is in listing price
	Discount      float64 `json:"discount"`
	Total         float64 `json:"total"`
}

// Fee configuration — split/display constants still referenced by GetFeeConfig.
// The merchant rate (FeatureProductMerchant) and the flat gas fee
// (FeatureProduct) are NO LONGER hardcoded here: they are resolved through the
// fee-config service so the buyer quote and the on-chain charge share one source
// of truth (design §6). The remaining constants describe the on-chain split that
// has no service-backed row yet.
const (
	BuyerCashbackBPS = 50  // 0.5% cashback to buyer
	UplineFeeBPS     = 50  // 0.5% bonus to upline
	SystemFeeBPS     = 100 // 1% to system wallet (remainder of 2%)
)

// CartFeeLookups injects the fee-config service reads into the models package.
// The models package cannot import services (services imports models — that
// would be an import cycle), so the cart handler threads these wrappers in (they
// are services.FeeRate / services.FeeFlatUSD bound to FeatureProductMerchant /
// FeatureProduct). The checkout charge itself is priced from the escrow's
// on-chain rates (handlers.resolvePricingRates), which the replica these read
// mirrors; the checkout quote endpoint, not this summary, is quote == charge.
//
// A nil CartFeeLookups (or nil funcs) means "no fee" — never a hardcoded
// fallback that could disagree with the service.
type CartFeeLookups struct {
	// MerchantBPS resolves the percent merchant component for the settlement
	// token: services.FeeRate(FeatureProductMerchant, token, nil). Returns
	// (bps, exempt).
	MerchantBPS func(token string) (bps int, exempt bool)
	// GasFlatUSD resolves the flat gas component for the settlement token:
	// services.FeeFlatUSD(FeatureProduct, token, nil). Returns (flatUSD, exempt).
	GasFlatUSD func(token string) (flatUSD float64, exempt bool)
}

// GetFeeConfig returns the fee schedule for the frontend.
//
// The merchant rate and the flat gas fee are now sourced from the fee-config
// service via the injected lookups (design §6), so the displayed schedule, the
// buyer quote (CalculateCartSummary), and the on-chain charge share one source
// of truth — no hardcoded constant that could drift after an admin edit. A nil
// lookups (or nil funcs) reports zero for the migrated fields rather than a
// stale constant. The non-MCGP token "USDC" is used as the representative
// settlement token for the displayed (token-agnostic) schedule.
func GetFeeConfig(fees *CartFeeLookups) map[string]interface{} {
	merchantBPS := 0
	gasFeeUSD := 0.0
	if fees != nil {
		if fees.MerchantBPS != nil {
			merchantBPS, _ = fees.MerchantBPS("USDC")
		}
		if fees.GasFlatUSD != nil {
			gasFeeUSD, _ = fees.GasFlatUSD("USDC")
		}
	}
	return map[string]interface{}{
		"merchantFeeBPS":       merchantBPS,
		"mcgpMerchantFeeBPS":   0, // product_merchant row is MCGP-exempt
		"buyerCashbackBPS":     BuyerCashbackBPS,
		"uplineFeeBPS":         UplineFeeBPS,
		"systemFeeBPS":         SystemFeeBPS,
		"merchantFeePercent":   float64(merchantBPS) / 100.0,
		"buyerCashbackPercent": float64(BuyerCashbackBPS) / 100.0, // 0.5
		"uplineFeePercent":     float64(UplineFeeBPS) / 100.0,     // 0.5
		"gasFeeUSD":            gasFeeUSD,
		"buyerPlatformFee":     0, // buyer pays 0% platform fee
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
// Buyer pays 0% platform fee — the merchant fee is baked into listing prices.
// Buyer only pays a flat gas fee per order for on-chain costs.
//
// token is the settlement token for the eventual order (USDC/USDT/MCGP); fees
// injects the fee-config service reads (nil chain = global). Both fee line items
// are resolved through fees so the cart QUOTE equals the on-chain CHARGE (design
// §6, B5/I8):
//
//   - GasFee     — the flat gas component, FeeFlatUSD(FeatureProduct, token).
//     MCGP resolves exempt → $0 (mirrors PrepareApprove adding no gas for MCGP).
//   - MerchantFee — the percent merchant component (informational; the buyer does
//     not pay it on top — it is baked into the listing price). MCGP-exempt → $0.
//
// A nil fees (or nil func) yields a $0 component rather than a hardcoded
// fallback that could disagree with the service.
func CalculateCartSummary(items []CartItem, coupon *AppliedCoupon, currentShipping float64, token string, fees *CartFeeLookups) CartSummary {
	summary := CartSummary{
		TotalItems: len(items),
		Shipping:   currentShipping,
	}

	for _, item := range items {
		summary.TotalQuantity += item.Quantity
		summary.Subtotal += item.Price * float64(item.Quantity)
	}

	// Buyer platform fee is 0% — merchant absorbs the merchant fee in listing price.
	summary.PlatformFee = 0

	if len(items) > 0 && fees != nil {
		// Flat gas fee per order (for on-chain escrow approval). Read from the
		// SAME service the charge reads (FeatureProduct flat); MCGP → exempt $0.
		if fees.GasFlatUSD != nil {
			if flat, exempt := fees.GasFlatUSD(token); !exempt {
				summary.GasFee = flat
			}
		}
		// Percent merchant component, surfaced as an informational line item
		// (FeatureProductMerchant). It is baked into the listing price, so it is
		// NOT added to Total — but it is computed from the same rate the charge
		// uses so the quote and charge agree. MCGP → exempt $0.
		if fees.MerchantBPS != nil {
			if bps, exempt := fees.MerchantBPS(token); !exempt {
				summary.MerchantFee = summary.Subtotal * (float64(bps) / 10000.0)
			}
		}
	}

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

	summary.Total = summary.Subtotal + summary.Shipping + summary.GasFee - summary.Discount
	if summary.Total < 0 {
		summary.Total = 0
	}

	return summary
}
