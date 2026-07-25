package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ITAgreementAcceptance is an append-only audit record of a user accepting the
// Instant Transfer trading agreement. One row per acceptance (a user re-accepts
// when the agreement version bumps), so the table is the legal trail of who
// agreed to what version, when, and from where. A user is cleared to trade IT
// only when a row exists for them at the CURRENT version.
type ITAgreementAcceptance struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	// Unique per (user, version): one acceptance row per version, so concurrent
	// accepts collapse to a single audit row (the second hits this constraint
	// and AcceptITAgreement treats it as success). Re-consent on a version bump
	// is a DIFFERENT version → a new row, not a conflict.
	UserID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_it_agreement_user_version" json:"userId"`
	Version string    `gorm:"size:16;not null;uniqueIndex:idx_it_agreement_user_version" json:"version"`

	// Captured for the audit trail; best-effort (may be empty behind some proxies).
	IPAddress string `gorm:"size:64" json:"ipAddress,omitempty"`
	UserAgent string `gorm:"size:256" json:"userAgent,omitempty"`

	AcceptedAt time.Time `json:"acceptedAt"`
}

func (a *ITAgreementAcceptance) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	if a.AcceptedAt.IsZero() {
		a.AcceptedAt = time.Now()
	}
	return nil
}
