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

// paycrest_webhook_handler.go — POST /api/webhooks/paycrest.
//
// Mirrors the Nomba and Flutterwave handlers deliberately: signature-verified
// rather than auth-gated, persisted and deduped before dispatch, and ALWAYS
// 200 once the signature checks out. A provider's retry schedule is not a
// backoff strategy we control, so failures are recorded and re-driven by the
// poller instead of being hammered by Paycrest.

type paycrestVerifier interface {
	Verify(body []byte, signature string) bool
}

type paycrestWebhookDispatcher interface {
	HandleWebhook(ctx context.Context, ev *services.PaycrestWebhookEvent) error
}

type PaycrestWebhookHandler struct {
	verifier paycrestVerifier
	store    *services.NombaWebhookStore // provider-parameterized (paycrest)
	orders   paycrestWebhookDispatcher
}

// NewPaycrestWebhookHandler returns nil unless everything is wired, so the
// route can answer 503 rather than accepting events it cannot act on. The
// store MUST be built with NewWebhookStoreForProvider(db, ProviderPaycrest).
func NewPaycrestWebhookHandler(v paycrestVerifier, store *services.NombaWebhookStore, orders paycrestWebhookDispatcher) *PaycrestWebhookHandler {
	if v == nil || store == nil || orders == nil {
		return nil
	}
	return &PaycrestWebhookHandler{verifier: v, store: store, orders: orders}
}

func (h *PaycrestWebhookHandler) Handle(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Failed to read body"})
		return
	}

	sig := c.GetHeader(services.PaycrestSignatureHeader)

	// Dashboard verification probes arrive unsigned; real events always carry
	// the header. Acknowledge probes side-effect-free — we return before
	// recording or dispatching. A present-but-wrong signature is a genuine
	// mismatch and gets 401.
	if sig == "" {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "ok"})
		return
	}
	if !h.verifier.Verify(body, sig) {
		log.Printf("[paycrest webhook] signature mismatch (%d bytes)", len(body))
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
		return
	}

	ev, err := services.ParsePaycrestWebhook(body)
	if err != nil {
		// An event type we do not consume is well-formed and simply not ours to
		// act on. Acknowledge it: erroring would earn retries forever for
		// something that will never be handled.
		if errors.Is(err, services.ErrPaycrestUnknownEvent) {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "ignored"})
			return
		}
		log.Printf("[paycrest webhook] parse: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid envelope"})
		return
	}

	ctx := c.Request.Context()
	// Stored without any echoed buyer KYC; the signature was checked on the raw body.
	rec, dup, err := h.store.Record(ctx, ev.DedupeKey, services.ScrubPaycrestKYC(body), sig)
	if err != nil {
		log.Printf("[paycrest webhook] record %s: %v", ev.DedupeKey, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to record event"})
		return
	}
	if dup {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "already processed"})
		return
	}

	dispErr := h.orders.HandleWebhook(ctx, ev)
	switch {
	case dispErr == nil:
		h.mark(ctx, rec.EventID, "processed", nil)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "processed"})
	case errors.Is(dispErr, services.ErrPaycrestOrderUnknown):
		// Terminal: a redelivery cannot make the row appear. Most likely an
		// order created by another environment sharing this API key, which is
		// worth a log line but not a retry.
		log.Printf("[paycrest webhook] unknown order ref=%q id=%q — not retrying", ev.Reference, ev.OrderID)
		h.mark(ctx, rec.EventID, "rejected", dispErr)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "received (unknown order)"})
	default:
		// Recorded 'failed' and acknowledged: the poller re-drives it on its
		// own schedule, which we control and Paycrest's retries do not.
		log.Printf("[paycrest webhook] dispatch %s: %v", ev.DedupeKey, dispErr)
		h.mark(ctx, rec.EventID, "failed", dispErr)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "received (deferred)"})
	}
}

func (h *PaycrestWebhookHandler) mark(ctx context.Context, eventID, status string, cause error) {
	updates := map[string]any{"status": status}
	if status == "processed" {
		updates["processed_at"] = time.Now()
	}
	if cause != nil {
		updates["error"] = cause.Error()
	}
	if err := h.store.DB().WithContext(ctx).
		Model(&webhookEventRow{}).
		Where("provider = ? AND event_id = ?", services.ProviderPaycrest, eventID).
		Updates(updates).Error; err != nil {
		log.Printf("[paycrest webhook] mark %s event=%s: %v", status, eventID, err)
	}
}
