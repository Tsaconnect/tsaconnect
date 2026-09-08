package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// paycrest_webhook.go — inbound order events.
//
// Until this existed the poller was the ONLY thing advancing an order, which
// made a 20s tick load-bearing rather than a safety net. Both paths now
// converge on the same state machine, and neither is trusted on its own: an
// event tells us to look, and the order is re-read from the API before
// anything moves. A signed webhook is a hint, not an authority.

// ProviderPaycrest is the canonical provider string stored in webhook_events.
const ProviderPaycrest = "paycrest"

// PaycrestSignatureHeader carries the HMAC. Note this is HEX, lowercase —
// Flutterwave's equivalent is Base64, and mixing the two silently rejects every
// event.
const PaycrestSignatureHeader = "X-Paycrest-Signature"

// PaycrestWebhookVerifier validates the signature over the RAW request body.
type PaycrestWebhookVerifier struct{ secret []byte }

// NewPaycrestWebhookVerifier returns nil when no secret is configured, so the
// route can fail closed rather than accepting unsigned events.
func NewPaycrestWebhookVerifier(secret string) *PaycrestWebhookVerifier {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	return &PaycrestWebhookVerifier{secret: []byte(strings.TrimSpace(secret))}
}

// Verify computes HMAC-SHA256(body, secret) and constant-time compares the hex
// digest against the header.
//
// Constant-time on the DECODED bytes, not the strings: comparing hex text
// leaks through length and case, and hmac.Equal on raw digests is the only
// comparison that is actually constant-time here.
func (v *PaycrestWebhookVerifier) Verify(body []byte, signature string) bool {
	if v == nil {
		return false
	}
	sig := strings.ToLower(strings.TrimSpace(signature))
	if sig == "" {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, v.secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// Documented event types. Unknown types are acknowledged and ignored — the v2
// contract may still grow, and erroring would only earn pointless retries.
const (
	PaycrestEventPending   = "payment_order.pending"
	PaycrestEventDeposited = "payment_order.deposited"
	PaycrestEventValidated = "payment_order.validated"
	PaycrestEventSettled   = "payment_order.settled"
	PaycrestEventRefunded  = "payment_order.refunded"
	PaycrestEventExpired   = "payment_order.expired"
)

// paycrestWebhookEnvelope is the v2 delivery shape.
type paycrestWebhookEnvelope struct {
	Event     string          `json:"event"`
	Direction string          `json:"direction"` // "onramp" | "offramp"
	Version   string          `json:"webhookVersion"`
	Data      json.RawMessage `json:"data"`
}

// PaycrestWebhookEvent is what a handler needs to act.
type PaycrestWebhookEvent struct {
	Event     string
	Direction string
	// OrderID and Reference identify the order. Reference is OURS — the key we
	// set at creation — and is the one we look up by, because it is the only
	// identifier guaranteed to exist on our side before Paycrest replies.
	OrderID   string
	Reference string
	Status    string
	// DedupeKey is what webhook_events stores. Paycrest sends no delivery id,
	// so it is composed from the order and the event: redelivering the same
	// transition is a no-op, while a genuine later transition still lands.
	DedupeKey string
	Raw       json.RawMessage
}

// ErrPaycrestUnknownEvent marks a well-formed delivery we do not act on.
// Callers should acknowledge it, not retry.
var ErrPaycrestUnknownEvent = errors.New("paycrest: event type not consumed")

// ParsePaycrestWebhook validates the envelope and pulls out what identifies the
// order. It deliberately does NOT trust the payload's amounts or status for
// settlement — the caller re-reads the order from the API first.
func ParsePaycrestWebhook(raw []byte) (*PaycrestWebhookEvent, error) {
	var env paycrestWebhookEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse paycrest webhook: %w", err)
	}
	event := strings.ToLower(strings.TrimSpace(env.Event))
	if event == "" {
		return nil, errors.New("parse paycrest webhook: missing event")
	}

	var data struct {
		ID        string `json:"id"`
		Reference string `json:"reference"`
		Status    string `json:"status"`
	}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return nil, fmt.Errorf("parse paycrest webhook data: %w", err)
		}
	}
	if data.ID == "" && data.Reference == "" {
		// Nothing to correlate against; retrying cannot add an identifier.
		return nil, errors.New("parse paycrest webhook: no order id or reference")
	}

	switch event {
	case PaycrestEventPending, PaycrestEventDeposited, PaycrestEventValidated,
		PaycrestEventSettled, PaycrestEventRefunded, PaycrestEventExpired:
	default:
		return nil, fmt.Errorf("%w: %s", ErrPaycrestUnknownEvent, event)
	}

	id := data.ID
	if id == "" {
		id = data.Reference
	}
	return &PaycrestWebhookEvent{
		Event:     event,
		Direction: strings.ToLower(strings.TrimSpace(env.Direction)),
		OrderID:   data.ID,
		Reference: data.Reference,
		Status:    strings.ToLower(strings.TrimSpace(data.Status)),
		DedupeKey: id + ":" + event,
		Raw:       env.Data,
	}, nil
}
