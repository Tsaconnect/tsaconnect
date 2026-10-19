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
	ID                 uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name               string         `gorm:"not null" json:"name"`
	Username           string         `gorm:"uniqueIndex;not null" json:"username"`
	Email              string         `gorm:"uniqueIndex;not null" json:"email"`
	Password           string         `gorm:"not null" json:"-"`
	Role               string         `gorm:"default:'user'" json:"role"`
	PhoneNumber        string         `json:"phoneNumber"`
	Country            string         `json:"country"`
	State              string         `json:"state,omitempty"`
	City               string         `json:"city,omitempty"`
	Address            string         `json:"address"`
	ProfilePhoto       datatypes.JSON `gorm:"type:jsonb" json:"profilePhoto,omitempty"`
	ReferralCode       string         `json:"referralCode,omitempty"`
	ReferredBy         *uuid.UUID     `gorm:"type:uuid" json:"referredBy,omitempty"`
	DiditSessionID     string         `json:"diditSessionId,omitempty"`
	VerificationStatus string         `gorm:"default:'pending'" json:"verificationStatus"`
	VerificationNotes  string         `json:"verificationNotes,omitempty"`
	// VerifiedLegalName is the name Didit read off the identity document. It is
	// the only name on this record that was checked against anything — `Name` is
	// free text typed at signup — which is why inbound bank transfers into the
	// central account are attributed against this field and never against `Name`.
	// Written only by the KYC paths; profile edits must not touch it.
	//
	// `json:"-"` is deliberate. The whole User struct rides along on payloads it
	// was never designed for (the `user` association on merchant and
	// account-deletion requests, every admin listing that selects a User), so a
	// tagged field leaks by default. The two surfaces that should show it — the
	// user's own KYC status and the admin verification queue — opt in by name.
	VerifiedLegalName string `json:"-"`
	// VerifiedLegalNameAt dates the capture so a name backfilled from an old
	// session can be told apart from one written live at approval, and so a row
	// predating a re-verification can be spotted and re-pulled.
	VerifiedLegalNameAt *time.Time `json:"-"`
	// DeclaredGivenNames and DeclaredSurname are what the user typed as the
	// name on their ID just before the Didit flow. They are a CLAIM, never a
	// verified name: Didit is asked to compare them with the document
	// (expected_details), and when the document cannot be read (the NIN slip)
	// an admin confirms them against the image before anything is stored in
	// VerifiedLegalName. Same `json:"-"` reasoning as VerifiedLegalName.
	DeclaredGivenNames string `json:"-"`
	DeclaredSurname    string `json:"-"`
	// AccountStatus gates every authenticated request: the auth middleware
	// re-reads the user per request and rejects anything but "active", so a
	// change here takes effect immediately. Indexed because public product
	// queries filter listings by their seller's status.
	AccountStatus string `gorm:"default:'active';index" json:"accountStatus"`
	// Admin moderation of the account itself, mirroring the product fields.
	SuspensionReason string     `json:"suspensionReason,omitempty"`
	SuspendedAt      *time.Time `json:"suspendedAt,omitempty"`
	SuspendedBy      *uuid.UUID `gorm:"type:uuid" json:"-"`
	// SuspendedFromStatus is the status to restore on reactivation, so lifting a
	// suspension cannot silently reactivate an account that was already inactive.
	SuspendedFromStatus string     `json:"-"`
	LastLogin           *time.Time `json:"lastLogin,omitempty"`
	LoginAttempts       int        `gorm:"default:0" json:"loginAttempts"`
	LockUntil           *time.Time `json:"lockUntil,omitempty"`
	WalletAddress       string     `json:"walletAddress,omitempty"`
	SeedPhraseBackedUp  bool       `gorm:"default:false" json:"seedPhraseBackedUp"`
	MuteNotifications   bool       `gorm:"default:false" json:"muteNotifications"`
	MuteEmail           bool       `gorm:"default:false" json:"muteEmail"`
	TPBalance           float64    `gorm:"default:0" json:"tpBalance"`
	CashbackBalance     float64    `gorm:"default:0" json:"cashbackBalance"`

	// LastSettledGlobalIndex is the user's checkpoint into the global-pool
	// cumulative index (models.GlobalTPPool.PerUserIndexScaled, same scaled-int
	// units). Owed global TP = (currentIndex − this) / GlobalTPIndexScale,
	// credited lazily by FeeDistributionService.SettleUserGlobalShare.
	//
	// NULLABLE on purpose — NEW-USER FAIRNESS: a nil checkpoint means "never
	// settled". On first settlement it initializes to the CURRENT index (the
	// user is owed 0 for all epochs before they existed; they only share epochs
	// AFTER they join). A non-nil 0 would wrongly grant the user every epoch's
	// share since the index began.
	LastSettledGlobalIndex *int64 `gorm:"default:null" json:"lastSettledGlobalIndex,omitempty"`

	EmailVerified bool       `gorm:"default:false" json:"emailVerified"`
	DeletedAt     *time.Time `json:"deletedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
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

// SuspendableAccountStatuses are the statuses an account may be restored to when
// a suspension is lifted. "deleted" is absent on purpose: reactivating a deleted
// account is account-restoration, which has its own flow.
var SuspendableAccountStatuses = map[string]bool{
	AccountStatusActive:   true,
	AccountStatusInactive: true,
}

// IsActive reports whether the account may authenticate and whether its listings
// are shown to buyers.
func (u *User) IsActive() bool {
	return u.AccountStatus == AccountStatusActive
}
