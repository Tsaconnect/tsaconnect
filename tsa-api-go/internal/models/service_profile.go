package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ServiceProfile holds the service-specific configuration for a service listing
// (a Product with Type="Service"). It is 1:1 with the product and only exists
// for services, keeping the overloaded products table free of service concerns.
//
// Which fields matter depends on the category's ServiceType:
//   - contact: AccessFeeUSD (engagement price), CalloutFeeUSD, coverage/address
//   - order:   coverage/address, operating hours (via ServiceAvailability)
//   - booking: AccessFeeUSD (optional), CalloutFeeUSD (optional), slot config,
//     policies, coverage/address, OperatingCompany (flight/transport)
//
// The tiered distance fee (booking/route or delivery fee) lives on the Product's
// Shipping* columns (+ ShippingResidential), reused so the escrow money path is
// unchanged.
type ServiceProfile struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ProductID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"productId"`

	// Flat merchant-set fees (USD). Tiered fees live on the Product (§3.2).
	AccessFeeUSD  float64 `gorm:"default:0" json:"accessFeeUsd"`  // contact: engagement price; booking: optional
	CalloutFeeUSD float64 `gorm:"default:0" json:"calloutFeeUsd"` // optional flat mobilisation fee

	// Booking config (booking type).
	SlotDurationMinutes int `gorm:"default:0" json:"slotDurationMinutes"` // service duration / slot length
	MaxCapacityPerSlot  int `gorm:"default:1" json:"maxCapacityPerSlot"`  // 0 = unlimited

	// Location / coverage (all types).
	ServiceAddress   string `json:"serviceAddress,omitempty"`
	CoverageArea     string `json:"coverageArea,omitempty"`
	OperatingCompany string `json:"operatingCompany,omitempty"` // flight/transport only

	// Policies (booking type) — free text shown to the buyer before reserving.
	CancellationPolicy string `json:"cancellationPolicy,omitempty"`
	ReschedulePolicy   string `json:"reschedulePolicy,omitempty"`
	RefundPolicy       string `json:"refundPolicy,omitempty"`
	BookingRules       string `json:"bookingRules,omitempty"`

	// Booking-engine config. Two dimensions, three valid combinations:
	//   instant + fixed        — buy now, pay the listing price into escrow
	//   request + fixed        — buyer requests, provider approves, pays listing price
	//   request + on_request   — buyer requests, provider approves & names the price
	// (instant + on_request is invalid — can't prepay an unnamed price.)
	BookingType   string  `gorm:"default:'instant'" json:"bookingType"` // instant | request
	PricingMethod string  `gorm:"default:'fixed'" json:"pricingMethod"` // fixed | on_request
	BookingFeeUSD float64 `gorm:"default:0" json:"bookingFeeUsd"`       // merchant "booking fee"

	// FieldConfig is the declarative set of enabled customer-form fields (a
	// BookingFieldConfig). Null means "use the legacy per-serviceType flow" so the
	// rollout is non-breaking. See booking_fields.go.
	FieldConfig datatypes.JSON `gorm:"type:jsonb" json:"fieldConfig,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Booking-engine enum constants (ServiceProfile config dimensions).
const (
	BookingTypeInstant = "instant"
	BookingTypeRequest = "request"

	PricingMethodFixed     = "fixed"
	PricingMethodOnRequest = "on_request"
)

// ValidBookingType reports whether s is a known booking type.
func ValidBookingType(s string) bool {
	return s == BookingTypeInstant || s == BookingTypeRequest
}

// ValidPricingMethod reports whether s is a known pricing method.
func ValidPricingMethod(s string) bool {
	return s == PricingMethodFixed || s == PricingMethodOnRequest
}

// TableName overrides the default table name.
func (ServiceProfile) TableName() string {
	return "service_profiles"
}
