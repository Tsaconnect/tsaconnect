package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The FW webhook receipt path, end to end over the real handler + real
// verifier + real store (sqlite): signature gate, unsigned-probe tolerance,
// dedup on webhook_id, and processed/failed status marking.

type fakeFWDispatcher struct {
	bodies [][]byte
	err    error
}

func (f *fakeFWDispatcher) DispatchFlutterwave(_ context.Context, body []byte) error {
	f.bodies = append(f.bodies, append([]byte(nil), body...))
	return f.err
}

func newFWSQLiteStore(t *testing.T) *services.NombaWebhookStore {
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
	return services.NewWebhookStoreForProvider(db, services.ProviderFlutterwave)
}

const fwTestSecret = "kt6-test-secret-hash"

func fwSignBody(body []byte) string {
	mac := hmac.New(sha256.New, []byte(fwTestSecret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func fwPost(t *testing.T, h *FlutterwaveWebhookHandler, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/flutterwave", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set("flutterwave-signature", sig)
	}
	c.Request = req
	h.Handle(c)
	return w
}

func fwEventStatus(t *testing.T, store *services.NombaWebhookStore, eventID string) (status, errMsg string, found bool) {
	t.Helper()
	var row struct {
		Status string
		Error  string
	}
	err := store.DB().Table("webhook_events").
		Where("provider = ? AND event_id = ?", services.ProviderFlutterwave, eventID).
		Select("status, error").Scan(&row).Error
	if err != nil {
		t.Fatalf("query event: %v", err)
	}
	if row.Status == "" {
		return "", "", false
	}
	return row.Status, row.Error, true
}

func TestFWWebhookReceipt_EndToEnd(t *testing.T) {
	store := newFWSQLiteStore(t)
	disp := &fakeFWDispatcher{}
	h := NewFlutterwaveWebhookHandler(services.NewFlutterwaveWebhookVerifier(fwTestSecret), store, disp)

	body := []byte(`{"webhook_id":"wh_e2e_1","type":"charge.completed","data":{"reference":"IT-E2E00000001","amount":500,"currency":"NGN","status":"succeeded"}}`)

	// 1. Unsigned probe: 200, nothing recorded, nothing dispatched.
	if w := fwPost(t, h, body, ""); w.Code != http.StatusOK {
		t.Fatalf("probe: %d", w.Code)
	}
	if _, _, found := fwEventStatus(t, store, "wh_e2e_1"); found {
		t.Fatal("probe recorded an event")
	}

	// 2. Wrong signature: 401, nothing recorded.
	if w := fwPost(t, h, body, "bm90LXRoZS1zaWc="); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig: %d", w.Code)
	}
	if len(disp.bodies) != 0 {
		t.Fatal("bad sig reached dispatcher")
	}

	// 3. Valid delivery: 200 processed, row processed, dispatcher got the body.
	if w := fwPost(t, h, body, fwSignBody(body)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "processed") {
		t.Fatalf("valid: %d %s", w.Code, w.Body.String())
	}
	if st, _, _ := fwEventStatus(t, store, "wh_e2e_1"); st != "processed" {
		t.Fatalf("status = %q, want processed", st)
	}
	if len(disp.bodies) != 1 {
		t.Fatalf("dispatch count = %d", len(disp.bodies))
	}

	// 4. Redelivery of the same webhook_id: acked, NOT re-dispatched.
	if w := fwPost(t, h, body, fwSignBody(body)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "already") {
		t.Fatalf("redelivery: %d %s", w.Code, w.Body.String())
	}
	if len(disp.bodies) != 1 {
		t.Fatalf("redelivery re-dispatched: %d", len(disp.bodies))
	}
}

func TestFWWebhookReceipt_FailureAndNonSettling(t *testing.T) {
	store := newFWSQLiteStore(t)
	disp := &fakeFWDispatcher{err: errors.New("db down")}
	h := NewFlutterwaveWebhookHandler(services.NewFlutterwaveWebhookVerifier(fwTestSecret), store, disp)

	// Dispatch failure: still 200 (we own retries), row marked failed w/ error.
	body := []byte(`{"webhook_id":"wh_fail_1","type":"transfer.disburse","data":{"id":1,"reference":"IT-SELL-x","status":"SUCCESSFUL"}}`)
	if w := fwPost(t, h, body, fwSignBody(body)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "deferred") {
		t.Fatalf("failure: %d %s", w.Code, w.Body.String())
	}
	if st, errMsg, _ := fwEventStatus(t, store, "wh_fail_1"); st != "failed" || !strings.Contains(errMsg, "db down") {
		t.Fatalf("failure row: %q %q", st, errMsg)
	}

	// Non-settling (unknown event type): 200, marked processed (no action).
	disp.err = nil
	unknown := []byte(`{"webhook_id":"wh_unk_1","type":"refund.completed","data":{}}`)
	h2disp := &fakeFWDispatcher{err: services.ErrNonSettlingEvent}
	h2 := NewFlutterwaveWebhookHandler(services.NewFlutterwaveWebhookVerifier(fwTestSecret), store, h2disp)
	if w := fwPost(t, h2, unknown, fwSignBody(unknown)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "no action") {
		t.Fatalf("non-settling: %d %s", w.Code, w.Body.String())
	}
	if st, _, _ := fwEventStatus(t, store, "wh_unk_1"); st != "processed" {
		t.Fatalf("non-settling row: %q", st)
	}
}
