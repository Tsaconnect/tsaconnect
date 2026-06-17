package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newSQLiteForPM opens an in-memory SQLite DB with the payment_merchants table
// created via raw DDL (not AutoMigrate) to avoid PostgreSQL-specific defaults
// such as gen_random_uuid(). Mirrors the fee_config and p2p test setups.
func newSQLiteForPM(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS payment_merchants (
		id                       TEXT PRIMARY KEY,
		user_id                  TEXT NOT NULL,
		name                     TEXT NOT NULL,
		status                   TEXT NOT NULL DEFAULT 'pending',
		nomba_account_id         TEXT,
		nomba_wallet_id          TEXT,
		nomba_creds_ref          TEXT,
		inbound_account_number   TEXT,
		inbound_bank             TEXT,
		operator_wallet_address  TEXT,
		nomba_sub_account_id     TEXT,
		settlement_bank_code     TEXT,
		settlement_account_number TEXT,
		settlement_account_name  TEXT,
		available_ngn            TEXT NOT NULL DEFAULT '0',
		reserved_ngn             TEXT NOT NULL DEFAULT '0',
		provisioning_status      TEXT NOT NULL DEFAULT 'pending',
		per_trade_cap_usd        REAL NOT NULL DEFAULT 0,
		daily_cap_usd            REAL NOT NULL DEFAULT 0,
		created_at               DATETIME,
		updated_at               DATETIME
	)`).Error; err != nil {
		t.Fatalf("create payment_merchants: %v", err)
	}
	return db
}

func TestPMService_GetByInboundAccount(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Name:                 "TSA",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: "1234567890",
		InboundBank:          "Nomba",
		PerTradeCapUSD:       1000,
		DailyCapUSD:          5000,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetByInboundAccount(context.Background(), "1234567890")
	if err != nil {
		t.Fatalf("GetByInboundAccount: %v", err)
	}
	if got.ID != pm.ID {
		t.Fatalf("wrong PM: got %v, want %v", got.ID, pm.ID)
	}

	if _, err := svc.GetByInboundAccount(context.Background(), "0000000000"); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("want ErrPMNotFound, got %v", err)
	}
}

func TestPMService_GetByWalletID(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Name:                 "TSA",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: "1234567890",
		NombaWalletID:        "wallet-123",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatal(err)
	}

	// Hit.
	got, err := svc.GetByWalletID(context.Background(), "wallet-123")
	if err != nil {
		t.Fatalf("GetByWalletID: %v", err)
	}
	if got.ID != pm.ID {
		t.Fatalf("wrong PM: got %v, want %v", got.ID, pm.ID)
	}

	// Miss.
	if _, err := svc.GetByWalletID(context.Background(), "wallet-nope"); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("miss: want ErrPMNotFound, got %v", err)
	}

	// Empty walletId must NEVER match an arbitrary PM (even one with a blank
	// wallet id) — it is rejected up front with ErrPMNotFound.
	if _, err := svc.GetByWalletID(context.Background(), ""); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("empty walletId: want ErrPMNotFound, got %v", err)
	}
}

func TestPMService_GetByID(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Name:                 "TSA",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: "111",
	}
	db.Create(pm)

	got, err := svc.GetByID(context.Background(), pm.ID)
	if err != nil || got.ID != pm.ID {
		t.Fatalf("GetByID: %v", err)
	}

	if _, err := svc.GetByID(context.Background(), uuid.New()); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("want ErrPMNotFound, got %v", err)
	}
}

func TestPMService_Caps(t *testing.T) {
	pm := &models.PaymentMerchant{PerTradeCapUSD: 100}

	if !WithinPerTradeCap(pm, 100) {
		t.Fatal("100 within cap 100")
	}
	if WithinPerTradeCap(pm, 100.01) {
		t.Fatal("100.01 exceeds cap 100")
	}
	if !WithinPerTradeCap(&models.PaymentMerchant{PerTradeCapUSD: 0}, 1e9) {
		t.Fatal("0 cap = unlimited")
	}
}

func TestPMService_SeedTSA_Idempotent(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	userID := uuid.New()
	const (
		acct        = "9876543210"
		bank        = "Nomba"
		wallet      = "0xABCDEF1234567890"
		nombaWallet = "wallet-seed-1"
		name        = "TSA Liquidity"
		name2       = "TSA Liquidity Updated"
	)

	// First call — should insert.
	pm1, err := svc.SeedTSA(context.Background(), userID, name, acct, bank, wallet, nombaWallet, 500, 10000)
	if err != nil {
		t.Fatalf("SeedTSA first call: %v", err)
	}
	if pm1.ID == uuid.Nil {
		t.Fatal("expected non-nil ID after first seed")
	}
	if pm1.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("expected active status, got %q", pm1.Status)
	}

	// The seeded wallet id is set.
	if pm1.NombaWalletID != nombaWallet {
		t.Fatalf("seeded NombaWalletID = %q, want %q", pm1.NombaWalletID, nombaWallet)
	}

	// Second call with updated name — should update, NOT insert.
	pm2, err := svc.SeedTSA(context.Background(), userID, name2, acct, bank, wallet, nombaWallet, 500, 10000)
	if err != nil {
		t.Fatalf("SeedTSA second call: %v", err)
	}
	if pm2.ID != pm1.ID {
		t.Fatalf("second seed returned different ID: got %v, want %v", pm2.ID, pm1.ID)
	}

	// Confirm exactly one row in the DB.
	var count int64
	if err := db.Model(&models.PaymentMerchant{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after two seeds, got %d", count)
	}

	// Confirm the name was updated on the second call.
	refreshed, err := svc.GetByID(context.Background(), pm1.ID)
	if err != nil {
		t.Fatalf("GetByID after second seed: %v", err)
	}
	if refreshed.Name != name2 {
		t.Fatalf("name not updated: got %q, want %q", refreshed.Name, name2)
	}

	// And the seeded row is resolvable by its Nomba walletId.
	byWallet, err := svc.GetByWalletID(context.Background(), nombaWallet)
	if err != nil {
		t.Fatalf("GetByWalletID after seed: %v", err)
	}
	if byWallet.ID != pm1.ID {
		t.Fatalf("GetByWalletID resolved %v, want %v", byWallet.ID, pm1.ID)
	}
}
