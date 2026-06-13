package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PRating is a single 👍/👎 left by one party of a completed trade about the other.
// One per (trade, rater); allowed only after the trade is released; immutable.
type P2PRating struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TradeID   uuid.UUID `gorm:"type:uuid;not null;index" json:"tradeId"`
	RaterID   uuid.UUID `gorm:"type:uuid;not null;index" json:"raterId"`
	RateeID   uuid.UUID `gorm:"type:uuid;not null;index" json:"rateeId"`
	Value     int16     `gorm:"not null" json:"value"` // +1 or -1
	CreatedAt time.Time `json:"createdAt"`
}

func (P2PRating) TableName() string { return "p2p_ratings" }
