package models

import (
	"time"

	"github.com/google/uuid"
)

// PMBalance is one payment merchant's fiat balance in ONE currency.
//
// It replaces the available_ngn / reserved_ngn columns on payment_merchants,
// which could only ever express a single implicit NGN balance. The defect there
// was never the naming — it was the missing dimension: a PM holding both naira
// and cedis needs rows, not columns.
//
// Semantics carry over unchanged from the NGN columns:
//   - Available is the total credited.
//   - Reserved is the portion ear-marked for in-flight sell trades.
//   - Free (spendable) balance is Available - Reserved.
//
// Amounts are decimal strings over numeric(38,2), matching the columns they
// replace. Every currency in the registry today is 2-decimal, so the scale is
// correct for all of them; the first 0- or 3-decimal currency is what would
// force this to become exponent-aware.
type PMBalance struct {
	PMID uuid.UUID `gorm:"type:uuid;primaryKey;column:pm_id" json:"pmId"`

	// Currency is the ISO 4217 code, uppercase. Validated against the currency
	// registry — never against display metadata.
	Currency string `gorm:"size:3;primaryKey" json:"currency"`

	Available string `gorm:"type:numeric(38,2);not null;default:0" json:"available"`
	Reserved  string `gorm:"type:numeric(38,2);not null;default:0" json:"reserved"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (PMBalance) TableName() string { return "pm_balances" }
