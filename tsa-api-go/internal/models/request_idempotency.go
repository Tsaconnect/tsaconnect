package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// RequestIdempotency stores the result of an idempotency-keyed write request
// so that retries from the client (network failure, double-tap, etc.) replay
// the original response rather than executing the action twice.
//
// Keyed on (Key) — the client-supplied Idempotency-Key header. Bound to a
// UserID so two different users sharing the same key get distinct rows.
// RequestHash is the sha256 of (method + path + body); a stored row whose
// hash mismatches the incoming request body indicates a key reuse with a
// different payload and the middleware returns 409 Conflict.
//
// Rows older than 24h are eligible for cleanup; a future periodic job
// (TimeoutWatchdog-style) can prune them. Phase 1: no cleaner yet, table grows
// unbounded but each row is small (<2KB) and not on the hot path.
type RequestIdempotency struct {
	Key            string         `gorm:"primaryKey;size:200" json:"key"`
	UserID         uuid.UUID      `gorm:"type:uuid;index;not null" json:"userId"`
	RequestHash    string         `gorm:"size:64;not null" json:"-"`
	ResponseStatus int            `gorm:"not null" json:"responseStatus"`
	// ResponseBody is the raw handler response. Stored as JSONB only when the
	// middleware confirms it's valid JSON; non-JSON responses are not cached
	// (the middleware logs and skips). The JSONB column ensures querying for
	// observability remains structured.
	ResponseBody datatypes.JSON `gorm:"type:jsonb" json:"responseBody"`
	// ResponseHeaders preserves response-affecting headers (Location, ETag,
	// Set-Cookie, etc.) so a replay returns the *full* original response, not
	// just status+body. Stored as JSONB map[string][]string.
	ResponseHeaders datatypes.JSON `gorm:"type:jsonb" json:"responseHeaders,omitempty"`
	CreatedAt       time.Time      `gorm:"index" json:"createdAt"`
}

func (RequestIdempotency) TableName() string {
	return "request_idempotencies"
}
