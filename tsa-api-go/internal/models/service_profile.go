package models

import (
	"time"

	"github.com/google/uuid"
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

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (ServiceProfile) TableName() string {
	return "service_profiles"
}
