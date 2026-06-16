// Nomba webhook handler HTTP-level tests. Exercise signature verification,
// (provider,event_id) dedup, classification (inbound vs payout), and dispatch
// via a narrow fake dispatcher — no full PMAutomationService graph needed.
//
// The webhook_events store runs over in-memory SQLite with raw DDL (copied from
// services/nomba_webhook_test.go) to avoid PostgreSQL-specific types.

package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testSigHeader = "X-Nomba-Signature"

// ─── Fake dispatcher ──────────────────────────────────────────────────────────

type fakeWebhookDispatcher struct {
	inbound     []services.InboundPayment
	payouts     []services.PayoutStatus
	inboundErr  error
	payoutErr   error
}

func (f *fakeWebhookDispatcher) HandleInbound(_ context.Context, p services.InboundPayment) error {
	f.inbound = append(f.inbound, p)
	return f.inboundErr
}

func (f *fakeWebhookDispatcher) HandlePayoutStatus(_ context.Context, p services.PayoutStatus) error {
	f.payouts = append(f.payouts, p)
	return f.payoutErr
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func newSQLiteWebhookStore(t *testing.T) (*gorm.DB, *services.NombaWebhookStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE webhook_events (
		id TEXT PRIMARY KEY,
		provider TEXT,
		event_id TEXT,
		payload TEXT,
		signature TEXT,
		status TEXT,
		error TEXT,
		received_at DATETIME,
		processed_at DATETIME,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create webhook_events: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_webhook_provider_event ON webhook_events(provider, event_id)`).Error; err != nil {
		t.Fatalf("create unique index: %v", err)
	}
	return db, services.NewNombaWebhookStore(db)
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, h *NombaWebhookHandler, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/webhooks/nomba", h.Handle)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/nomba", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set(testSigHeader, sig)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const webhookSecret = "topsecret"

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestNombaWebhook_ValidInbound_Dispatches(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := NewNombaWebhookHandler(services.NewHMACWebhookVerifier(webhookSecret), store, disp, testSigHeader)

	body := []byte(`{"event_id":"evt_in_1","data":{"destination_account":"1234567890","sender_name":"JOHN DOE","sender_account":"0987654321","amount":"1500.00","status":"settled"}}`)
	w := postWebhook(t, h, body, signBody(webhookSecret, body))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.inbound) != 1 {
		t.Fatalf("want 1 inbound dispatch, got %d", len(disp.inbound))
	}
	if disp.inbound[0].EventID != "evt_in_1" || disp.inbound[0].DestinationAccount != "1234567890" {
		t.Fatalf("bad inbound payload: %+v", disp.inbound[0])
	}
	if len(disp.payouts) != 0 {
		t.Fatalf("payout should not be dispatched for an inbound event")
	}
}

func TestNombaWebhook_ValidPayout_Dispatches(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := NewNombaWebhookHandler(services.NewHMACWebhookVerifier(webhookSecret), store, disp, testSigHeader)

	body := []byte(`{"event_id":"evt_po_1","event":"payout.success","data":{"payout_id":"po_1","status":"success"}}`)
	w := postWebhook(t, h, body, signBody(webhookSecret, body))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.payouts) != 1 {
		t.Fatalf("want 1 payout dispatch, got %d", len(disp.payouts))
	}
	if disp.payouts[0].PayoutID != "po_1" || disp.payouts[0].Status != "success" {
		t.Fatalf("bad payout payload: %+v", disp.payouts[0])
	}
	if len(disp.inbound) != 0 {
		t.Fatalf("inbound should not be dispatched for a payout event")
	}
}

func TestNombaWebhook_BadSignature_401(t *testing.T) {
	db, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := NewNombaWebhookHandler(services.NewHMACWebhookVerifier(webhookSecret), store, disp, testSigHeader)

	body := []byte(`{"event_id":"evt_bad","data":{"status":"settled"}}`)
	w := postWebhook(t, h, body, "deadbeef")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.inbound) != 0 || len(disp.payouts) != 0 {
		t.Fatal("bad-sig event must not dispatch")
	}
	// Must NOT be recorded.
	var count int64
	db.Table("webhook_events").Count(&count)
	if count != 0 {
		t.Fatalf("bad-sig event must not be recorded, found %d rows", count)
	}
}

func TestNombaWebhook_Duplicate_NotReDispatched(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := NewNombaWebhookHandler(services.NewHMACWebhookVerifier(webhookSecret), store, disp, testSigHeader)

	body := []byte(`{"event_id":"evt_dup","data":{"destination_account":"1","sender_name":"A","sender_account":"2","amount":"1.00","status":"settled"}}`)
	sig := signBody(webhookSecret, body)

	w1 := postWebhook(t, h, body, sig)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call want 200, got %d", w1.Code)
	}
	w2 := postWebhook(t, h, body, sig)
	if w2.Code != http.StatusOK {
		t.Fatalf("second (dup) call want 200, got %d", w2.Code)
	}
	if len(disp.inbound) != 1 {
		t.Fatalf("duplicate must dispatch exactly once, got %d", len(disp.inbound))
	}
}

func TestNombaWebhook_EmptyEventID_400(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := NewNombaWebhookHandler(services.NewHMACWebhookVerifier(webhookSecret), store, disp, testSigHeader)

	body := []byte(`{"data":{"status":"settled"}}`)
	w := postWebhook(t, h, body, signBody(webhookSecret, body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.inbound) != 0 || len(disp.payouts) != 0 {
		t.Fatal("empty-event-id must not dispatch")
	}
}
