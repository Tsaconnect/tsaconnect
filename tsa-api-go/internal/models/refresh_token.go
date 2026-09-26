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
	// Web: issued to a web (cookie) session. It rotates only on the web, so it
	// can never be traded for an app token that carries staff powers.
	Web       bool `gorm:"not null;default:false"`
	CreatedAt time.Time
}
