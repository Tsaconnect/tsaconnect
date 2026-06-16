package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// WebhookEvent is the inbound-webhook ledger. Every incoming webhook payload
// (Nomba PM inbound/payout events; Phase 2+: other providers) is persisted here
// BEFORE any business-logic side effects, and the unique (Provider, EventID)
// index dedupes provider retries.
//
// Lifecycle:
//   - status="pending":   just received, HMAC-validated; dispatch in progress
//   - status="processed": handler ran successfully; ProcessedAt set
//   - status="failed":    dispatch returned an infrastructure error; Error filled
//
// Recovery for a "failed" (or a "pending" orphaned by a crash mid-dispatch) is
// NOT a dedicated webhook-retry worker (none exists yet). It is the downstream
// safety net that already owns trade drift: a failed PM *inbound* leaves the
// trade in pending_payment, surfaced by the PM manual-release queue
// (PMAutomationService.ListBuyManualQueue); a failed PM *payout* leaves the sell
// trade for the timeout watchdog to cancel+refund. Returning 200 on a failed
// dispatch is deliberate — it stops the provider hammer-retrying while those
// nets converge. TODO(pm-phase2): add a periodic sweeper over
// webhook_events WHERE status IN ('failed','pending') AND received_at < now-N
// to re-dispatch automatically instead of relying on the manual queue/watchdog.
type WebhookEvent struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Provider    string         `gorm:"size:32;not null;uniqueIndex:idx_webhook_provider_event" json:"provider"`
	EventID     string         `gorm:"size:128;not null;uniqueIndex:idx_webhook_provider_event" json:"eventId"`
	Payload     datatypes.JSON `gorm:"type:jsonb;not null" json:"payload"`
	Signature   string         `gorm:"size:512;not null" json:"-"`
	Status      string         `gorm:"size:16;not null;index;default:'pending'" json:"status"`
	Error       string         `gorm:"type:text" json:"error,omitempty"`
	ReceivedAt  time.Time      `gorm:"index;not null" json:"receivedAt"`
	ProcessedAt *time.Time     `gorm:"index" json:"processedAt,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

func (WebhookEvent) TableName() string {
	return "webhook_events"
}
