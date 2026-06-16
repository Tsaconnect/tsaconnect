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
	t.Run("payment_success happy path", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payment_success",
			"requestId": "REQ-uuid",
			"data": {
				"merchant": {"walletId": "wallet-123", "walletBalance": 50000, "userId": "user-uuid"},
				"transaction": {"transactionId": "txn-1", "type": "vact_transfer", "transactionAmount": 1500, "fee": 0, "time": "2026-06-16T10:00:00Z", "responseCode": "00", "narration": "..."},
				"customer": {"senderName": "JOHN DOE", "accountNumber": "0123456789", "bankCode": "058", "bankName": "GTB"}
			}
		}`)
		p, err := ParseNombaInbound(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.EventID != "REQ-uuid" {
			t.Errorf("EventID = %q; want %q", p.EventID, "REQ-uuid")
		}
		if p.WalletID != "wallet-123" {
			t.Errorf("WalletID = %q; want %q", p.WalletID, "wallet-123")
		}
		if p.SenderAccountName != "JOHN DOE" {
			t.Errorf("SenderAccountName = %q; want %q", p.SenderAccountName, "JOHN DOE")
		}
		if p.SenderAccountNumber != "0123456789" {
			t.Errorf("SenderAccountNumber = %q; want %q", p.SenderAccountNumber, "0123456789")
		}
		if p.AmountNGN != "1500.00" {
			t.Errorf("AmountNGN = %q; want %q", p.AmountNGN, "1500.00")
		}
		if p.Status != "settled" {
			t.Errorf("Status = %q; want %q", p.Status, "settled")
		}
	})

	t.Run("amount with decimal formats to 2dp", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payment_success",
			"requestId": "REQ-dec",
			"data": {
				"merchant": {"walletId": "wallet-456"},
				"transaction": {"transactionAmount": 1500.5},
				"customer": {"senderName": "JANE", "accountNumber": "111"}
			}
		}`)
		p, err := ParseNombaInbound(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.AmountNGN != "1500.50" {
			t.Errorf("AmountNGN = %q; want %q", p.AmountNGN, "1500.50")
		}
	})

	t.Run("wrong event_type returns error", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payment_failed",
			"requestId": "REQ-fail",
			"data": {
				"merchant": {"walletId": "wallet-123"},
				"transaction": {"transactionAmount": 1500},
				"customer": {}
			}
		}`)
		_, err := ParseNombaInbound(raw)
		if err == nil {
			t.Fatal("expected error for non-payment_success event, got nil")
		}
	})

	t.Run("missing walletId returns error", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payment_success",
			"requestId": "REQ-nowallet",
			"data": {
				"merchant": {},
				"transaction": {"transactionAmount": 1500},
				"customer": {}
			}
		}`)
		_, err := ParseNombaInbound(raw)
		if err == nil {
			t.Fatal("expected error for missing walletId, got nil")
		}
	})

	t.Run("missing requestId returns error", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payment_success",
			"data": {
				"merchant": {"walletId": "wallet-123"},
				"transaction": {"transactionAmount": 1500},
				"customer": {}
			}
		}`)
		_, err := ParseNombaInbound(raw)
		if err == nil {
			t.Fatal("expected error for missing requestId, got nil")
		}
	})
}

func TestParsePayout(t *testing.T) {
	t.Run("payout_success happy path", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payout_success",
			"requestId": "REQ-uuid2",
			"data": {
				"merchant": {"walletId": "...", "userId": "..."},
				"transaction": {"transactionId": "API-TRANSFER-abc", "type": "transfer", "transactionAmount": 1500, "fee": 50, "time": "...", "responseCode": "00", "merchantTxRef": "trade-uuid-123"},
				"customer": {"senderName": "...", "recipientName": "...", "accountNumber": "...", "bankCode": "...", "bankName": "..."}
			}
		}`)
		p, err := ParseNombaPayout(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.EventID != "REQ-uuid2" {
			t.Errorf("EventID = %q; want %q", p.EventID, "REQ-uuid2")
		}
		if p.PayoutID != "API-TRANSFER-abc" {
			t.Errorf("PayoutID = %q; want %q", p.PayoutID, "API-TRANSFER-abc")
		}
		if p.MerchantTxRef != "trade-uuid-123" {
			t.Errorf("MerchantTxRef = %q; want %q", p.MerchantTxRef, "trade-uuid-123")
		}
		if p.Status != "success" {
			t.Errorf("Status = %q; want %q", p.Status, "success")
		}
	})

	t.Run("payout_failed maps to failed", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payout_failed",
			"requestId": "REQ-fail",
			"data": {
				"merchant": {},
				"transaction": {"transactionId": "API-TRANSFER-xyz", "merchantTxRef": "trade-uuid-456"},
				"customer": {}
			}
		}`)
		p, err := ParseNombaPayout(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Status != "failed" {
			t.Errorf("Status = %q; want %q", p.Status, "failed")
		}
		if p.MerchantTxRef != "trade-uuid-456" {
			t.Errorf("MerchantTxRef = %q; want %q", p.MerchantTxRef, "trade-uuid-456")
		}
	})

	t.Run("unknown event_type maps to pending", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payout_processing",
			"requestId": "REQ-pending",
			"data": {
				"merchant": {},
				"transaction": {"transactionId": "API-TRANSFER-pend"},
				"customer": {}
			}
		}`)
		p, err := ParseNombaPayout(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Status != "pending" {
			t.Errorf("Status = %q; want %q", p.Status, "pending")
		}
	})

	t.Run("missing requestId returns error", func(t *testing.T) {
		raw := []byte(`{
			"event_type": "payout_success",
			"data": {
				"merchant": {},
				"transaction": {},
				"customer": {}
			}
		}`)
		_, err := ParseNombaPayout(raw)
		if err == nil {
			t.Fatal("expected error for missing requestId, got nil")
		}
	})
}
