package models

import (
	"time"

	"github.com/google/uuid"
)

// EmailVerification stores OTP codes for email verification.
type EmailVerification struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	Email     string    `gorm:"not null" json:"email"`
	Code      string    `gorm:"not null" json:"-"`
	ExpiresAt time.Time `gorm:"not null" json:"expiresAt"`
	Attempts  int       `gorm:"default:0" json:"attempts"`
	Verified  bool      `gorm:"default:false" json:"verified"`
	CreatedAt time.Time `json:"createdAt"`
}

func (EmailVerification) TableName() string {
	return "email_verifications"
}
