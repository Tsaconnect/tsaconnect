package models

import (
	"time"

	"github.com/google/uuid"
)

// ServiceBooking is a reserved time slot for a Booking-type service order. It is
// 1:1 with the order (one order per cart line item). Slot capacity is enforced
// at checkout by SUMming PartySize over bookings whose order is not in a
// cancelled/refunded terminal state (see checkout_handler.go), so a cancel/refund
// automatically frees the slot without a separate status write.
type ServiceBooking struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrderID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"orderId"`
	ProductID uuid.UUID `gorm:"type:uuid;not null;index:idx_booking_slot" json:"productId"`
	BuyerID   uuid.UUID `gorm:"type:uuid;not null;index" json:"buyerId"`
	SlotStart time.Time `gorm:"not null;index:idx_booking_slot" json:"slotStart"`
	SlotEnd   time.Time `gorm:"not null" json:"slotEnd"`
	PartySize int       `gorm:"not null;default:1" json:"partySize"`
	Status    string    `gorm:"not null;default:'reserved';index" json:"status"` // reserved|cancelled|completed
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (ServiceBooking) TableName() string {
	return "service_bookings"
}

// ServiceBooking status constants.
const (
	ServiceBookingStatusReserved  = "reserved"
	ServiceBookingStatusCancelled = "cancelled"
	ServiceBookingStatusCompleted = "completed"
)
