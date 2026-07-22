package handlers

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fwVerifier validates a Flutterwave v4 webhook from the raw body and the
// `flutterwave-signature` header. The real
// *services.FlutterwaveWebhookVerifier satisfies it; tests inject a fake.
type fwVerifier interface {
	Verify(body []byte, signatureB64 string) bool
}

// fwDispatcher is the narrow PM-automation slice this handler needs. The real
// *services.PMAutomationService satisfies it via DispatchFlutterwave; tests
// inject a fake.
type fwDispatcher interface {
	DispatchFlutterwave(ctx context.Context, body []byte) error
}

// FlutterwaveWebhookHandler receives Flutterwave v4 webhooks: verifies the
// HMAC signature over the raw body, persists+dedupes on webhook_id, then
// dispatches into PM automation. Mirrors the Nomba handler's semantics:
// dispatch failures are recorded `failed` and STILL return 200 — the retry
// job owns re-drives, not Flutterwave's 3×/30min retry hammer.
type FlutterwaveWebhookHandler struct {
	verifier fwVerifier
	store    *services.NombaWebhookStore // provider-parameterized (flutterwave)
	auto     fwDispatcher
}

// NewFlutterwaveWebhookHandler constructs the handler. The store MUST be
// built with services.NewWebhookStoreForProvider(db, services.ProviderFlutterwave).
func NewFlutterwaveWebhookHandler(v fwVerifier, store *services.NombaWebhookStore, auto fwDispatcher) *FlutterwaveWebhookHandler {
	return &FlutterwaveWebhookHandler{verifier: v, store: store, auto: auto}
}

// Handle is the POST /api/webhooks/flutterwave entry point. Signature-verified,
// not auth-gated — mount it OUTSIDE the auth-required groups.
func (h *FlutterwaveWebhookHandler) Handle(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Failed to read body"})
		return
	}

	sig := c.GetHeader("flutterwave-signature")

	// Dashboard URL-verification probes arrive unsigned; real events always
	// carry the signature header. Acknowledge probes side-effect-free (we
	// return before Record/Dispatch). A present-but-wrong signature is a
	// genuine mismatch and gets 401.
	if sig == "" {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "ok"})
		return
	}
	if !h.verifier.Verify(body, sig) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
		return
	}

	eventType, webhookID, err := services.ClassifyFlutterwaveEvent(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid envelope"})
		return
	}

	ctx := c.Request.Context()
	ev, dup, err := h.store.Record(ctx, webhookID, body, sig)
	if err != nil {
		log.Printf("[fw webhook] record event=%s type=%s: %v", webhookID, eventType, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to record event"})
		return
	}
	if dup {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "already processed"})
		return
	}

	dispErr := h.auto.DispatchFlutterwave(ctx, body)
	switch {
	case dispErr == nil:
		h.markStatus(ctx, ev.EventID, "processed", nil)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "processed"})
	case errors.Is(dispErr, services.ErrNonSettlingEvent):
		log.Printf("[fw webhook] non-settling event=%s type=%s: %v (marking processed)", webhookID, eventType, dispErr)
		h.markStatus(ctx, ev.EventID, "processed", nil)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "processed (no action)"})
	case errors.Is(dispErr, services.ErrChargeRejected):
		// Terminal: the charge contradicts the webhook and will never settle.
		// Mark 'rejected' (not 'failed') so it is never re-driven; the dispatcher
		// already alarmed. Ack so Flutterwave stops re-delivering.
		log.Printf("[fw webhook] REJECTED event=%s type=%s: %v (marking rejected, not retried)", webhookID, eventType, dispErr)
		h.markStatus(ctx, ev.EventID, "rejected", dispErr)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "received (rejected)"})
	default:
		log.Printf("[fw webhook] dispatch event=%s type=%s: %v", webhookID, eventType, dispErr)
		h.markStatus(ctx, ev.EventID, "failed", dispErr)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "received (deferred)"})
	}
}

func (h *FlutterwaveWebhookHandler) markStatus(ctx context.Context, eventID, status string, cause error) {
	updates := map[string]any{"status": status}
	if status == "processed" {
		updates["processed_at"] = time.Now()
	}
	if cause != nil {
		updates["error"] = cause.Error()
	}
	if err := h.store.DB().WithContext(ctx).
		Model(&webhookEventRow{}).
		Where("provider = ? AND event_id = ?", services.ProviderFlutterwave, eventID).
		Updates(updates).Error; err != nil {
		log.Printf("[fw webhook] mark %s event=%s: %v", status, eventID, err)
	}
}
