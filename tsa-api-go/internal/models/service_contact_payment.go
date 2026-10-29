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
	// ChainID and FeeRecipient are set on a payment made as a plain transfer to the core set's
	// FeeConfig fee recipient (contracts v2 slice 5); both are empty on a ServiceContact payment.
	ChainID      int64     `json:"chainId,omitempty"`
	FeeRecipient string    `gorm:"size:42" json:"feeRecipient,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ServiceContactIntent is what a caller was asked to pay for a contact reveal: the exact token,
// recipient and amount a plain transfer must match. One per prepare; a submit is accepted against
// any of the caller's intents for that service, so a fee change between prepare and submit never
// rejects a transfer that paid what it was quoted.
type ServiceContactIntent struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CallerID     uuid.UUID `gorm:"type:uuid;not null;index:idx_sc_intent_caller_service,priority:1" json:"callerId"`
	ServiceID    uuid.UUID `gorm:"type:uuid;not null;index:idx_sc_intent_caller_service,priority:2" json:"serviceId"`
	ChainID      int64     `gorm:"not null" json:"chainId"`
	TokenSymbol  string    `gorm:"size:8;not null" json:"tokenSymbol"`
	TokenAddress string    `gorm:"size:42;not null" json:"tokenAddress"`
	FeeRecipient string    `gorm:"size:42;not null" json:"feeRecipient"`
	PayerWallet  string    `gorm:"size:42;not null" json:"payerWallet"`
	Amount       string    `gorm:"size:78;not null" json:"amount"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (ServiceContactIntent) TableName() string {
	return "service_contact_intents"
}

// TableName overrides the default table name.
func (ServiceContactPayment) TableName() string {
	return "service_contact_payments"
}
