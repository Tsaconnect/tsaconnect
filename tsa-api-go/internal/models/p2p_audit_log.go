package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// P2PAuditLog is an immutable append-only ledger of every consequential
// action taken on the P2P system: trade state transitions, admin
// resolutions, reconciler drift corrections, operator releases (Phase 2+),
// idempotency replays. Used for compliance, incident response, and proving
// "who did what when" to support operations.
//
// Schema choices:
//   - ActorID nullable: system-driven entries (reconciler, scheduler) have
//     no human actor.
//   - TradeID nullable: some entries are scoped to an ad or to nothing.
//   - Metadata is JSON: each Action has its own shape (e.g. drift correction
//     stores chainStatus + dbStatus; admin resolve stores refundedToBuyer +
//     note). Free-form to avoid premature column proliferation; queryable
//     via PostgreSQL JSONB operators.
//
// Indexed on CreatedAt for time-ordered reads and on (TradeID, CreatedAt) for
// "show me everything that happened to this trade".
type P2PAuditLog struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ActorID   *uuid.UUID     `gorm:"type:uuid;index" json:"actorId,omitempty"`
	TradeID   *uuid.UUID     `gorm:"type:uuid;index:idx_p2p_audit_trade_created,priority:1" json:"tradeId,omitempty"`
	AdID      *uuid.UUID     `gorm:"type:uuid;index" json:"adId,omitempty"`
	Action    string         `gorm:"size:64;not null;index" json:"action"`
	Metadata  datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	CreatedAt time.Time      `gorm:"index;index:idx_p2p_audit_trade_created,priority:2" json:"createdAt"`
}

func (P2PAuditLog) TableName() string {
	return "p2p_audit_logs"
}

// Action names — keep stable; analytics dashboards index on these strings.
const (
	P2PAuditAdCreated         = "ad.created"
	P2PAuditAdClosed          = "ad.closed"
	P2PAuditAdPaused          = "ad.paused"
	P2PAuditAdResumed         = "ad.resumed"
	P2PAuditTradeOpened       = "trade.opened"
	P2PAuditTradeMarkedPaid   = "trade.marked_paid"
	P2PAuditTradeReleasing    = "trade.releasing"
	P2PAuditTradeReleased     = "trade.released"
	P2PAuditTradeCancelled    = "trade.cancelled"
	P2PAuditTradeDisputed     = "trade.disputed"
	P2PAuditTradeResolved     = "trade.resolved"
	P2PAuditReconcilerDrift   = "reconciler.drift"
	P2PAuditOperatorBroadcast = "operator.broadcast"
	P2PAuditDepositPrepared   = "deposit.prepared"
	P2PAuditDepositConfirmed  = "deposit.confirmed"
	P2PAuditWithdrawPrepared  = "withdraw.prepared"
	P2PAuditWithdrawConfirmed = "withdraw.confirmed"
)
