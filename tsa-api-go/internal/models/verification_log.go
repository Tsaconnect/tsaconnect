package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// VerificationLog records verification-related actions on a user's account.
type VerificationLog struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID    uuid.UUID      `gorm:"type:uuid;index" json:"userId"`
	Action    string         `json:"action"`
	Status    string         `json:"status"`
	Notes     string         `json:"notes,omitempty"`
	Metadata  datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	AdminID   *uuid.UUID     `gorm:"type:uuid" json:"adminId,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (VerificationLog) TableName() string {
	return "verification_logs"
}

// Verification log action constants.
const (
	VerificationActionPersonaInitiated = "persona_initiated"
	VerificationActionPersonaPassed    = "persona_passed"
	VerificationActionPersonaFailed    = "persona_failed"
	VerificationActionAdminOverride    = "admin_override_approved"
	VerificationActionAdminRejection   = "admin_rejection"
)

// Verification log status constants.
const (
	VerificationLogStatusPending  = "pending"
	VerificationLogStatusInReview = "in_review"
	VerificationLogStatusVerified = "verified"
	VerificationLogStatusRejected = "rejected"
	VerificationLogStatusFailed   = "failed"
)
