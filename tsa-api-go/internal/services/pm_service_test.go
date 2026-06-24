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
		nomba_account_ref        TEXT,
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
		reviewed_by              TEXT,
		reviewed_at              DATETIME,
		review_note              TEXT,
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

func TestPMService_GetByAccountRef(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Name:                 "Acme Corp",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: "9012345678",
		NombaAccountRef:      "pm-abc-123",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatal(err)
	}

	// Hit.
	got, err := svc.GetByAccountRef(context.Background(), "pm-abc-123")
	if err != nil {
		t.Fatalf("GetByAccountRef: %v", err)
	}
	if got.ID != pm.ID {
		t.Fatalf("wrong PM: got %v, want %v", got.ID, pm.ID)
	}

	// Miss.
	if _, err := svc.GetByAccountRef(context.Background(), "pm-nope"); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("miss: want ErrPMNotFound, got %v", err)
	}

	// Empty accountRef must NEVER match an arbitrary PM — rejected up front.
	if _, err := svc.GetByAccountRef(context.Background(), ""); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("empty accountRef: want ErrPMNotFound, got %v", err)
	}
}

func TestPMService_SumActiveAvailableNGN(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	// Empty table → "0.00", 0.
	sum, count, err := svc.SumActiveAvailableNGN(context.Background())
	if err != nil {
		t.Fatalf("SumActiveAvailableNGN empty: %v", err)
	}
	if sum != "0.00" || count != 0 {
		t.Fatalf("empty: got (%q,%d), want (\"0.00\",0)", sum, count)
	}

	// Two active PMs + one paused (paused excluded from the sum).
	mk := func(name, ngn, status string) {
		if err := db.Create(&models.PaymentMerchant{
			ID: uuid.New(), UserID: uuid.New(), Name: name, Status: status,
			AvailableNGN: ngn, ReservedNGN: "0",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "1000.50", models.PaymentMerchantStatusActive)
	mk("b", "2500.25", models.PaymentMerchantStatusActive)
	mk("c", "9999.00", models.PaymentMerchantStatusPaused)

	sum, count, err = svc.SumActiveAvailableNGN(context.Background())
	if err != nil {
		t.Fatalf("SumActiveAvailableNGN: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (paused excluded)", count)
	}
	if sum != "3500.75" {
		t.Errorf("sum = %q, want 3500.75", sum)
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

func TestPMService_ListAll(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	// Empty table — must return empty slice, not nil, not error.
	pms, err := svc.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll on empty table: %v", err)
	}
	if pms == nil {
		t.Fatal("ListAll returned nil slice, want empty slice")
	}
	if len(pms) != 0 {
		t.Fatalf("ListAll on empty table: want 0 rows, got %d", len(pms))
	}

	// Seed two PMs.
	pm1 := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Name:                 "Alpha PM",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: "1111111111",
		AvailableNGN:         "5000.00",
		ReservedNGN:          "1000.00",
		ProvisioningStatus:   models.PMProvisioningActive,
		PerTradeCapUSD:       500,
		DailyCapUSD:          5000,
	}
	pm2 := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Name:                 "Beta PM",
		Status:               models.PaymentMerchantStatusPaused,
		InboundAccountNumber: "2222222222",
		AvailableNGN:         "12000.00",
		ReservedNGN:          "3000.00",
		ProvisioningStatus:   models.PMProvisioningProvisioned,
		PerTradeCapUSD:       1000,
		DailyCapUSD:          10000,
	}
	if err := db.Create(pm1).Error; err != nil {
		t.Fatalf("create pm1: %v", err)
	}
	if err := db.Create(pm2).Error; err != nil {
		t.Fatalf("create pm2: %v", err)
	}

	pms, err = svc.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll with 2 rows: %v", err)
	}
	if len(pms) != 2 {
		t.Fatalf("ListAll: want 2 rows, got %d", len(pms))
	}

	// Verify both rows are present and fields are populated.
	ids := map[string]bool{pms[0].ID.String(): true, pms[1].ID.String(): true}
	if !ids[pm1.ID.String()] {
		t.Errorf("pm1 not in ListAll result")
	}
	if !ids[pm2.ID.String()] {
		t.Errorf("pm2 not in ListAll result")
	}

	// Spot-check fields on pm1.
	var got1 *models.PaymentMerchant
	for i := range pms {
		if pms[i].ID == pm1.ID {
			got1 = &pms[i]
			break
		}
	}
	if got1 == nil {
		t.Fatal("pm1 not found in result set")
	}
	if got1.AvailableNGN != "5000.00" {
		t.Errorf("pm1 AvailableNGN = %q, want 5000.00", got1.AvailableNGN)
	}
	if got1.ReservedNGN != "1000.00" {
		t.Errorf("pm1 ReservedNGN = %q, want 1000.00", got1.ReservedNGN)
	}
	if got1.Status != models.PaymentMerchantStatusActive {
		t.Errorf("pm1 Status = %q, want active", got1.Status)
	}
	if got1.ProvisioningStatus != models.PMProvisioningActive {
		t.Errorf("pm1 ProvisioningStatus = %q, want active", got1.ProvisioningStatus)
	}
}
