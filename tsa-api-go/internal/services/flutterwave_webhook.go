package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ProviderFlutterwave is the canonical provider string stored in webhook_events.
const ProviderFlutterwave = "flutterwave"

// ---------------------------------------------------------------------------
// FlutterwaveWebhookVerifier — the v4 signature scheme
// ---------------------------------------------------------------------------

// FlutterwaveWebhookVerifier validates v4 webhooks: the `flutterwave-signature`
// header carries Base64(HMAC-SHA256(raw request body, secret hash)), with the
// secret configured in the Flutterwave dashboard. This is NOT the v3 scheme
// (v3's `verif-hash` is a plain equality check against the stored secret) —
// do not mix them.
type FlutterwaveWebhookVerifier struct {
	secret []byte
}

// NewFlutterwaveWebhookVerifier constructs a verifier with the dashboard's
// secret hash.
func NewFlutterwaveWebhookVerifier(secret string) *FlutterwaveWebhookVerifier {
	return &FlutterwaveWebhookVerifier{secret: []byte(secret)}
}

// Verify computes Base64(HMAC-SHA256(body, secret)) over the RAW bytes and
// constant-time compares against the header value.
func (v *FlutterwaveWebhookVerifier) Verify(body []byte, signatureB64 string) bool {
	if signatureB64 == "" {
		return false
	}
	mac := hmac.New(sha256.New, v.secret)
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signatureB64))
}

// ---------------------------------------------------------------------------
// Envelope + parsers — raw Flutterwave v4 JSON → the shared gateway DTOs
// ---------------------------------------------------------------------------

// fwWebhookEnvelope is the v4 event envelope: {webhook_id, timestamp, type,
// data}. webhook_id is the dedup key (events can be delivered more than once).
type fwWebhookEnvelope struct {
	WebhookID string          `json:"webhook_id"`
	Timestamp json.Number     `json:"timestamp"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
}

// FW event types this integration consumes. Unknown types are acknowledged
// and ignored (the v4 contract may still grow types; erroring would cause
// pointless provider retries).
const (
	fwEventChargeCompleted  = "charge.completed"
	fwEventTransferDisburse = "transfer.disburse"
	fwEventTransferReversal = "transfer.reversal"
)

// ClassifyFlutterwaveEvent returns the envelope's type + webhook id without
// fully parsing data. Handlers use it for dedup keys and routing.
func ClassifyFlutterwaveEvent(raw []byte) (eventType, webhookID string, err error) {
	var env fwWebhookEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", "", fmt.Errorf("parse flutterwave envelope: %w", err)
	}
	if env.WebhookID == "" {
		return "", "", errors.New("parse flutterwave envelope: missing webhook_id")
	}
	return strings.ToLower(strings.TrimSpace(env.Type)), env.WebhookID, nil
}

// fwChargeData is the charge.completed payload slice we consume. reference is
// OUR attribution key (the reference we set when creating the virtual account
// / charge); amount+currency must be validated server-side per the FW docs.
type fwChargeData struct {
	ID            string      `json:"id"`
	Reference     string      `json:"reference"`
	Amount        json.Number `json:"amount"`
	Currency      string      `json:"currency"`
	Status        string      `json:"status"`
	Customer      fwParty     `json:"customer"`
	PaymentMethod struct {
		Type string `json:"type"`
	} `json:"payment_method"`
}

type fwParty struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ParseFlutterwaveInbound converts a charge.completed event into the shared
// InboundPayment DTO. Returns ErrNonSettlingEvent for a well-formed charge
// event whose status is not a success (failed/pending charges settle nothing),
// mirroring the Nomba parser's contract so handler retry semantics hold.
func ParseFlutterwaveInbound(raw []byte) (*InboundPayment, error) {
	var env fwWebhookEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse flutterwave inbound: %w", err)
	}
	if env.WebhookID == "" {
		return nil, errors.New("parse flutterwave inbound: missing webhook_id")
	}
	if t := strings.ToLower(strings.TrimSpace(env.Type)); t != fwEventChargeCompleted {
		return nil, fmt.Errorf("parse flutterwave inbound: not a %s event (got %q)", fwEventChargeCompleted, env.Type)
	}
	var data fwChargeData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, fmt.Errorf("parse flutterwave inbound data: %w", err)
	}
	if data.Reference == "" {
		return nil, errors.New("parse flutterwave inbound: missing data.reference (attribution key)")
	}
	if !fwSucceeded(data.Status) {
		// Failed/pending charge — benign, nothing settles.
		return nil, fmt.Errorf("%w: charge status %q", ErrNonSettlingEvent, data.Status)
	}
	return &InboundPayment{
		EventID:           env.WebhookID,
		AccountRef:        data.Reference,
		SenderAccountName: data.Customer.Name,
		AmountNGN:         formatNaira(data.Amount.String()),
		Currency:          strings.ToUpper(strings.TrimSpace(data.Currency)),
		Status:            "settled",
		ProviderChargeID:  data.ID,
	}, nil
}

// fwTransferData is the transfer.disburse / transfer.reversal payload slice.
type fwTransferData struct {
	ID        json.Number `json:"id"`
	Reference string      `json:"reference"`
	Status    string      `json:"status"`
	Amount    json.Number `json:"amount"`
	Currency  string      `json:"currency"`
}

// ParseFlutterwavePayout converts transfer.disburse / transfer.reversal
// events into the shared PayoutStatus DTO. Status mapping:
//
//	disburse + SUCCESSFUL → "success"
//	disburse + FAILED     → "failed"
//	disburse + other      → "pending"  (NEW/PENDING interim states)
//	reversal (any status) → "reversed" (operator attention; funds came back
//	                        AFTER a success — never auto-retry on it)
func ParseFlutterwavePayout(raw []byte) (*PayoutStatus, error) {
	var env fwWebhookEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse flutterwave payout: %w", err)
	}
	if env.WebhookID == "" {
		return nil, errors.New("parse flutterwave payout: missing webhook_id")
	}
	t := strings.ToLower(strings.TrimSpace(env.Type))
	if t != fwEventTransferDisburse && t != fwEventTransferReversal {
		return nil, fmt.Errorf("parse flutterwave payout: unexpected event type %q", env.Type)
	}
	var data fwTransferData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, fmt.Errorf("parse flutterwave payout data: %w", err)
	}

	var status string
	switch {
	case t == fwEventTransferReversal:
		status = "reversed"
	case fwSucceeded(data.Status):
		status = "success"
	case strings.EqualFold(data.Status, "FAILED"):
		status = "failed"
	default:
		status = "pending"
	}

	return &PayoutStatus{
		EventID:       env.WebhookID,
		PayoutID:      data.ID.String(),
		MerchantTxRef: data.Reference,
		Status:        status,
	}, nil
}

// fwSucceeded folds the success spellings seen across v4 resources
// (charges use lowercase "succeeded", transfers use "SUCCESSFUL").
func fwSucceeded(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "successful", "success":
		return true
	}
	return false
}
