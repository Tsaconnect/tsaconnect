package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"golang.org/x/crypto/bcrypt"
)

// VerifiableImage represents an uploaded document image with verification status.
type VerifiableImage struct {
	URL      string `bson:"url,omitempty" json:"url,omitempty"`
	PublicID string `bson:"publicId,omitempty" json:"publicId,omitempty"`
	Verified bool   `bson:"verified,omitempty" json:"verified"`
}

// DriversLicense holds driver's license front and back images.
type DriversLicense struct {
	Front VerifiableImage `bson:"front,omitempty" json:"front,omitempty"`
	Back  VerifiableImage `bson:"back,omitempty" json:"back,omitempty"`
}

// NIN holds National Identification Number front and back images.
type NIN struct {
	Front VerifiableImage `bson:"front,omitempty" json:"front,omitempty"`
	Back  VerifiableImage `bson:"back,omitempty" json:"back,omitempty"`
}

// Passport holds the passport photo image.
type Passport struct {
	Photo VerifiableImage `bson:"photo,omitempty" json:"photo,omitempty"`
}

// PVC holds the Permanent Voter's Card image.
type PVC struct {
	Card VerifiableImage `bson:"card,omitempty" json:"card,omitempty"`
}

// BVN holds Bank Verification Number details.
type BVN struct {
	Number     string     `bson:"number,omitempty" json:"number,omitempty"`
	Verified   bool       `bson:"verified,omitempty" json:"verified"`
	VerifiedAt *time.Time `bson:"verifiedAt,omitempty" json:"verifiedAt,omitempty"`
}

// Documents holds all user verification documents.
type Documents struct {
	DriversLicense DriversLicense `bson:"driversLicense,omitempty" json:"driversLicense,omitempty"`
	NIN            NIN            `bson:"nin,omitempty" json:"nin,omitempty"`
	Passport       Passport       `bson:"passport,omitempty" json:"passport,omitempty"`
	PVC            PVC            `bson:"pvc,omitempty" json:"pvc,omitempty"`
	BVN            BVN            `bson:"bvn,omitempty" json:"bvn,omitempty"`
}

// FaceImage represents a facial verification image with optional embeddings.
type FaceImage struct {
	URL        string    `bson:"url,omitempty" json:"url,omitempty"`
	PublicID   string    `bson:"publicId,omitempty" json:"publicId,omitempty"`
	Embeddings []float64 `bson:"embeddings,omitempty" json:"embeddings,omitempty"`
}

// FacialVerification holds facial verification data.
type FacialVerification struct {
	FaceFront         FaceImage  `bson:"faceFront,omitempty" json:"faceFront,omitempty"`
	FaceLeft          FaceImage  `bson:"faceLeft,omitempty" json:"faceLeft,omitempty"`
	FaceRight         FaceImage  `bson:"faceRight,omitempty" json:"faceRight,omitempty"`
	FaceUp            FaceImage  `bson:"faceUp,omitempty" json:"faceUp,omitempty"`
	FaceDown          FaceImage  `bson:"faceDown,omitempty" json:"faceDown,omitempty"`
	VerificationScore float64    `bson:"verificationScore,omitempty" json:"verificationScore,omitempty"`
	Verified          bool       `bson:"verified,omitempty" json:"verified"`
	VerifiedAt        *time.Time `bson:"verifiedAt,omitempty" json:"verifiedAt,omitempty"`
}

// ProfilePhoto represents a user's profile photo.
type ProfilePhoto struct {
	URL      string `bson:"url,omitempty" json:"url,omitempty"`
	PublicID string `bson:"publicId,omitempty" json:"publicId,omitempty"`
}

// User represents a user document in MongoDB.
type User struct {
	ID                         primitive.ObjectID  `bson:"_id,omitempty" json:"id,omitempty"`
	Name                       string              `bson:"name" json:"name"`
	Username                   string              `bson:"username" json:"username"`
	Email                      string              `bson:"email" json:"email"`
	Password                   string              `bson:"password" json:"-"`
	Role                       string              `bson:"role" json:"role"`
	PhoneNumber                string              `bson:"phoneNumber" json:"phoneNumber"`
	Country                    string              `bson:"country" json:"country"`
	State                      string              `bson:"state,omitempty" json:"state,omitempty"`
	City                       string              `bson:"city,omitempty" json:"city,omitempty"`
	Address                    string              `bson:"address" json:"address"`
	ProfilePhoto               *ProfilePhoto       `bson:"profilePhoto,omitempty" json:"profilePhoto,omitempty"`
	ReferralCode               string              `bson:"referralCode,omitempty" json:"referralCode,omitempty"`
	ReferredBy                 *primitive.ObjectID `bson:"referredBy,omitempty" json:"referredBy,omitempty"`
	Documents                  Documents           `bson:"documents,omitempty" json:"documents,omitempty"`
	FacialVerification         FacialVerification  `bson:"facialVerification,omitempty" json:"facialVerification,omitempty"`
	VerificationStatus         string              `bson:"verificationStatus" json:"verificationStatus"`
	VerificationNotes          string              `bson:"verificationNotes,omitempty" json:"verificationNotes,omitempty"`
	AccountStatus              string              `bson:"accountStatus" json:"accountStatus"`
	LastLogin                  *time.Time          `bson:"lastLogin,omitempty" json:"lastLogin,omitempty"`
	LoginAttempts              int                 `bson:"loginAttempts" json:"loginAttempts"`
	LockUntil                  *time.Time          `bson:"lockUntil,omitempty" json:"lockUntil,omitempty"`
	DeletedAt                  *time.Time          `bson:"deletedAt,omitempty" json:"deletedAt,omitempty"`
	SubmittedForVerificationAt *time.Time          `bson:"submittedForVerificationAt,omitempty" json:"submittedForVerificationAt,omitempty"`
	CreatedAt                  time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt                  time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// Constants for user roles.
const (
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleSuperAdmin = "super_admin"
	RoleMerchant   = "merchant"
)

// Constants for verification statuses.
const (
	VerificationStatusPending  = "pending"
	VerificationStatusInReview = "in_review"
	VerificationStatusVerified = "verified"
	VerificationStatusRejected = "rejected"
)

// Constants for account statuses.
const (
	AccountStatusActive    = "active"
	AccountStatusInactive  = "inactive"
	AccountStatusSuspended = "suspended"
	AccountStatusDeleted   = "deleted"
)

// ComparePassword compares a plaintext password against the user's hashed password.
func (u *User) ComparePassword(password string) error {
	return bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
}

// IsLocked returns true if the user account is currently locked.
func (u *User) IsLocked() bool {
	if u.LockUntil == nil {
		return false
	}
	return u.LockUntil.After(time.Now())
}

// IncrementLoginAttempts increments the login attempt counter.
// Returns the new count and the lock-until time (if locked).
func (u *User) IncrementLoginAttempts() (int, *time.Time) {
	u.LoginAttempts++
	const maxAttempts = 5
	if u.LoginAttempts >= maxAttempts {
		lockTime := time.Now().Add(2 * time.Hour)
		u.LockUntil = &lockTime
		return u.LoginAttempts, u.LockUntil
	}
	return u.LoginAttempts, nil
}

// HashPassword hashes the given plaintext password using bcrypt.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
