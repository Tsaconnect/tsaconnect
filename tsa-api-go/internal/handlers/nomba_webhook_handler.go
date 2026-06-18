package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// webhookDispatcher is the narrow PM-automation slice the webhook handler needs.
// The real *services.PMAutomationService satisfies it; tests inject a fake so a
// full service graph isn't required to test the wire/dedup/classification logic.
//
// Dispatch owns classify→parse→dispatch so the handler and the PM recovery job
// (RetryFailedWebhooks) drive the exact same routing. It returns
// services.ErrNonSettlingEvent for a recognized-but-non-settling payment_* event
// so the handler can mark it `processed` (intentionally no action) rather than
// `failed`.
type webhookDispatcher interface {
	Dispatch(ctx context.Context, body []byte) error
}

// nombaVerifier validates a Nomba webhook from the raw body, the base64
// signature header, and the timestamp header. The real
// *services.NombaWebhookVerifier satisfies it; tests can inject a fake.
type nombaVerifier interface {
	Verify(body []byte, signatureB64, timestamp string) bool
}

// NombaWebhookHandler receives Nomba webhooks: it verifies the signature using
// Nomba's colon-field HMAC-SHA256 scheme, persists+dedupes the event in
// webhook_events, then classifies the payload as inbound (buy) or payout (sell)
// and dispatches it to PM automation.
//
// Dispatch is synchronous; the PM reconciler/retry path is the safety net for a
// dispatch error, so a failed dispatch is recorded as `failed` and STILL returns
// 200 (Nomba shouldn't hammer-retry — we own the retry).
type NombaWebhookHandler struct {
	verifier nombaVerifier
	store    *services.NombaWebhookStore
	auto     webhookDispatcher
}

// NewNombaWebhookHandler constructs the handler. auto is typed as the narrow
// webhookDispatcher so tests can inject a fake; the real
// *services.PMAutomationService satisfies it. v is the Nomba signature verifier
// (the real *services.NombaWebhookVerifier).
func NewNombaWebhookHandler(v nombaVerifier, store *services.NombaWebhookStore, auto webhookDispatcher) *NombaWebhookHandler {
	return &NombaWebhookHandler{verifier: v, store: store, auto: auto}
}

// webhookEnvelope is the minimal top-level shape we read to dedupe + classify.
// Nomba's real per-event id is `requestId` (NOT a top-level `event_id`), so the
// dedup key is keyed on requestId. Classification reads `event_type` — the real
// Nomba discriminator (payment_* vs payout_*); there is no top-level `event`
// field or `payout_id` in production payloads.
type webhookEnvelope struct {
	EventID   string `json:"requestId"`
	EventType string `json:"event_type"`
}

// Handle is the POST /api/webhooks/nomba entry point. It is signature-verified,
// not auth-gated, so it must be mounted OUTSIDE the auth-required groups.
func (h *NombaWebhookHandler) Handle(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Failed to read body"})
		return
	}

	sig := c.GetHeader("nomba-signature")
	ts := c.GetHeader("nomba-timestamp")

	// Nomba's dashboard "Create webhook" performs an UNSIGNED reachability probe
	// (a POST carrying neither nomba-signature nor nomba-timestamp) and will only
	// save the URL if it returns 2xx. Real Nomba events ALWAYS carry both headers.
	// Treat a header-less request as that probe: acknowledge 200 and do nothing.
	// This is side-effect-free — we return before Record/Dispatch, so no event is
	// ever acted on without a valid signature. A request that DOES carry a
	// signature but fails verification still gets 401 (genuine tamper/mismatch;
	// Nomba retries those).
	if sig == "" && ts == "" {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "ok"})
		return
	}

	if !h.verifier.Verify(body, sig, ts) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
		return
	}

	var env webhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid JSON"})
		return
	}
	if env.EventID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Missing requestId"})
		return
	}

	ctx := c.Request.Context()
	ev, dup, err := h.store.Record(ctx, env.EventID, body, sig)
	if err != nil {
		log.Printf("[nomba webhook] record event=%s: %v", env.EventID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to record event"})
		return
	}
	if dup {
		// Already processed (or in-flight) — Nomba retry. Ack without re-dispatch.
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "already processed"})
		return
	}

	// Dispatch synchronously via the centralized PM-automation entry point.
	//   - nil                  → mark processed.
	//   - ErrNonSettlingEvent  → mark processed (recognized; intentionally no
	//                            action — NOT a failure).
	//   - any other error      → mark failed (the recovery job re-dispatches it).
	// We always 200 so Nomba doesn't hammer-retry — we own the retry.
	dispErr := h.auto.Dispatch(ctx, body)
	switch {
	case dispErr == nil:
		h.markProcessed(ctx, ev.EventID)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "processed"})
	case errors.Is(dispErr, services.ErrNonSettlingEvent):
		log.Printf("[nomba webhook] non-settling event=%s: %v (marking processed)", env.EventID, dispErr)
		h.markProcessed(ctx, ev.EventID)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "processed (no action)"})
	default:
		log.Printf("[nomba webhook] dispatch event=%s: %v", env.EventID, dispErr)
		h.markFailed(ctx, ev.EventID, dispErr)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "received (deferred)"})
	}
}

func (h *NombaWebhookHandler) markProcessed(ctx context.Context, eventID string) {
	now := time.Now()
	if err := h.store.DB().WithContext(ctx).
		Model(&webhookEventRow{}).
		Where("provider = ? AND event_id = ?", services.ProviderNomba, eventID).
		Updates(map[string]any{"status": "processed", "processed_at": now}).Error; err != nil {
		log.Printf("[nomba webhook] mark processed event=%s: %v", eventID, err)
	}
}

func (h *NombaWebhookHandler) markFailed(ctx context.Context, eventID string, cause error) {
	if err := h.store.DB().WithContext(ctx).
		Model(&webhookEventRow{}).
		Where("provider = ? AND event_id = ?", services.ProviderNomba, eventID).
		Updates(map[string]any{"status": "failed", "error": cause.Error()}).Error; err != nil {
		log.Printf("[nomba webhook] mark failed event=%s: %v", eventID, err)
	}
}

// webhookEventRow is a thin local type used only to target the webhook_events
// table for status updates (avoids importing the full model just for a table
// name). TableName binds it to the right table.
type webhookEventRow struct{}

func (webhookEventRow) TableName() string { return "webhook_events" }
