package models

import (
	"time"

	"github.com/google/uuid"
)

// Category represents a product/service category.
type Category struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Title            string     `gorm:"uniqueIndex;not null" json:"title"`
	Description      string     `json:"description,omitempty"`
	Type             string     `json:"type,omitempty"`
	ParentCategoryID *uuid.UUID `gorm:"type:uuid;column:parent_category_id" json:"parentCategory,omitempty"`
	Icon             string     `json:"icon,omitempty"`
	Image            string     `json:"image,omitempty"`
	Color            string     `json:"color"`
	IsActive         bool       `json:"isActive"`
	Order            int        `gorm:"column:sort_order" json:"order"`
	ProductCount     int        `json:"productCount"`
	// ServiceFeeMode governs how a Service/Both category charges at checkout:
	// "standard" — the merchant sets a fixed price + per-zone call-out fee on
	// the listing; "booking" — the admin sets a merged per-area fare schedule on
	// the category (CategoryBookingFee), which is the sole charge. Ignored for
	// Product categories. Defaults to standard so existing categories are unchanged.
	ServiceFeeMode string    `gorm:"default:'standard'" json:"serviceFeeMode"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (Category) TableName() string {
	return "categories"
}

// CategoryWithChildren extends Category with a recursive children list for tree views.
type CategoryWithChildren struct {
	Category
	Children []CategoryWithChildren `json:"children"`
}

// Category type constants.
const (
	CategoryTypeProduct = "Product"
	CategoryTypeService = "Service"
	CategoryTypeBoth    = "Both"

	DefaultCategoryColor = "#666666"
)

// Service fee-mode constants for Category.ServiceFeeMode.
const (
	ServiceFeeModeStandard = "standard" // merchant sets price + per-zone call-out (default)
	ServiceFeeModeBooking  = "booking"  // admin sets merged per-area fare; sole charge
)

// Booking-fee area tiers (Category booking mode). Ordered coarsest-last for UI.
const (
	BookingTierResidential    = "residential"
	BookingTierSameCity       = "same_city"
	BookingTierSameLGA        = "same_lga"
	BookingTierSameState      = "same_state"
	BookingTierSameCountry    = "same_country"
	BookingTierOutsideCountry = "outside_country"
)

// BookingFeeTiers lists the tiers in display order (near → far).
var BookingFeeTiers = []string{
	BookingTierResidential,
	BookingTierSameCity,
	BookingTierSameLGA,
	BookingTierSameState,
	BookingTierSameCountry,
	BookingTierOutsideCountry,
}

// CategoryBookingFee is one (category, tier) → fare row for a booking-mode
// category. The admin defines the full six-tier schedule; the buyer is charged
// the row matching their resolved area tier as the sole checkout amount.
type CategoryBookingFee struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CategoryID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_cat_booking_tier" json:"categoryId"`
	Tier       string    `gorm:"size:24;not null;uniqueIndex:idx_cat_booking_tier" json:"tier"`
	AmountUSD  float64   `gorm:"not null;default:0" json:"amountUsd"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (CategoryBookingFee) TableName() string {
	return "category_booking_fees"
}
