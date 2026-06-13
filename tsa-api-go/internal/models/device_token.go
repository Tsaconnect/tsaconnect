package models

import (
	"time"

	"github.com/google/uuid"
)

// DeviceToken is a registered Expo push token for one of a user's devices.
// Unique on Token: re-registering the same token (e.g. after a reinstall or a
// different login) re-points it at the current user via upsert. Pruned when
// Expo reports DeviceNotRegistered.
type DeviceToken struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	Token     string    `gorm:"size:256;not null;uniqueIndex" json:"token"` // ExponentPushToken[...]
	Platform  string    `gorm:"size:16" json:"platform"`                    // ios | android
	LastSeen  time.Time `json:"lastSeen"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
