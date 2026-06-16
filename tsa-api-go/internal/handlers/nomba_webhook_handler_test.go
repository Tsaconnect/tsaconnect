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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── Fake dispatcher ──────────────────────────────────────────────────────────

type fakeWebhookDispatcher struct {
	inbound    []services.InboundPayment
	payouts    []services.PayoutStatus
	inboundErr error
	payoutErr  error
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

const webhookSecret = "topsecret"
const testTimestamp = "2026-06-16T10:00:00Z"

// nombaSign reconstructs Nomba's colon-delimited signing string from the parsed
// body + timestamp and returns the base64 HMAC-SHA256 signature — i.e. exactly
// what a correct Nomba caller would put in the nomba-signature header.
func nombaSign(t *testing.T, secret string, body []byte, timestamp string) string {
	t.Helper()
	var env struct {
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
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("nombaSign unmarshal: %v", err)
	}
	signing := strings.Join([]string{
		env.EventType, env.RequestID, env.Data.Merchant.UserID, env.Data.Merchant.WalletID,
		env.Data.Transaction.TransactionID, env.Data.Transaction.Type,
		env.Data.Transaction.Time, env.Data.Transaction.ResponseCode, timestamp,
	}, ":")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func newWebhookHandler(store *services.NombaWebhookStore, disp webhookDispatcher) *NombaWebhookHandler {
	return NewNombaWebhookHandler(services.NewNombaWebhookVerifier(webhookSecret), store, disp)
}

func postWebhook(t *testing.T, h *NombaWebhookHandler, body []byte, sig, ts string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/webhooks/nomba", h.Handle)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/nomba", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set("nomba-signature", sig)
	}
	if ts != "" {
		req.Header.Set("nomba-timestamp", ts)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// inboundBody is a real-shape Nomba payment_success body. It also carries an
// event_id so the handler's dedup envelope can extract it.
func inboundBody(eventID string) []byte {
	return []byte(`{
		"event_id": "` + eventID + `",
		"event_type": "payment_success",
		"requestId": "REQ-` + eventID + `",
		"data": {
			"merchant": {"walletId": "wallet-123", "userId": "user-uuid"},
			"transaction": {"transactionId": "txn-1", "type": "vact_transfer", "transactionAmount": 1500, "fee": 0, "time": "2026-06-16T09:00:00Z", "responseCode": "00", "narration": "..."},
			"customer": {"senderName": "JOHN DOE", "accountNumber": "0987654321", "bankCode": "058", "bankName": "GTB"}
		}
	}`)
}

func TestNombaWebhook_ValidInbound_Dispatches(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	body := inboundBody("evt_in_1")
	w := postWebhook(t, h, body, nombaSign(t, webhookSecret, body, testTimestamp), testTimestamp)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.inbound) != 1 {
		t.Fatalf("want 1 inbound dispatch, got %d", len(disp.inbound))
	}
	if disp.inbound[0].WalletID != "wallet-123" || disp.inbound[0].SenderAccountName != "JOHN DOE" {
		t.Fatalf("bad inbound payload: %+v", disp.inbound[0])
	}
	if len(disp.payouts) != 0 {
		t.Fatalf("payout should not be dispatched for an inbound event")
	}
}

func TestNombaWebhook_ValidPayout_Dispatches(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	body := []byte(`{
		"event_id": "evt_po_1",
		"event_type": "payout_success",
		"requestId": "REQ-po-1",
		"data": {
			"merchant": {"walletId": "wallet-123", "userId": "user-uuid"},
			"transaction": {"transactionId": "po_1", "type": "transfer", "time": "2026-06-16T09:00:00Z", "responseCode": "00", "merchantTxRef": "trade-uuid-123"},
			"customer": {}
		}
	}`)
	w := postWebhook(t, h, body, nombaSign(t, webhookSecret, body, testTimestamp), testTimestamp)

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

// TestNombaWebhook_RealPayoutShape_RoutesToPayout asserts that a payout body
// matching the REAL production Nomba shape — discriminated ONLY by top-level
// event_type ("payout_success"), with NO synthetic "event"/"payout_id" fields —
// is classified as a payout and routed to HandlePayoutStatus (NOT HandleInbound).
// This is the regression guard for the misclassification bug that broke the PM
// sell-flow release.
func TestNombaWebhook_RealPayoutShape_RoutesToPayout(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	body := []byte(`{
		"event_type": "payout_success",
		"requestId": "REQ-real-po-1",
		"data": {
			"merchant": {"walletId": "wallet-123", "userId": "user-uuid"},
			"transaction": {"transactionId": "po_real_1", "type": "transfer", "time": "2026-06-16T09:00:00Z", "responseCode": "00", "merchantTxRef": "trade-uuid-456"},
			"customer": {}
		}
	}`)
	if strings.Contains(string(body), `"event"`) || strings.Contains(string(body), `"payout_id"`) {
		t.Fatal("real-shape fixture must NOT carry synthetic event/payout_id fields")
	}

	w := postWebhook(t, h, body, nombaSign(t, webhookSecret, body, testTimestamp), testTimestamp)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.payouts) != 1 {
		t.Fatalf("real payout shape must route to HandlePayoutStatus, got %d payout dispatches", len(disp.payouts))
	}
	if len(disp.inbound) != 0 {
		t.Fatalf("real payout shape must NOT route to HandleInbound, got %d inbound dispatches", len(disp.inbound))
	}
	if disp.payouts[0].PayoutID != "po_real_1" || disp.payouts[0].MerchantTxRef != "trade-uuid-456" || disp.payouts[0].Status != "success" {
		t.Fatalf("bad payout payload: %+v", disp.payouts[0])
	}
}

func TestNombaWebhook_BadSignature_401(t *testing.T) {
	db, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	body := inboundBody("evt_bad")
	w := postWebhook(t, h, body, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", testTimestamp)

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

func TestNombaWebhook_MissingTimestamp_401(t *testing.T) {
	db, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	body := inboundBody("evt_nots")
	// Valid signature for testTimestamp, but no timestamp header sent.
	w := postWebhook(t, h, body, nombaSign(t, webhookSecret, body, testTimestamp), "")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.inbound) != 0 || len(disp.payouts) != 0 {
		t.Fatal("missing-timestamp event must not dispatch")
	}
	var count int64
	db.Table("webhook_events").Count(&count)
	if count != 0 {
		t.Fatalf("missing-timestamp event must not be recorded, found %d rows", count)
	}
}

func TestNombaWebhook_Duplicate_NotReDispatched(t *testing.T) {
	db, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	body := inboundBody("evt_dup")
	sig := nombaSign(t, webhookSecret, body, testTimestamp)

	w1 := postWebhook(t, h, body, sig, testTimestamp)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call want 200, got %d", w1.Code)
	}
	w2 := postWebhook(t, h, body, sig, testTimestamp)
	if w2.Code != http.StatusOK {
		t.Fatalf("second (dup) call want 200, got %d", w2.Code)
	}
	if len(disp.inbound) != 1 {
		t.Fatalf("duplicate must dispatch exactly once, got %d", len(disp.inbound))
	}

	// Dedup is keyed on requestId (Nomba's real id), NOT the top-level event_id.
	// The recorded webhook_events row's event_id column holds the requestId.
	var recorded string
	if err := db.Table("webhook_events").Select("event_id").Row().Scan(&recorded); err != nil {
		t.Fatalf("scan recorded event_id: %v", err)
	}
	if recorded != "REQ-evt_dup" {
		t.Fatalf("dedup key = %q, want requestId %q", recorded, "REQ-evt_dup")
	}
}

func TestNombaWebhook_EmptyEventID_400(t *testing.T) {
	_, store := newSQLiteWebhookStore(t)
	disp := &fakeWebhookDispatcher{}
	h := newWebhookHandler(store, disp)

	// Real-shape body but WITHOUT a requestId field (the dedup key). Missing
	// requestId → 400.
	body := []byte(`{
		"event_type": "payment_success",
		"data": {
			"merchant": {"walletId": "wallet-123", "userId": "user-uuid"},
			"transaction": {"transactionId": "txn-1", "type": "vact_transfer", "transactionAmount": 1500, "fee": 0, "time": "2026-06-16T09:00:00Z", "responseCode": "00"},
			"customer": {"senderName": "JOHN DOE", "accountNumber": "0987654321"}
		}
	}`)
	w := postWebhook(t, h, body, nombaSign(t, webhookSecret, body, testTimestamp), testTimestamp)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", w.Code, w.Body.String())
	}
	if len(disp.inbound) != 0 || len(disp.payouts) != 0 {
		t.Fatal("empty-event-id must not dispatch")
	}
}
