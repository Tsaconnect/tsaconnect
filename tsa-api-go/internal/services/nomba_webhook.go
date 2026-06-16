package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProviderNomba is the canonical provider string stored in webhook_events.
const ProviderNomba = "nomba"

// ---------------------------------------------------------------------------
// WebhookVerifier — signature seam
// ---------------------------------------------------------------------------

// WebhookVerifier validates the signature on an incoming webhook body.
type WebhookVerifier interface {
	Verify(body []byte, signature string) bool
}

// HMACWebhookVerifier validates webhooks using HMAC-SHA256.
//
// TODO(nomba-facts): confirm Nomba's exact signature scheme + header encoding.
// The live Nomba webhook may use a different algorithm, encoding, or header
// name. Finalize against design §12 / Nomba documentation before production.
type HMACWebhookVerifier struct {
	secret []byte
}

// NewHMACWebhookVerifier constructs a verifier with the given shared secret.
func NewHMACWebhookVerifier(secret string) *HMACWebhookVerifier {
	return &HMACWebhookVerifier{secret: []byte(secret)}
}

// Verify returns true iff signature is the lowercase hex HMAC-SHA256 of body
// under the configured secret. Empty or non-hex signatures are rejected
// without a timing oracle (hmac.Equal is constant-time, but hex.DecodeString
// failure returns early — this is acceptable because an invalid hex string is
// trivially distinguishable without a timing side-channel).
func (h *HMACWebhookVerifier) Verify(body []byte, signature string) bool {
	if signature == "" {
		return false
	}
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(expected, sigBytes)
}

// ---------------------------------------------------------------------------
// NombaWebhookStore — ledger writer with (provider, event_id) dedup
// ---------------------------------------------------------------------------

// NombaWebhookStore persists incoming Nomba webhook events to webhook_events
// and deduplicates on the (provider, event_id) unique index.
type NombaWebhookStore struct {
	db *gorm.DB
}

// NewNombaWebhookStore constructs a store backed by the given GORM DB.
func NewNombaWebhookStore(db *gorm.DB) *NombaWebhookStore {
	return &NombaWebhookStore{db: db}
}

// DB returns the underlying GORM handle so callers (the webhook handler) can
// update an event row's status/error/processed_at after dispatch. Exposed
// deliberately — the store owns inserts/dedup; post-dispatch status is the
// caller's concern and shares the same connection.
func (s *NombaWebhookStore) DB() *gorm.DB { return s.db }

// Record persists the webhook event. It returns:
//   - (ev, false, nil) on first insert — ev is the newly created row.
//   - (existing, true, nil) on duplicate (provider+event_id already stored).
//   - (nil, false, err) on any other database error.
//
// Dedup is implemented via clause.OnConflict DoNothing against the unique index
// idx_webhook_provider_event. When RowsAffected == 0 after the insert the row
// was already present; the existing record is re-fetched and returned so callers
// always receive the canonical stored record.
func (s *NombaWebhookStore) Record(ctx context.Context, eventID string, body []byte, signature string) (*models.WebhookEvent, bool, error) {
	ev := &models.WebhookEvent{
		ID:         uuid.New(),
		Provider:   ProviderNomba,
		EventID:    eventID,
		Payload:    datatypes.JSON(body),
		Signature:  signature,
		Status:     "pending",
		ReceivedAt: time.Now(),
	}

	result := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "provider"}, {Name: "event_id"}},
			DoNothing: true,
		}).
		Create(ev)

	if result.Error != nil {
		return nil, false, fmt.Errorf("webhook record insert: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		// Duplicate — re-fetch the canonical existing record.
		var existing models.WebhookEvent
		if err := s.db.WithContext(ctx).
			Where("provider = ? AND event_id = ?", ProviderNomba, eventID).
			First(&existing).Error; err != nil {
			return nil, true, fmt.Errorf("webhook re-fetch existing: %w", err)
		}
		return &existing, true, nil
	}

	return ev, false, nil
}

// ---------------------------------------------------------------------------
// Parsers — raw Nomba JSON → existing DTOs
// ---------------------------------------------------------------------------

// nombaInboundRaw is the assumed wire shape of a Nomba inbound-payment webhook.
//
// TODO(nomba-facts): field mapping assumes a documented shape; finalize against
// the live Nomba payload (design §12). Fields here are best-effort until the
// live Nomba sandbox confirms the exact JSON structure and field names.
type nombaInboundRaw struct {
	EventID string `json:"event_id"`
	Data    struct {
		DestinationAccount  string `json:"destination_account"`
		SenderName          string `json:"sender_name"`
		SenderAccount       string `json:"sender_account"`
		Amount              string `json:"amount"`
		Status              string `json:"status"`
	} `json:"data"`
}

// nombaPayoutRaw is the assumed wire shape of a Nomba payout-status webhook.
//
// TODO(nomba-facts): field mapping assumes a documented shape; finalize against
// the live Nomba payload (design §12).
type nombaPayoutRaw struct {
	EventID string `json:"event_id"`
	Data    struct {
		PayoutID string `json:"payout_id"`
		Status   string `json:"status"`
	} `json:"data"`
}

// ParseNombaInbound unmarshals a raw Nomba inbound-payment webhook payload
// into an InboundPayment DTO. Returns an error if EventID is empty.
func ParseNombaInbound(raw []byte) (*InboundPayment, error) {
	var r nombaInboundRaw
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse nomba inbound: %w", err)
	}
	if r.EventID == "" {
		return nil, errors.New("parse nomba inbound: missing event_id")
	}
	return &InboundPayment{
		EventID:             r.EventID,
		DestinationAccount:  r.Data.DestinationAccount,
		SenderAccountName:   r.Data.SenderName,
		SenderAccountNumber: r.Data.SenderAccount,
		AmountNGN:           r.Data.Amount,
		Status:              r.Data.Status,
	}, nil
}

// ParseNombaPayout unmarshals a raw Nomba payout-status webhook payload into a
// PayoutStatus DTO. Returns an error if EventID is empty.
func ParseNombaPayout(raw []byte) (*PayoutStatus, error) {
	var r nombaPayoutRaw
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse nomba payout: %w", err)
	}
	if r.EventID == "" {
		return nil, errors.New("parse nomba payout: missing event_id")
	}
	return &PayoutStatus{
		EventID:  r.EventID,
		PayoutID: r.Data.PayoutID,
		Status:   r.Data.Status,
	}, nil
}
