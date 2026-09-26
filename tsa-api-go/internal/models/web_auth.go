package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WebSigninChallenge is a web sign-in waiting on the code emailed to the user
// (a browser the account has not trusted yet). One per user.
type WebSigninChallenge struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex"`
	CodeHash  string    `gorm:"size:64;not null"`
	Attempts  int       `gorm:"not null;default:0"`
	ExpiresAt time.Time `gorm:"not null"`
	CreatedAt time.Time
}

func (c *WebSigninChallenge) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// WebTrustedBrowser is a browser the user chose to remember after entering a
// sign-in code. The cookie holds a random token; only its hash is stored.
type WebTrustedBrowser struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index"`
	TokenHash  string    `gorm:"size:64;not null;uniqueIndex"`
	ExpiresAt  time.Time `gorm:"not null"`
	CreatedAt  time.Time
	LastSeenAt time.Time
}

func (b *WebTrustedBrowser) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}
