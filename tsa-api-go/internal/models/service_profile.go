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

	// Unified booking-engine config (Phase 1: only the instant/fixed/auto_accept/
	// pay_before defaults are exercised; the other enum values are forward-compat
	// for the request/quote/deposit phases). Defaults keep every existing listing
	// behaving exactly as before.
	BookingType    string  `gorm:"default:'instant'" json:"bookingType"`      // instant | request_approval | quote_before_payment
	PricingMethod  string  `gorm:"default:'fixed'" json:"pricingMethod"`      // fixed | starting_from | dynamic | quote_later
	Workflow       string  `gorm:"default:'auto_accept'" json:"workflow"`     // auto_accept | manual_accept | quote_required
	PaymentTiming  string  `gorm:"default:'pay_before'" json:"paymentTiming"` // pay_before | pay_after | deposit
	DepositPercent int     `gorm:"default:0" json:"depositPercent"`           // 0..100, meaningful when PaymentTiming=deposit
	BookingFeeUSD  float64 `gorm:"default:0" json:"bookingFeeUsd"`            // PM "Booking Fee" (distinct from access/callout)

	// FieldConfig is the declarative set of enabled customer-form fields (a
	// BookingFieldConfig). Null means "use the legacy per-serviceType flow" so the
	// rollout is non-breaking. See booking_fields.go.
	FieldConfig datatypes.JSON `gorm:"type:jsonb" json:"fieldConfig,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Booking-engine enum constants (ServiceProfile config dimensions).
const (
	BookingTypeInstant         = "instant"
	BookingTypeRequestApproval = "request_approval"
	BookingTypeQuoteFirst      = "quote_before_payment"

	PricingMethodFixed        = "fixed"
	PricingMethodStartingFrom = "starting_from"
	PricingMethodDynamic      = "dynamic"
	PricingMethodQuoteLater   = "quote_later"

	WorkflowAutoAccept    = "auto_accept"
	WorkflowManualAccept  = "manual_accept"
	WorkflowQuoteRequired = "quote_required"

	PaymentTimingBefore  = "pay_before"
	PaymentTimingAfter   = "pay_after"
	PaymentTimingDeposit = "deposit"
)

// ValidBookingType reports whether s is a known booking type.
func ValidBookingType(s string) bool {
	switch s {
	case BookingTypeInstant, BookingTypeRequestApproval, BookingTypeQuoteFirst:
		return true
	}
	return false
}

// ValidPricingMethod reports whether s is a known pricing method.
func ValidPricingMethod(s string) bool {
	switch s {
	case PricingMethodFixed, PricingMethodStartingFrom, PricingMethodDynamic, PricingMethodQuoteLater:
		return true
	}
	return false
}

// ValidWorkflow reports whether s is a known workflow.
func ValidWorkflow(s string) bool {
	switch s {
	case WorkflowAutoAccept, WorkflowManualAccept, WorkflowQuoteRequired:
		return true
	}
	return false
}

// ValidPaymentTiming reports whether s is a known payment timing.
func ValidPaymentTiming(s string) bool {
	switch s {
	case PaymentTimingBefore, PaymentTimingAfter, PaymentTimingDeposit:
		return true
	}
	return false
}

// TableName overrides the default table name.
func (ServiceProfile) TableName() string {
	return "service_profiles"
}
