package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// BookingRequest is the off-chain pre-order for non-instant bookings (unified
// booking engine, Phase 2+). When a service's BookingType is not "instant", the
// buyer's submitted form does NOT create an escrowed Order immediately — it
// creates a BookingRequest the merchant can approve/reject (and, in later
// phases, quote). Only when a positive amount exists and the buyer pays is an
// Order created and escrowed, satisfying the on-chain productAmount>0 invariant.
//
// Lifecycle: requested → (approved | rejected) → [buyer pays] → converted.
// A quote flow (Phase 3) adds QuotedAmountUSD/QuoteExpiresAt; not exercised yet.
type BookingRequest struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ProductID uuid.UUID `gorm:"type:uuid;not null;index" json:"productId"`
	BuyerID   uuid.UUID `gorm:"type:uuid;not null;index" json:"buyerId"`
	SellerID  uuid.UUID `gorm:"type:uuid;not null;index" json:"sellerId"`

	// Token + buyer location captured at request time so the price computed at
	// approval/pay matches what the buyer saw.
	Token        string `json:"token"`
	ServiceTier  string `json:"serviceTier,omitempty"`
	BuyerCity    string `json:"buyerCity,omitempty"`
	BuyerState   string `json:"buyerState,omitempty"`
	BuyerCountry string `json:"buyerCountry,omitempty"`

	// Answers is the submitted booking-form payload (keyed by FieldConfig keys).
	Answers datatypes.JSON `gorm:"type:jsonb" json:"answers,omitempty"`

	Status          string   `gorm:"not null;default:'requested';index" json:"status"`
	RejectReason    string   `json:"rejectReason,omitempty"`
	QuotedAmountUSD *float64 `json:"quotedAmountUsd,omitempty"` // set by the provider when approving a price-on-request booking
	// OrderID is set once the request is paid and converted to an escrowed order.
	OrderID *uuid.UUID `gorm:"type:uuid" json:"orderId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (BookingRequest) TableName() string {
	return "booking_requests"
}

// Booking-request status constants.
const (
	BookingRequestRequested = "requested"
	BookingRequestApproved  = "approved"
	BookingRequestRejected  = "rejected"
	BookingRequestExpired   = "expired"
	BookingRequestConverted = "converted" // paid → order created
	BookingRequestCancelled = "cancelled"
)

// ValidNextBookingRequestStatuses returns the allowed transitions from current.
func ValidNextBookingRequestStatuses(current string) []string {
	switch current {
	case BookingRequestRequested:
		return []string{BookingRequestApproved, BookingRequestRejected, BookingRequestCancelled, BookingRequestExpired}
	case BookingRequestApproved:
		return []string{BookingRequestConverted, BookingRequestCancelled, BookingRequestExpired}
	default:
		return nil
	}
}

// CanTransitionBookingRequest reports whether current → next is allowed.
func CanTransitionBookingRequest(current, next string) bool {
	for _, s := range ValidNextBookingRequestStatuses(current) {
		if s == next {
			return true
		}
	}
	return false
}

// SetAnswers serializes the booking-form answers into the JSONB column.
func (r *BookingRequest) SetAnswers(answers map[string]interface{}) {
	if len(answers) == 0 {
		r.Answers = nil
		return
	}
	data, _ := json.Marshal(answers)
	r.Answers = data
}

// GetAnswers deserializes the JSONB column.
func (r *BookingRequest) GetAnswers() map[string]interface{} {
	answers := map[string]interface{}{}
	if r.Answers != nil {
		_ = json.Unmarshal(r.Answers, &answers)
	}
	return answers
}
