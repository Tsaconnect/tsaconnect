package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
// NombaWebhookVerifier — the REAL Nomba signature scheme
// ---------------------------------------------------------------------------

// nombaSigEnvelope is the minimal parsed shape needed to reconstruct Nomba's
// colon-delimited signing string from a webhook body.
type nombaSigEnvelope struct {
	EventType string `json:"event_type"`
	RequestID string `json:"requestId"`
	Data      struct {
		Merchant struct {
			UserID   string `json:"userId"`
			WalletID string `json:"walletId"`
		} `json:"merchant"`
		Transaction struct {
			TransactionID string `json:"transactionId"`
			Type          string `json:"type"`
			Time          string `json:"time"`
			ResponseCode  string `json:"responseCode"`
		} `json:"transaction"`
	} `json:"data"`
}

// NombaWebhookVerifier validates Nomba webhooks using Nomba's documented scheme:
// base64( HMAC_SHA256( secret, signingString ) ), where signingString is the
// colon-joined concatenation of fields drawn from the PARSED body plus the
// nomba-timestamp header (the trailing element).
type NombaWebhookVerifier struct {
	secret []byte
}

// NewNombaWebhookVerifier constructs a verifier with the given webhook signature key.
func NewNombaWebhookVerifier(secret string) *NombaWebhookVerifier {
	return &NombaWebhookVerifier{secret: []byte(secret)}
}

// Verify reconstructs Nomba's colon-delimited signing string from the parsed
// body + the nomba-timestamp header, HMAC-SHA256 with the secret, base64-encodes,
// and constant-time compares against the nomba-signature header value.
//
// signingString =
//
//	"{event_type}:{requestId}:{userId}:{walletId}:{transactionId}:{type}:{time}:{responseCode}:{timestamp}"
func (v *NombaWebhookVerifier) Verify(body []byte, signatureB64, timestamp string) bool {
	if len(signatureB64) == 0 || timestamp == "" {
		return false
	}
	var env nombaSigEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	signing := strings.Join([]string{
		env.EventType,
		env.RequestID,
		env.Data.Merchant.UserID,
		env.Data.Merchant.WalletID,
		env.Data.Transaction.TransactionID,
		env.Data.Transaction.Type,
		env.Data.Transaction.Time,
		env.Data.Transaction.ResponseCode,
		timestamp,
	}, ":")
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(signing))
	expected := mac.Sum(nil)
	got, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, got)
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
// Parsers — raw Nomba JSON → DTOs
// ---------------------------------------------------------------------------

// nombaInboundRaw is the real wire shape of a Nomba payment_success webhook.
type nombaInboundRaw struct {
	EventType string `json:"event_type"`
	RequestID string `json:"requestId"`
	Data      struct {
		Merchant struct {
			WalletID string `json:"walletId"`
			UserID   string `json:"userId"`
		} `json:"merchant"`
		Transaction struct {
			TransactionID     string  `json:"transactionId"`
			Type              string  `json:"type"`
			TransactionAmount float64 `json:"transactionAmount"` // naira, not kobo
			Fee               float64 `json:"fee"`
			Time              string  `json:"time"`
			ResponseCode      string  `json:"responseCode"`
			Narration         string  `json:"narration"`
		} `json:"transaction"`
		Customer struct {
			SenderName    string `json:"senderName"`
			AccountNumber string `json:"accountNumber"`
			BankCode      string `json:"bankCode"`
			BankName      string `json:"bankName"`
		} `json:"customer"`
	} `json:"data"`
}

// nombaPayoutRaw is the real wire shape of a Nomba payout_success / payout_failed webhook.
type nombaPayoutRaw struct {
	EventType string `json:"event_type"`
	RequestID string `json:"requestId"`
	Data      struct {
		Merchant struct {
			WalletID string `json:"walletId"`
			UserID   string `json:"userId"`
		} `json:"merchant"`
		Transaction struct {
			TransactionID     string  `json:"transactionId"`
			Type              string  `json:"type"`
			TransactionAmount float64 `json:"transactionAmount"`
			Fee               float64 `json:"fee"`
			Time              string  `json:"time"`
			ResponseCode      string  `json:"responseCode"`
			MerchantTxRef     string  `json:"merchantTxRef"`
		} `json:"transaction"`
		Customer struct {
			SenderName    string `json:"senderName"`
			RecipientName string `json:"recipientName"`
			AccountNumber string `json:"accountNumber"`
			BankCode      string `json:"bankCode"`
			BankName      string `json:"bankName"`
		} `json:"customer"`
	} `json:"data"`
}

// formatNaira formats a float64 naira amount to a 2-decimal string (e.g. 1500 → "1500.00").
func formatNaira(amount float64) string {
	return strconv.FormatFloat(amount, 'f', 2, 64)
}

// ParseNombaInbound unmarshals a real Nomba payment_success webhook payload
// into an InboundPayment DTO. Returns an error if event_type is not
// "payment_success", requestId is empty, or data.merchant.walletId is empty.
func ParseNombaInbound(raw []byte) (*InboundPayment, error) {
	var r nombaInboundRaw
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse nomba inbound: %w", err)
	}
	if r.EventType != "payment_success" {
		return nil, fmt.Errorf("parse nomba inbound: not a payment_success event (got %q)", r.EventType)
	}
	if r.RequestID == "" {
		return nil, errors.New("parse nomba inbound: missing requestId")
	}
	if r.Data.Merchant.WalletID == "" {
		return nil, errors.New("parse nomba inbound: missing data.merchant.walletId")
	}
	return &InboundPayment{
		EventID:             r.RequestID,
		WalletID:            r.Data.Merchant.WalletID,
		DestinationAccount:  "", // not present in real Nomba payload; walletId is the PM key
		SenderAccountName:   r.Data.Customer.SenderName,
		SenderAccountNumber: r.Data.Customer.AccountNumber,
		AmountNGN:           formatNaira(r.Data.Transaction.TransactionAmount),
		Status:              "settled",
	}, nil
}

// ParseNombaPayout unmarshals a real Nomba payout_success / payout_failed webhook
// payload into a PayoutStatus DTO. Returns an error if requestId is empty.
// Status mapping: "payout_success" → "success", "payout_failed" → "failed", else "pending".
func ParseNombaPayout(raw []byte) (*PayoutStatus, error) {
	var r nombaPayoutRaw
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse nomba payout: %w", err)
	}
	if r.RequestID == "" {
		return nil, errors.New("parse nomba payout: missing requestId")
	}

	var status string
	switch r.EventType {
	case "payout_success":
		status = "success"
	case "payout_failed":
		status = "failed"
	default:
		status = "pending"
	}

	return &PayoutStatus{
		EventID:       r.RequestID,
		PayoutID:      r.Data.Transaction.TransactionID,
		MerchantTxRef: r.Data.Transaction.MerchantTxRef,
		Status:        status,
	}, nil
}
