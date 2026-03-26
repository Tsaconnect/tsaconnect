package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type Notification struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID    uuid.UUID      `gorm:"type:uuid;index;not null" json:"userId"`
	Type      string         `gorm:"not null;index" json:"type"`
	Title     string         `gorm:"not null" json:"title"`
	Message   string         `gorm:"not null" json:"message"`
	Data      datatypes.JSON `gorm:"type:jsonb" json:"data"`
	Channel   string         `gorm:"not null;default:'in_app'" json:"channel"`
	IsRead    bool           `gorm:"default:false" json:"isRead"`
	ReadAt    *time.Time     `json:"readAt"`
	CreatedAt time.Time      `json:"createdAt"`
	DeletedAt *time.Time     `gorm:"index" json:"-"`
}
