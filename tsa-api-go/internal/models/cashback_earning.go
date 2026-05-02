package models

import (
	"time"

	"github.com/google/uuid"
)

// CashbackEarning records a single cashback credit event for a user.
// Cashback is paid on-chain in dollars (USDC/USDT) instantly when a
// transaction completes — this model mirrors those on-chain events
// so the app can display cashback history.
type CashbackEarning struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID       uuid.UUID `gorm:"type:uuid;not null;index:idx_cashback_user_created,priority:1" json:"userId"`
	SourceUserID uuid.UUID `gorm:"type:uuid;not null" json:"sourceUserId"`
	SourceType   string    `gorm:"not null" json:"sourceType"`
	SourceID     uuid.UUID `gorm:"type:uuid;not null" json:"sourceId"`
	AmountUSD    float64   `gorm:"not null" json:"amountUsd"`
	TxHash       string    `json:"txHash,omitempty"`
	CreatedAt    time.Time `gorm:"not null;index:idx_cashback_user_created,priority:2" json:"createdAt"`
}

// TableName overrides the default table name.
func (CashbackEarning) TableName() string {
	return "cashback_earnings"
}
