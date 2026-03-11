package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// VerificationLog records verification-related actions on a user's account.
type VerificationLog struct {
	ID        primitive.ObjectID  `bson:"_id,omitempty" json:"id,omitempty"`
	UserID    primitive.ObjectID  `bson:"userId" json:"userId"`
	Action    string              `bson:"action" json:"action"`
	Status    string              `bson:"status" json:"status"`
	Notes     string              `bson:"notes,omitempty" json:"notes,omitempty"`
	Metadata  interface{}         `bson:"metadata,omitempty" json:"metadata,omitempty"`
	AdminID   *primitive.ObjectID `bson:"adminId,omitempty" json:"adminId,omitempty"`
	CreatedAt time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// Verification log action constants.
const (
	VerificationActionSubmitted            = "submitted"
	VerificationActionBVNVerification      = "bvn_verification"
	VerificationActionDocumentVerification = "document_verification"
	VerificationActionFacialVerification   = "facial_verification"
	VerificationActionAdminApproval        = "admin_approval"
	VerificationActionAdminRejection       = "admin_rejection"
	VerificationActionInfoRequested        = "info_requested"
	VerificationActionResubmitted          = "resubmitted"
)

// Verification log status constants.
const (
	VerificationLogStatusPending  = "pending"
	VerificationLogStatusInReview = "in_review"
	VerificationLogStatusVerified = "verified"
	VerificationLogStatusRejected = "rejected"
	VerificationLogStatusFailed   = "failed"
)
