package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
)

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
	PersonaInquiryID               string         `json:"personaInquiryId,omitempty"`
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
	TPBalance                  float64    `gorm:"default:0" json:"tpBalance"`
	CashbackBalance            float64    `gorm:"default:0" json:"cashbackBalance"`
	EmailVerified              bool           `gorm:"default:false" json:"emailVerified"`
	DeletedAt                  *time.Time     `json:"deletedAt,omitempty"`
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

// MarshalJSON ensures every user response includes a flat `profilePicture`
// URL derived from the ProfilePhoto JSONB, so clients that read
// `user.profilePicture` as a plain string work alongside the structured
// `profilePhoto` object. Omits the field when no photo is set.
func (u User) MarshalJSON() ([]byte, error) {
	type userAlias User
	pic := ""
	if pp := u.GetProfilePhoto(); pp != nil {
		pic = pp.URL
	}
	return json.Marshal(&struct {
		userAlias
		ProfilePicture string `json:"profilePicture,omitempty"`
	}{
		userAlias:      userAlias(u),
		ProfilePicture: pic,
	})
}
