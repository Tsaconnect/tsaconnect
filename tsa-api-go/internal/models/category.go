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
	ServiceFeeMode string `gorm:"default:'standard'" json:"serviceFeeMode"`
	// ServiceType classifies a Service/Both category into one of the three
	// first-class service interaction types the marketplace surfaces as sub-tabs:
	//   "contact" — pay to unlock the provider's contact and engage (default);
	//   "order"   — pick items from a menu and place an order + delivery;
	//   "booking" — reserve a date/time slot.
	// It drives the merchant listing form and the buyer flow. Ignored for Product
	// categories. Defaults to "contact" so existing Service categories keep their
	// current pay-to-reveal behaviour; the booking backfill maps legacy fare
	// categories (see config/database.go). Supersedes ServiceFeeMode for the new
	// flows, which is retained for legacy standard/booking pricing fallbacks.
	ServiceType string    `gorm:"default:'contact'" json:"serviceType"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
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

// Service-type constants for Category.ServiceType (the three marketplace tabs).
const (
	ServiceTypeContact = "contact" // pay access fee → unlock/engage (default)
	ServiceTypeOrder   = "order"   // select menu items → order + delivery
	ServiceTypeBooking = "booking" // reserve a date/time slot
)

// ValidServiceType reports whether s is one of the three service types.
func ValidServiceType(s string) bool {
	switch s {
	case ServiceTypeContact, ServiceTypeOrder, ServiceTypeBooking:
		return true
	default:
		return false
	}
}

// EffectiveServiceType resolves a category's service type, falling back to the
// legacy ServiceFeeMode when ServiceType is unset/invalid. A zero-value Category
// (e.g. a service whose CategoryID is nil) resolves to "contact" — the default
// pay-to-reveal behaviour. This is the single source of truth for both the
// checkout pricing resolver and the contact-fee handlers.
func EffectiveServiceType(cat Category) string {
	if ValidServiceType(cat.ServiceType) {
		return cat.ServiceType
	}
	if cat.ServiceFeeMode == ServiceFeeModeBooking {
		return ServiceTypeBooking
	}
	return ServiceTypeContact
}

// HostsServices reports whether a category can hold service listings. "Both"
// counts: such a category hosts services alongside products.
func HostsServices(cat Category) bool {
	return cat.Type == CategoryTypeService || cat.Type == CategoryTypeBoth
}

// ServiceCategoryIDs selects the categories that host services, optionally
// narrowed to a single effective service type. Pass "" for every service
// category.
//
// Callers use this to bucket LISTINGS by service type: a listing's own Type
// field is merchant-entered and frequently says "Product" for what is plainly a
// service, so the category is the reliable classifier. Resolution goes through
// EffectiveServiceType, which keeps legacy fare-mode categories in the booking
// bucket. An unrecognised serviceType matches nothing rather than everything —
// a typo'd filter should return an empty list, not the whole catalogue.
func ServiceCategoryIDs(categories []Category, serviceType string) []uuid.UUID {
	if serviceType != "" && !ValidServiceType(serviceType) {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(categories))
	for _, cat := range categories {
		if !HostsServices(cat) {
			continue
		}
		if serviceType != "" && EffectiveServiceType(cat) != serviceType {
			continue
		}
		ids = append(ids, cat.ID)
	}
	return ids
}

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
