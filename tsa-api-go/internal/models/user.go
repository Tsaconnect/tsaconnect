package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
)

// VerifiableImage represents an uploaded document image with verification status.
type VerifiableImage struct {
	URL      string `json:"url,omitempty"`
	PublicID string `json:"publicId,omitempty"`
	Verified bool   `json:"verified"`
}

// DriversLicense holds driver's license front and back images.
type DriversLicense struct {
	Front VerifiableImage `json:"front,omitempty"`
	Back  VerifiableImage `json:"back,omitempty"`
}

// NIN holds National Identification Number front and back images.
type NIN struct {
	Front VerifiableImage `json:"front,omitempty"`
	Back  VerifiableImage `json:"back,omitempty"`
}

// Passport holds the passport photo image.
type Passport struct {
	Photo VerifiableImage `json:"photo,omitempty"`
}

// PVC holds the Permanent Voter's Card image.
type PVC struct {
	Card VerifiableImage `json:"card,omitempty"`
}

// BVN holds Bank Verification Number details.
type BVN struct {
	Number     string     `json:"number,omitempty"`
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
}

// Documents holds all user verification documents.
type Documents struct {
	DriversLicense DriversLicense `json:"driversLicense,omitempty"`
	NIN            NIN            `json:"nin,omitempty"`
	Passport       Passport       `json:"passport,omitempty"`
	PVC            PVC            `json:"pvc,omitempty"`
	BVN            BVN            `json:"bvn,omitempty"`
}

// FaceImage represents a facial verification image with optional embeddings.
type FaceImage struct {
	URL        string    `json:"url,omitempty"`
	PublicID   string    `json:"publicId,omitempty"`
	Embeddings []float64 `json:"embeddings,omitempty"`
}

// FacialVerification holds facial verification data.
type FacialVerification struct {
	FaceFront         FaceImage  `json:"faceFront,omitempty"`
	FaceLeft          FaceImage  `json:"faceLeft,omitempty"`
	FaceRight         FaceImage  `json:"faceRight,omitempty"`
	FaceUp            FaceImage  `json:"faceUp,omitempty"`
	FaceDown          FaceImage  `json:"faceDown,omitempty"`
	VerificationScore float64    `json:"verificationScore,omitempty"`
	Verified          bool       `json:"verified"`
	VerifiedAt        *time.Time `json:"verifiedAt,omitempty"`
}

// ProfilePhoto represents a user's profile photo.
type ProfilePhoto struct {
	URL      string `json:"url,omitempty"`
	PublicID string `json:"publicId,omitempty"`
}

// User represents a user record in PostgreSQL.
type User struct {
	ID                         uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name                       string         `gorm:"not null" json:"name"`
	Username                   string         `gorm:"uniqueIndex;not null" json:"username"`
	Email                      string         `gorm:"uniqueIndex;not null" json:"email"`
	Password                   string         `gorm:"not null" json:"-"`
	Role                       string         `gorm:"default:'user'" json:"role"`
	PhoneNumber                string         `gorm:"uniqueIndex" json:"phoneNumber"`
	Country                    string         `json:"country"`
	State                      string         `json:"state,omitempty"`
	City                       string         `json:"city,omitempty"`
	Address                    string         `json:"address"`
	ProfilePhoto               datatypes.JSON `gorm:"type:jsonb" json:"profilePhoto,omitempty"`
	ReferralCode               string         `json:"referralCode,omitempty"`
	ReferredBy                 *uuid.UUID     `gorm:"type:uuid" json:"referredBy,omitempty"`
	Documents                  datatypes.JSON `gorm:"type:jsonb" json:"documents,omitempty"`
	FacialVerification         datatypes.JSON `gorm:"type:jsonb" json:"facialVerification,omitempty"`
	VerificationStatus         string         `gorm:"default:'pending'" json:"verificationStatus"`
	VerificationNotes          string         `json:"verificationNotes,omitempty"`
	AccountStatus              string         `gorm:"default:'active'" json:"accountStatus"`
	LastLogin                  *time.Time     `json:"lastLogin,omitempty"`
	LoginAttempts              int            `gorm:"default:0" json:"loginAttempts"`
	LockUntil                  *time.Time     `json:"lockUntil,omitempty"`
	WalletAddress              string         `json:"walletAddress,omitempty"`
	SeedPhraseBackedUp         bool           `gorm:"default:false" json:"seedPhraseBackedUp"`
	MuteNotifications          bool           `gorm:"default:false" json:"muteNotifications"`
	MuteEmail                  bool           `gorm:"default:false" json:"muteEmail"`
	DeletedAt                  *time.Time     `json:"deletedAt,omitempty"`
	SubmittedForVerificationAt *time.Time     `json:"submittedForVerificationAt,omitempty"`
	CreatedAt                  time.Time      `json:"createdAt"`
	UpdatedAt                  time.Time      `json:"updatedAt"`
}

// TableName overrides the default table name.
func (User) TableName() string {
	return "users"
}

// Constants for user roles.
const (
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleSuperAdmin = "super_admin"
	RoleMerchant   = "merchant"
	RoleSupport    = "support"
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

// GetDocuments deserializes the Documents JSONB field.
func (u *User) GetDocuments() Documents {
	var docs Documents
	if u.Documents != nil {
		_ = json.Unmarshal(u.Documents, &docs)
	}
	return docs
}

// SetDocuments serializes the Documents struct into the JSONB field.
func (u *User) SetDocuments(docs Documents) {
	data, _ := json.Marshal(docs)
	u.Documents = data
}

// GetFacialVerification deserializes the FacialVerification JSONB field.
func (u *User) GetFacialVerification() FacialVerification {
	var fv FacialVerification
	if u.FacialVerification != nil {
		_ = json.Unmarshal(u.FacialVerification, &fv)
	}
	return fv
}

// SetFacialVerification serializes the FacialVerification struct into the JSONB field.
func (u *User) SetFacialVerification(fv FacialVerification) {
	data, _ := json.Marshal(fv)
	u.FacialVerification = data
}

// GetProfilePhoto deserializes the ProfilePhoto JSONB field.
func (u *User) GetProfilePhoto() *ProfilePhoto {
	if u.ProfilePhoto == nil {
		return nil
	}
	var pp ProfilePhoto
	if err := json.Unmarshal(u.ProfilePhoto, &pp); err != nil {
		return nil
	}
	if pp.URL == "" && pp.PublicID == "" {
		return nil
	}
	return &pp
}

// SetProfilePhoto serializes the ProfilePhoto struct into the JSONB field.
func (u *User) SetProfilePhoto(pp *ProfilePhoto) {
	if pp == nil {
		u.ProfilePhoto = nil
		return
	}
	data, _ := json.Marshal(pp)
	u.ProfilePhoto = data
}
