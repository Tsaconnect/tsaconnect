package models

import (
	"time"

	"github.com/google/uuid"
)

// ServiceContactPayment records a contact fee payment for a service listing.
type ServiceContactPayment struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CallerID          uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_caller_service" json:"callerId"`
	ServiceProviderID uuid.UUID `gorm:"type:uuid;not null" json:"serviceProviderId"`
	ServiceID         uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_caller_service" json:"serviceId"`
	Token             string    `json:"token"`
	FeeAmount         string    `json:"feeAmount"`
	ApproveTxHash     string    `json:"approveTxHash"`
	PayFeeTxHash      string    `json:"payFeeTxHash"`
	CreatedAt         time.Time `json:"createdAt"`
}

// TableName overrides the default table name.
func (ServiceContactPayment) TableName() string {
	return "service_contact_payments"
}
