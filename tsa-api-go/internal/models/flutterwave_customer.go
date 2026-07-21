package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FlutterwaveCustomer persists the company-level Flutterwave customer id used as
// the parent for ALL virtual accounts (IT dynamic + PM static), so it's created
// exactly once and reused on every boot — no FLUTTERWAVE_CUSTOMER_ID env to hunt
// down in the dashboard.
//
// Keyed by BaseURL: the sandbox and live FW APIs mint DIFFERENT customer ids, so
// switching FLUTTERWAVE_BASE_URL must never reuse a stale cross-environment id.
// One company customer per environment.
type FlutterwaveCustomer struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BaseURL    string    `gorm:"not null;uniqueIndex" json:"baseUrl"`
	CustomerID string    `gorm:"not null" json:"customerId"`
	Email      string    `json:"email"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (FlutterwaveCustomer) TableName() string { return "flutterwave_customers" }

func (f *FlutterwaveCustomer) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	return nil
}
