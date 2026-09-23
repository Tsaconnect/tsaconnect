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
// Rows older than RequestIdempotencyRetention are deleted by
// jobs.RequestIdempotencyPruner. That matters beyond table size: a cached
// response is whatever the endpoint answered, which for some endpoints (a
// sell intent) includes the user's bank details.
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

// RequestIdempotencyRetention is how long a cached response can be replayed.
// Clients mint a fresh key per action and retry within seconds or minutes, so
// a day is far past any legitimate retry of the same key.
const RequestIdempotencyRetention = 24 * time.Hour

func (RequestIdempotency) TableName() string {
	return "request_idempotencies"
}
