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
		MakerWalletAddress:      "0xDeAdBeEf",
		RequestedPerTradeCapUSD: 500,
		RequestedDailyCapUSD:    10000,
	}
}

// ── fakeNombaProvisioner ──────────────────────────────────────────────────────

type fakeNombaProvisioner struct {
	// Controls behaviour per call type.
	createSubAccountFn          func(ctx context.Context, name string) (string, error)
	createVirtualAccountFn      func(ctx context.Context, subAccountID, accountName string) (string, string, error)
	setSubAccountSettlementFn   func(ctx context.Context, subAccountID, bankCode, accountNumber string) error

	// Call counters so tests can assert idempotency.
	createSubAccountCalls        int
	createVirtualAccountCalls    int
	setSettlementCalls           int
}

func (f *fakeNombaProvisioner) CreateSubAccount(ctx context.Context, name string) (string, error) {
	f.createSubAccountCalls++
	if f.createSubAccountFn != nil {
		return f.createSubAccountFn(ctx, name)
	}
	return "sub-acct-001", nil
}

func (f *fakeNombaProvisioner) CreateVirtualAccountForSubAccount(ctx context.Context, subAccountID, accountName string) (string, string, error) {
	f.createVirtualAccountCalls++
	if f.createVirtualAccountFn != nil {
		return f.createVirtualAccountFn(ctx, subAccountID, accountName)
	}
	return "0012345678", "wallet-001", nil
}

func (f *fakeNombaProvisioner) SetSubAccountSettlement(ctx context.Context, subAccountID, bankCode, accountNumber string) error {
	f.setSettlementCalls++
	if f.setSubAccountSettlementFn != nil {
		return f.setSubAccountSettlementFn(ctx, subAccountID, bankCode, accountNumber)
	}
	return nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestProvisionAndActivate_HappyPath verifies the full success path:
// row ends active, all Nomba IDs stored, each Nomba method called exactly once.
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

	// Nomba IDs must be stored.
	if pm.NombaSubAccountID == "" {
		t.Error("NombaSubAccountID is empty")
	}
	if pm.InboundAccountNumber == "" {
		t.Error("InboundAccountNumber is empty")
	}
	if pm.NombaWalletID == "" {
		t.Error("NombaWalletID is empty")
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

	// Each Nomba method called exactly once.
	if fake.createSubAccountCalls != 1 {
		t.Errorf("CreateSubAccount called %d times, want 1", fake.createSubAccountCalls)
	}
	if fake.createVirtualAccountCalls != 1 {
		t.Errorf("CreateVirtualAccount called %d times, want 1", fake.createVirtualAccountCalls)
	}
	if fake.setSettlementCalls != 1 {
		t.Errorf("SetSettlement called %d times, want 1", fake.setSettlementCalls)
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
// first attempt fails at CreateVirtualAccount → sub-account is stored, row pending.
// Second attempt does NOT re-create the sub-account, completes VA + settlement → active.
func TestProvisionAndActivate_Resume(t *testing.T) {
	db := newSQLiteForOnboarding(t)

	vaErrOnFirst := true
	vaErr := errors.New("va network timeout")

	fake := &fakeNombaProvisioner{
		createVirtualAccountFn: func(ctx context.Context, subAccountID, accountName string) (string, string, error) {
			if vaErrOnFirst {
				vaErrOnFirst = false
				return "", "", vaErr
			}
			return "0098765432", "wallet-resume", nil
		},
	}
	svc := NewPMOnboardingService(db, fake)

	userID := uuid.New()
	req := fakeMerchantRequest(userID)

	// Attempt 1 — VA creation fails.
	_, err := svc.ProvisionAndActivate(context.Background(), req)
	if err == nil {
		t.Fatal("attempt 1: expected error, got nil")
	}
	if !errors.Is(err, vaErr) {
		t.Fatalf("attempt 1: expected vaErr, got %v", err)
	}

	// After attempt 1: sub-account stored, row still pending/not-active.
	var pm models.PaymentMerchant
	if err := db.First(&pm, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load pm after attempt 1: %v", err)
	}
	if pm.NombaSubAccountID == "" {
		t.Error("after attempt 1: NombaSubAccountID should be stored (idempotency anchor)")
	}
	if pm.Status == models.PaymentMerchantStatusActive {
		t.Error("after attempt 1: status should not be active")
	}
	if pm.ProvisioningStatus == models.PMProvisioningActive {
		t.Error("after attempt 1: provisioning_status should not be active")
	}
	subAcctAfterAttempt1 := pm.NombaSubAccountID

	// CreateSubAccount must have been called exactly once.
	if fake.createSubAccountCalls != 1 {
		t.Errorf("after attempt 1: CreateSubAccount called %d times, want 1", fake.createSubAccountCalls)
	}

	// Attempt 2 — same request; VA now succeeds.
	pm2, err := svc.ProvisionAndActivate(context.Background(), req)
	if err != nil {
		t.Fatalf("attempt 2: unexpected error: %v", err)
	}

	// Row is now active.
	if pm2.Status != models.PaymentMerchantStatusActive {
		t.Errorf("attempt 2: status = %q, want %q", pm2.Status, models.PaymentMerchantStatusActive)
	}
	if pm2.ProvisioningStatus != models.PMProvisioningActive {
		t.Errorf("attempt 2: provisioning_status = %q, want %q", pm2.ProvisioningStatus, models.PMProvisioningActive)
	}

	// CreateSubAccount must NOT have been called again (total = 1).
	if fake.createSubAccountCalls != 1 {
		t.Errorf("attempt 2: CreateSubAccount called %d times total, want exactly 1 (no re-create)", fake.createSubAccountCalls)
	}

	// VA stored.
	if pm2.InboundAccountNumber == "" {
		t.Error("attempt 2: InboundAccountNumber is empty")
	}
	if pm2.NombaWalletID == "" {
		t.Error("attempt 2: NombaWalletID is empty")
	}

	// Sub-account ID unchanged from attempt 1.
	if pm2.NombaSubAccountID != subAcctAfterAttempt1 {
		t.Errorf("attempt 2: NombaSubAccountID changed: got %q, want %q", pm2.NombaSubAccountID, subAcctAfterAttempt1)
	}

	// Exactly one row in DB (upsert, not duplicate insert).
	var count int64
	if err := db.Model(&models.PaymentMerchant{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 payment_merchants row, got %d", count)
	}
}

// TestProvisionAndActivate_GatesOnAllSteps verifies that activation (status=active)
// ONLY happens when all four steps complete. We simulate settlement failing — the
// row must not be active even though sub-account + VA succeeded.
func TestProvisionAndActivate_GatesOnAllSteps(t *testing.T) {
	db := newSQLiteForOnboarding(t)

	settlementErr := errors.New("settlement bank unavailable")
	fake := &fakeNombaProvisioner{
		setSubAccountSettlementFn: func(ctx context.Context, subAccountID, bankCode, accountNumber string) error {
			return settlementErr
		},
	}
	svc := NewPMOnboardingService(db, fake)

	userID := uuid.New()
	req := fakeMerchantRequest(userID)

	_, err := svc.ProvisionAndActivate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when settlement fails, got nil")
	}

	// Row must NOT be active.
	var pm models.PaymentMerchant
	if err := db.First(&pm, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load pm after settlement failure: %v", err)
	}
	if pm.Status == models.PaymentMerchantStatusActive {
		t.Error("status must not be active when settlement fails")
	}
	if pm.ProvisioningStatus == models.PMProvisioningActive {
		t.Error("provisioning_status must not be active when settlement fails")
	}

	// But sub-account + VA were stored (persisted before the failure).
	if pm.NombaSubAccountID == "" {
		t.Error("NombaSubAccountID should be stored even after settlement failure")
	}
	if pm.InboundAccountNumber == "" {
		t.Error("InboundAccountNumber should be stored even after settlement failure")
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
