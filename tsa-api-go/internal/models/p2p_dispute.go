package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PDispute is the off-chain audit row for a disputed trade. Exactly one
// row per trade (enforced by unique index on TradeID for non-rejected rows).
// The on-chain dispute state lives in P2PEscrow.Trade.status; this record
// captures the WHY (raised reason, raised role) and the resolution decision
// the admin made (refundedToBuyer, free-form note).
//
// Lifecycle: open → (resolved | rejected). Rejected means the admin found
// the dispute baseless and chose not to override — the trade returns to its
// pre-dispute state via the contract's adminResolve(false) path.
type P2PDispute struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TradeID          uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex" json:"tradeId"`
	RaisedBy         uuid.UUID  `gorm:"type:uuid;not null" json:"raisedBy"`
	RaisedRole       string     `gorm:"size:8;not null" json:"raisedRole"`
	Reason           string     `gorm:"type:text;not null" json:"reason"`
	Status           string     `gorm:"size:16;not null;default:'open';index" json:"status"`
	ResolvedBy       *uuid.UUID `gorm:"type:uuid" json:"resolvedBy,omitempty"`
	Resolution       string     `gorm:"type:text" json:"resolution,omitempty"`
	RefundedToBuyer  *bool      `json:"refundedToBuyer,omitempty"`
	ResolveTxHash    string     `gorm:"size:66" json:"resolveTxHash,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	ResolvedAt       *time.Time `json:"resolvedAt,omitempty"`
}

func (P2PDispute) TableName() string {
	return "p2p_disputes"
}

const (
	P2PDisputeRoleBuyer  = "buyer"
	P2PDisputeRoleSeller = "seller"

	P2PDisputeStatusOpen     = "open"
	P2PDisputeStatusResolved = "resolved"
	P2PDisputeStatusRejected = "rejected"
)
