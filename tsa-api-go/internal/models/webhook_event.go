package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// WebhookEvent is the inbound-webhook ledger. Every incoming webhook payload
// (Phase 2+: Flutterwave deposits/payouts) is persisted here BEFORE any
// business-logic side effects, and the unique (Provider, EventID) index
// dedupes provider retries.
//
// Lifecycle:
//   - status="pending": just received, HMAC-validated; awaiting processing
//   - status="processed": handler ran successfully; ProcessedAt set
//   - status="failed":    handler raised an error; Error filled; retried by
//                         the WebhookRetry worker until success or dead-letter
//
// Phase 1: model exists so the schema is in place when Phase 2 wires the
// Flutterwave handler. No table writer yet.
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
