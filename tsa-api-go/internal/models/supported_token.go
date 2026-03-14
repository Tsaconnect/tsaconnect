package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// SupportedToken represents a token available in the app.
// Stored in the database so tokens can be added/removed without redeploying.
type SupportedToken struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Symbol    string         `gorm:"uniqueIndex;not null" json:"symbol"`
	Name      string         `gorm:"not null" json:"name"`
	Decimals  int            `gorm:"not null;default:18" json:"decimals"`
	IconColor string         `gorm:"not null;default:'#888888'" json:"iconColor"`
	Chains    datatypes.JSON `gorm:"type:jsonb;not null" json:"chains"` // e.g. ["sonic","bsc"]
	IsActive  bool           `gorm:"not null;default:true" json:"isActive"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

func (SupportedToken) TableName() string {
	return "supported_tokens"
}
