package models

import (
	"time"

	"github.com/google/uuid"
)

// Account-deletion request lifecycle.
//
//	pending   — user asked to delete; the account is ALREADY soft-deleted and
//	            unusable. Admin has not acted yet.
//	completed — admin confirmed the data purge/anonymisation was carried out.
//	restored  — admin reinstated the account (user changed their mind, or the
//	            request was fraudulent). The user row goes back to active.
const (
	AccountDeletionStatusPending   = "pending"
	AccountDeletionStatusCompleted = "completed"
	AccountDeletionStatusRestored  = "restored"
)

// AccountDeletionGraceDays is how long a soft-deleted account is retained
// before it is eligible for permanent erasure. The window gives the user a way
// back (via support) and gives us time to satisfy the record-keeping duties
// that outlive the account — AML/KYC records and completed-transaction history
// (see the retention section of the Privacy Policy).
const AccountDeletionGraceDays = 30

// AccountDeletionRequest is the audit trail for a user-initiated account
// deletion. The user's account is soft-deleted the moment the request is
// created (Apple guideline 5.1.1(v) requires deletion to actually happen, not
// to be queued behind a human); this row is what admins review to carry out the
// permanent erasure once the grace window closes.
type AccountDeletionRequest struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;index;not null" json:"userId"`

	// Reason is the user's free-text explanation. Optional — never gate the
	// deletion on it.
	Reason string `gorm:"type:text" json:"reason,omitempty"`

	Status string `gorm:"not null;default:'pending';index" json:"status"`

	// PurgeAfter is when the account becomes eligible for permanent erasure
	// (requested_at + AccountDeletionGraceDays). Advisory: the purge itself is
	// an admin action, not a job, so nothing deletes data behind your back.
	PurgeAfter time.Time `gorm:"index" json:"purgeAfter"`

	AdminNote  string     `json:"adminNote,omitempty"`
	ReviewedBy *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty"`

	User     User  `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Reviewer *User `gorm:"foreignKey:ReviewedBy" json:"reviewer,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (AccountDeletionRequest) TableName() string {
	return "account_deletion_requests"
}
