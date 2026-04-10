package models

import (
	"time"

	"github.com/google/uuid"
)

type RefreshToken struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index"`
	TokenHash  string    `gorm:"type:varchar(64);uniqueIndex;not null"`
	ExpiresAt  time.Time `gorm:"not null"`
	Revoked    bool      `gorm:"default:false"`
	DeviceInfo string    `gorm:"type:varchar(255)"`
	CreatedAt  time.Time
}
