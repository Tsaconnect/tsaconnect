package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PMessage is one entry in a per-trade chat thread. Three sources:
//
//   - User-authored (SenderID set, Type=text|image): buyer and seller
//     coordinating during the trade. Phase 1 supports text and Cloudinary-hosted
//     image attachments.
//
//   - System events (SenderID nil, Type=system): emitted automatically when
//     the trade transitions states ("Buyer marked paid at 14:32", "Trade
//     released, 99.5 USDT sent to buyer"). Off-chain only — these mirror but
//     don't authoritatively reflect chain state.
//
//   - Bot messages (SenderID nil, Type=bot): Phase-4 rule-based assistant
//     (reminders, fraud warnings). Schema reserves the type now so the
//     migration doesn't need a column add later.
//
// Indexed on (TradeID, CreatedAt) for the dominant chat-history query.
type P2PMessage struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TradeID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_p2p_msg_trade_created,priority:1" json:"tradeId"`
	SenderID  *uuid.UUID `gorm:"type:uuid;index" json:"senderId,omitempty"`
	Type      string     `gorm:"size:16;not null" json:"type"`
	Body      string     `gorm:"type:text" json:"body,omitempty"`
	ImageURL  string     `gorm:"type:text" json:"imageUrl,omitempty"`
	CreatedAt time.Time  `gorm:"index:idx_p2p_msg_trade_created,priority:2" json:"createdAt"`
}

func (P2PMessage) TableName() string {
	return "p2p_messages"
}

const (
	P2PMessageTypeText   = "text"
	P2PMessageTypeImage  = "image"
	P2PMessageTypeSystem = "system"
	P2PMessageTypeBot    = "bot"
)
