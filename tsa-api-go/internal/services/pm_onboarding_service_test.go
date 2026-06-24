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

// newSQLiteForOnboarding opens an in-memory SQLite DB with the payment_merchants
// and merchant_requests tables (raw DDL, not AutoMigrate) including all Task-1 columns.
func newSQLiteForOnboarding(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	sqls := []string{
		`CREATE TABLE IF NOT EXISTS payment_merchants (
			id                        TEXT PRIMARY KEY,
			user_id                   TEXT NOT NULL,
			name                      TEXT NOT NULL DEFAULT '',
			status                    TEXT NOT NULL DEFAULT 'pending',
			nomba_account_id          TEXT,
			nomba_wallet_id           TEXT,
			nomba_creds_ref           TEXT,
			inbound_account_number    TEXT,
			inbound_bank              TEXT,
			nomba_account_ref         TEXT,
			operator_wallet_address   TEXT,
			nomba_sub_account_id      TEXT,
			settlement_bank_code      TEXT,
			settlement_account_number TEXT,
			settlement_account_name   TEXT,
			available_ngn             TEXT NOT NULL DEFAULT '0',
			reserved_ngn              TEXT NOT NULL DEFAULT '0',
			provisioning_status       TEXT NOT NULL DEFAULT 'pending',
			per_trade_cap_usd         REAL NOT NULL DEFAULT 0,
			daily_cap_usd             REAL NOT NULL DEFAULT 0,
			reviewed_by               TEXT,
			reviewed_at               DATETIME,
			review_note               TEXT,
			created_at                DATETIME,
			updated_at                DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS merchant_requests (
			id                          TEXT PRIMARY KEY,
			user_id                     TEXT NOT NULL,
			business_type               TEXT NOT NULL,
			business_name               TEXT NOT NULL DEFAULT '',
			business_description        TEXT,
			address                     TEXT NOT NULL DEFAULT '',
			city                        TEXT NOT NULL DEFAULT '',
			state                       TEXT NOT NULL DEFAULT '',
			country                     TEXT NOT NULL DEFAULT '',
			phone                       TEXT NOT NULL DEFAULT '',
			registration_number         TEXT,
			status                      TEXT NOT NULL DEFAULT 'pending',
			admin_note                  TEXT,
			reviewed_by                 TEXT,
			reviewed_at                 DATETIME,
			settlement_bank_code        TEXT,
			settlement_account_number   TEXT,
			maker_wallet_address        TEXT,
			requested_per_trade_cap_usd REAL DEFAULT 0,
			requested_daily_cap_usd     REAL DEFAULT 0,
			created_at                  DATETIME,
			updated_at                  DATETIME
		)`,
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return db
}

// fakeMerchantRequest builds a minimal MerchantRequest for provisioning tests.
func fakeMerchantRequest(userID uuid.UUID) *models.MerchantRequest {
	return &models.MerchantRequest{
		ID:                      uuid.New(),
		UserID:                  userID,
		BusinessType:            models.BusinessTypeP2PMerchant,
		BusinessName:            "Acme Corp",
		SettlementBankCode:      "058",
		SettlementAccountNumber: "1234567890",
		SettlementAccountName:   "Acme Corp Ltd",
		MakerWalletAddress:      "0xDeAdBeEf",
		RequestedPerTradeCapUSD: 500,
		RequestedDailyCapUSD:    10000,
	}
}

// ── fakeNombaProvisioner (VA-only) ─────────────────────────────────────────────

type fakeNombaProvisioner struct {
	// Controls VA-create behaviour.
	createVirtualAccountFn func(ctx context.Context, accountRef, accountName string) (string, error)

	// Records so tests can assert idempotency + the accountRef passed.
	createVirtualAccountCalls int
	lastAccountRef            string
	lastAccountName           string
}

func (f *fakeNombaProvisioner) CreateVirtualAccount(ctx context.Context, accountRef, accountName string) (string, error) {
	f.createVirtualAccountCalls++
	f.lastAccountRef = accountRef
	f.lastAccountName = accountName
	if f.createVirtualAccountFn != nil {
		return f.createVirtualAccountFn(ctx, accountRef, accountName)
	}
	return "0012345678", nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestProvisionAndActivate_HappyPath verifies the full VA-only success path:
// row ends active, VA number + accountRef stored, CreateVirtualAccount called
// exactly once with the deterministic accountRef and business name.
func TestProvisionAndActivate_HappyPath(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	fake := &fakeNombaProvisioner{}
	svc := NewPMOnboardingService(db, fake)

	userID := uuid.New()
	req := fakeMerchantRequest(userID)

	pm, err := svc.ProvisionAndActivate(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionAndActivate error: %v", err)
	}

	// Row should be active.
	if pm.Status != models.PaymentMerchantStatusActive {
		t.Errorf("status = %q, want %q", pm.Status, models.PaymentMerchantStatusActive)
	}
	if pm.ProvisioningStatus != models.PMProvisioningActive {
		t.Errorf("provisioning_status = %q, want %q", pm.ProvisioningStatus, models.PMProvisioningActive)
	}

	// VA fields stored: accountRef is the match key, inbound is the VA number.
	if pm.NombaAccountRef != pmAccountRef(userID) {
		t.Errorf("NombaAccountRef = %q, want %q", pm.NombaAccountRef, pmAccountRef(userID))
	}
	if pm.InboundAccountNumber == "" {
		t.Error("InboundAccountNumber (VA number) is empty")
	}
	// Sub-account id must NOT be set under the VA-only model.
	if pm.NombaSubAccountID != "" {
		t.Errorf("NombaSubAccountID = %q, want empty (VA-only model never sets it)", pm.NombaSubAccountID)
	}

	// Settlement bank copied from request.
	if pm.SettlementBankCode != req.SettlementBankCode {
		t.Errorf("SettlementBankCode = %q, want %q", pm.SettlementBankCode, req.SettlementBankCode)
	}
	if pm.SettlementAccountNumber != req.SettlementAccountNumber {
		t.Errorf("SettlementAccountNumber = %q, want %q", pm.SettlementAccountNumber, req.SettlementAccountNumber)
	}

	// Operator wallet copied.
	if pm.OperatorWalletAddress != req.MakerWalletAddress {
		t.Errorf("OperatorWalletAddress = %q, want %q", pm.OperatorWalletAddress, req.MakerWalletAddress)
	}

	// Caps copied.
	if pm.PerTradeCapUSD != req.RequestedPerTradeCapUSD {
		t.Errorf("PerTradeCapUSD = %v, want %v", pm.PerTradeCapUSD, req.RequestedPerTradeCapUSD)
	}
	if pm.DailyCapUSD != req.RequestedDailyCapUSD {
		t.Errorf("DailyCapUSD = %v, want %v", pm.DailyCapUSD, req.RequestedDailyCapUSD)
	}

	// UserID set from request.
	if pm.UserID != userID {
		t.Errorf("UserID = %v, want %v", pm.UserID, userID)
	}

	// CreateVirtualAccount called exactly once with the deterministic accountRef
	// and the business name.
	if fake.createVirtualAccountCalls != 1 {
		t.Errorf("CreateVirtualAccount called %d times, want 1", fake.createVirtualAccountCalls)
	}
	if fake.lastAccountRef != pmAccountRef(userID) {
		t.Errorf("CreateVirtualAccount accountRef = %q, want %q", fake.lastAccountRef, pmAccountRef(userID))
	}
	if fake.lastAccountName != req.BusinessName {
		t.Errorf("CreateVirtualAccount accountName = %q, want %q", fake.lastAccountName, req.BusinessName)
	}

	// Exactly one row in DB.
	var count int64
	if err := db.Model(&models.PaymentMerchant{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 payment_merchants row, got %d", count)
	}
}

// TestProvisionAndActivate_Resume verifies the idempotency / resume logic:
// first attempt fails at CreateVirtualAccount → row pending, no accountRef.
// Second attempt creates the VA → active. The VA create runs only on the second
// attempt (no double-create) because the first never persisted an accountRef.
func TestProvisionAndActivate_Resume(t *testing.T) {
	db := newSQLiteForOnboarding(t)

	vaErrOnFirst := true
	vaErr := errors.New("va network timeout")

	fake := &fakeNombaProvisioner{
		createVirtualAccountFn: func(ctx context.Context, accountRef, accountName string) (string, error) {
			if vaErrOnFirst {
				vaErrOnFirst = false
				return "", vaErr
			}
			return "0098765432", nil
		},
	}
	svc := NewPMOnboardingService(db, fake)

	userID := uuid.New()
	req := fakeMerchantRequest(userID)

	// Attempt 1 — VA creation fails (before any number returned → nothing persisted).
	_, err := svc.ProvisionAndActivate(context.Background(), req)
	if err == nil {
		t.Fatal("attempt 1: expected error, got nil")
	}
	if !errors.Is(err, vaErr) {
		t.Fatalf("attempt 1: expected vaErr, got %v", err)
	}

	// After attempt 1: row exists, not active, no accountRef (VA create failed).
	var pm models.PaymentMerchant
	if err := db.First(&pm, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load pm after attempt 1: %v", err)
	}
	if pm.Status == models.PaymentMerchantStatusActive {
		t.Error("after attempt 1: status should not be active")
	}
	if pm.NombaAccountRef != "" {
		t.Error("after attempt 1: NombaAccountRef should be empty (VA create failed)")
	}
	if fake.createVirtualAccountCalls != 1 {
		t.Errorf("after attempt 1: CreateVirtualAccount called %d times, want 1", fake.createVirtualAccountCalls)
	}

	// Attempt 2 — same request; VA now succeeds.
	pm2, err := svc.ProvisionAndActivate(context.Background(), req)
	if err != nil {
		t.Fatalf("attempt 2: unexpected error: %v", err)
	}
	if pm2.Status != models.PaymentMerchantStatusActive {
		t.Errorf("attempt 2: status = %q, want %q", pm2.Status, models.PaymentMerchantStatusActive)
	}
	if pm2.NombaAccountRef != pmAccountRef(userID) {
		t.Errorf("attempt 2: NombaAccountRef = %q, want %q", pm2.NombaAccountRef, pmAccountRef(userID))
	}
	if pm2.InboundAccountNumber == "" {
		t.Error("attempt 2: InboundAccountNumber is empty")
	}
	// VA create ran exactly twice total (once-failed, once-succeeded). The third
	// time below must NOT re-create.
	if fake.createVirtualAccountCalls != 2 {
		t.Errorf("attempt 2: CreateVirtualAccount called %d times total, want 2", fake.createVirtualAccountCalls)
	}

	// Attempt 3 — already provisioned: must NOT re-create the VA (resume skips it).
	pm3, err := svc.ProvisionAndActivate(context.Background(), req)
	if err != nil {
		t.Fatalf("attempt 3: unexpected error: %v", err)
	}
	if pm3.NombaAccountRef != pm2.NombaAccountRef {
		t.Errorf("attempt 3: NombaAccountRef changed: got %q, want %q", pm3.NombaAccountRef, pm2.NombaAccountRef)
	}
	if fake.createVirtualAccountCalls != 2 {
		t.Errorf("attempt 3: CreateVirtualAccount called %d times total, want still 2 (resume skips re-create)", fake.createVirtualAccountCalls)
	}

	// Exactly one row in DB.
	var count int64
	if err := db.Model(&models.PaymentMerchant{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 payment_merchants row, got %d", count)
	}
}

// TestProvisionAndActivate_NilProvisioner verifies that when PMOnboardingService
// is constructed with a nil provisioner, ProvisionAndActivate returns a clear error.
func TestProvisionAndActivate_NilProvisioner(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)

	userID := uuid.New()
	req := fakeMerchantRequest(userID)

	_, err := svc.ProvisionAndActivate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nil provisioner, got nil")
	}
}
