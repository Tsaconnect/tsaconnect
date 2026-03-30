package models

import (
	"time"

	"github.com/google/uuid"
)

// TPEarning records a single TP credit event for a user.
type TPEarning struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID       uuid.UUID `gorm:"type:uuid;not null;index:idx_tp_user_created,priority:1" json:"userId"`
	SourceUserID uuid.UUID `gorm:"type:uuid;not null" json:"sourceUserId"`
	SourceType   string    `gorm:"not null" json:"sourceType"`
	SourceID     uuid.UUID `gorm:"type:uuid;not null" json:"sourceId"`
	Generation   int       `gorm:"not null" json:"generation"`
	FeeAmountUSD float64   `gorm:"not null" json:"feeAmountUsd"`
	Percentage   float64   `gorm:"not null" json:"percentage"`
	TPEarned     float64   `gorm:"not null" json:"tpEarned"`
	CreatedAt    time.Time `gorm:"not null;index:idx_tp_user_created,priority:2" json:"createdAt"`
}

// TableName overrides the default table name.
func (TPEarning) TableName() string {
	return "tp_earnings"
}
