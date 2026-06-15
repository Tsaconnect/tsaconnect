package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newSQLiteForWebhook opens an in-memory SQLite DB with the webhook_events table
// created via raw DDL (avoiding PostgreSQL-specific types like jsonb and uuid
// defaults). Mirrors the fee_config_service_test.go pattern.
func newSQLiteForWebhook(t *testing.T) *gorm.DB {
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
	return db
}

func TestHMACVerifier(t *testing.T) {
	v := NewHMACWebhookVerifier("topsecret")
	body := []byte(`{"event":"payment_success"}`)
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	if !v.Verify(body, sig) {
		t.Fatal("valid sig rejected")
	}
	if v.Verify(body, "deadbeef") {
		t.Fatal("bad sig accepted")
	}
	if v.Verify(body, "") {
		t.Fatal("empty sig accepted")
	}
}

func TestRecordWebhookEvent_Dedup(t *testing.T) {
	db := newSQLiteForWebhook(t)
	w := NewNombaWebhookStore(db)
	body := []byte(`{"id":"evt_1"}`)
	ev, dup, err := w.Record(context.Background(), "evt_1", body, "sig")
	if err != nil || dup {
		t.Fatalf("first record dup=%v err=%v", dup, err)
	}
	if ev == nil || ev.EventID != "evt_1" {
		t.Fatal("event not returned")
	}
	_, dup2, err := w.Record(context.Background(), "evt_1", body, "sig")
	if err != nil {
		t.Fatal(err)
	}
	if !dup2 {
		t.Fatal("second record with same event id should be dup")
	}
}

func TestParseInbound(t *testing.T) {
	// assumed Nomba shape (GATED on real facts — see TODO in impl)
	raw := []byte(`{"event_id":"evt_9","data":{"destination_account":"1234567890","sender_name":"JOHN DOE","sender_account":"0987654321","amount":"1500.00","status":"settled"}}`)
	p, err := ParseNombaInbound(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.EventID != "evt_9" || p.DestinationAccount != "1234567890" || p.SenderAccountName != "JOHN DOE" || p.AmountNGN != "1500.00" || p.Status != "settled" {
		t.Fatalf("bad parse: %+v", p)
	}
}

func TestParsePayout(t *testing.T) {
	raw := []byte(`{"event_id":"evt_p","data":{"payout_id":"po_1","status":"success"}}`)
	p, err := ParseNombaPayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.EventID != "evt_p" || p.PayoutID != "po_1" || p.Status != "success" {
		t.Fatalf("bad parse: %+v", p)
	}
}
