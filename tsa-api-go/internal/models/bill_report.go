package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	BillReportStatusOpen     = "open"
	BillReportStatusResolved = "resolved"
	BillReportStatusRejected = "rejected"
)

// ValidBillReportResolution reports whether s is a status an admin may close a
// report with. "open" is deliberately excluded — reports never reopen.
func ValidBillReportResolution(s string) bool {
	return s == BillReportStatusResolved || s == BillReportStatusRejected
}

// BillReport is a user complaint about a bill order ("I was debited but the
// airtime never arrived"), reviewed and closed out by an admin. One report per
// order — the unique index is the guard, not a pre-check (two taps race).
type BillReport struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrderID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"orderId"`
	UserID  uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`

	Reason string `gorm:"type:text;not null" json:"reason"`

	Status     string     `gorm:"size:16;not null;default:'open';index" json:"status"`
	ResolvedBy *uuid.UUID `gorm:"type:uuid" json:"resolvedBy,omitempty"`
	Resolution string     `gorm:"type:text" json:"resolution,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (BillReport) TableName() string { return "bill_reports" }

// App-side, not gen_random_uuid(), so the SQLite test rig works.
func (r *BillReport) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
