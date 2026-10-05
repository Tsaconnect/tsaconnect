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
	createVirtualAccountFn func(ctx context.Context, accountRef, accountName, idType, idNumber string) (string, error)

	// Records so tests can assert idempotency + the args passed.
	createVirtualAccountCalls int
	lastAccountRef            string
	lastAccountName           string
	lastIDType                string
	lastIDNumber              string
}

func (f *fakeNombaProvisioner) CreateVirtualAccount(ctx context.Context, accountRef, accountName, idType, idNumber string) (string, error) {
	f.createVirtualAccountCalls++
	f.lastAccountRef = accountRef
	f.lastAccountName = accountName
	f.lastIDType = idType
	f.lastIDNumber = idNumber
	if f.createVirtualAccountFn != nil {
		return f.createVirtualAccountFn(ctx, accountRef, accountName, idType, idNumber)
	}
	return "0012345678", nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestApproveFromRequest_HappyPath verifies approval copies settlement/caps and
// lands the PM in 'approved' WITHOUT creating a virtual account — VA creation is
// deferred to merchant KYC activation.
func TestApproveFromRequest_HappyPath(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	fake := &fakeNombaProvisioner{}
	svc := NewPMOnboardingService(db, fake)

	userID := uuid.New()
	req := fakeMerchantRequest(userID)

	pm, err := svc.ApproveFromRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ApproveFromRequest error: %v", err)
	}

	// Row should be approved, not active — and NO VA yet.
	if pm.Status != models.PaymentMerchantStatusApproved {
		t.Errorf("status = %q, want %q", pm.Status, models.PaymentMerchantStatusApproved)
	}
	if pm.NombaAccountRef != "" {
		t.Errorf("NombaAccountRef = %q, want empty (VA deferred to activation)", pm.NombaAccountRef)
	}
	if pm.InboundAccountNumber != "" {
		t.Errorf("InboundAccountNumber = %q, want empty (VA deferred)", pm.InboundAccountNumber)
	}
	if fake.createVirtualAccountCalls != 0 {
		t.Errorf("CreateVirtualAccount called %d times, want 0 (approval never provisions)", fake.createVirtualAccountCalls)
	}

	// Settlement bank / wallet / caps copied from request.
	if pm.SettlementBankCode != req.SettlementBankCode {
		t.Errorf("SettlementBankCode = %q, want %q", pm.SettlementBankCode, req.SettlementBankCode)
	}
	if pm.OperatorWalletAddress != req.MakerWalletAddress {
		t.Errorf("OperatorWalletAddress = %q, want %q", pm.OperatorWalletAddress, req.MakerWalletAddress)
	}
	if pm.PerTradeCapUSD != req.RequestedPerTradeCapUSD {
		t.Errorf("PerTradeCapUSD = %v, want %v", pm.PerTradeCapUSD, req.RequestedPerTradeCapUSD)
	}
	if pm.UserID != userID {
		t.Errorf("UserID = %v, want %v", pm.UserID, userID)
	}
}

// TestApproveFromRequest_NilProvisionerOK verifies approval does NOT require a
// fiat provider — it is a pure state transition; only activation needs one.
func TestApproveFromRequest_NilProvisionerOK(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)

	req := fakeMerchantRequest(uuid.New())
	pm, err := svc.ApproveFromRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ApproveFromRequest with nil provisioner should succeed: %v", err)
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		t.Errorf("status = %q, want approved", pm.Status)
	}
}

// approvedPM sets up an approved PM (via ApproveFromRequest) ready for activation.
func approvedPM(t *testing.T, svc *PMOnboardingService, userID uuid.UUID) *models.PaymentMerchant {
	t.Helper()
	pm, err := svc.ApproveFromRequest(context.Background(), fakeMerchantRequest(userID))
	if err != nil {
		t.Fatalf("approvedPM setup: %v", err)
	}
	return pm
}

// TestActivateWithKYC_HappyPath verifies the merchant-triggered activation:
// approved → active, VA created once with the forwarded BVN, and the id number
// is NOT persisted anywhere on the row.
func TestActivateWithKYC_HappyPath(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	fake := &fakeNombaProvisioner{}
	svc := NewPMOnboardingService(db, fake)

	userID := uuid.New()
	pm := approvedPM(t, svc, userID)

	const bvn = "22233344455"
	out, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "BVN", bvn)
	if err != nil {
		t.Fatalf("ActivateWithKYC error: %v", err)
	}

	if out.Status != models.PaymentMerchantStatusActive {
		t.Errorf("status = %q, want active", out.Status)
	}
	if out.ProvisioningStatus != models.PMProvisioningActive {
		t.Errorf("provisioning_status = %q, want active", out.ProvisioningStatus)
	}
	if out.NombaAccountRef != pmAccountRef(userID) {
		t.Errorf("NombaAccountRef = %q, want %q", out.NombaAccountRef, pmAccountRef(userID))
	}
	if out.InboundAccountNumber == "" {
		t.Error("InboundAccountNumber (VA number) is empty")
	}

	// VA created exactly once, with the BVN forwarded to the provider.
	if fake.createVirtualAccountCalls != 1 {
		t.Errorf("CreateVirtualAccount called %d times, want 1", fake.createVirtualAccountCalls)
	}
	if fake.lastIDType != "BVN" || fake.lastIDNumber != bvn {
		t.Errorf("forwarded id = (%q,%q), want (BVN,%q)", fake.lastIDType, fake.lastIDNumber, bvn)
	}

	// CRITICAL: the id number must never be persisted on the PM row.
	var raw models.PaymentMerchant
	if err := db.First(&raw, "id = ?", pm.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	for field, val := range map[string]string{
		"InboundAccountNumber": raw.InboundAccountNumber,
		"NombaAccountRef":      raw.NombaAccountRef,
		"Name":                 raw.Name,
		"ReviewNote":           raw.ReviewNote,
	} {
		if val == bvn {
			t.Errorf("BVN leaked into persisted field %s", field)
		}
	}
}

// TestActivateWithKYC_Resume verifies the resume/idempotency logic: a first
// activation that fails at the VA call persists nothing, and a retry creates the
// VA exactly once; a third call skips re-creation.
func TestActivateWithKYC_Resume(t *testing.T) {
	db := newSQLiteForOnboarding(t)

	failFirst := true
	vaErr := errors.New("va network timeout")
	fake := &fakeNombaProvisioner{
		createVirtualAccountFn: func(ctx context.Context, accountRef, accountName, idType, idNumber string) (string, error) {
			if failFirst {
				failFirst = false
				return "", vaErr
			}
			return "0098765432", nil
		},
	}
	svc := NewPMOnboardingService(db, fake)
	userID := uuid.New()
	pm := approvedPM(t, svc, userID)

	// Attempt 1 — VA fails, nothing persisted, still approved.
	if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "BVN", "22233344455"); !errors.Is(err, vaErr) {
		t.Fatalf("attempt 1: want vaErr, got %v", err)
	}
	var after1 models.PaymentMerchant
	db.First(&after1, "id = ?", pm.ID)
	if after1.Status == models.PaymentMerchantStatusActive || after1.NombaAccountRef != "" {
		t.Errorf("attempt 1 must not activate or store a ref: status=%q ref=%q", after1.Status, after1.NombaAccountRef)
	}

	// Attempt 2 — succeeds.
	if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "BVN", "22233344455"); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	// Attempt 3 — already active: idempotent, no re-create.
	if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "BVN", "22233344455"); err != nil {
		t.Fatalf("attempt 3: %v", err)
	}
	if fake.createVirtualAccountCalls != 2 {
		t.Errorf("CreateVirtualAccount called %d times total, want 2 (fail, succeed, then skip)", fake.createVirtualAccountCalls)
	}
}

// TestActivateWithKYC_OwnershipMismatch verifies a user cannot activate another
// user's PM — it returns ErrPMNotFound (no existence leak) and creates no VA.
func TestActivateWithKYC_OwnershipMismatch(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	fake := &fakeNombaProvisioner{}
	svc := NewPMOnboardingService(db, fake)

	owner := uuid.New()
	pm := approvedPM(t, svc, owner)

	_, err := svc.ActivateWithKYC(context.Background(), pm.ID, uuid.New(), "BVN", "22233344455")
	if !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("want ErrPMNotFound for foreign user, got %v", err)
	}
	if fake.createVirtualAccountCalls != 0 {
		t.Errorf("CreateVirtualAccount called %d times, want 0", fake.createVirtualAccountCalls)
	}
}

// TestActivateWithKYC_WrongStatus verifies activation is rejected on a still-pending
// PM (not yet admin-approved).
func TestActivateWithKYC_WrongStatus(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, &fakeNombaProvisioner{})

	userID := uuid.New()
	pm := &models.PaymentMerchant{ID: uuid.New(), UserID: userID, Name: "Acme", Status: models.PaymentMerchantStatusPending}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "BVN", "22233344455"); !errors.Is(err, ErrInvalidPMTransition) {
		t.Fatalf("want ErrInvalidPMTransition for pending PM, got %v", err)
	}
}

// TestActivateWithKYC_NilProvisioner verifies activation surfaces a clear error
// when no fiat provider is configured.
func TestActivateWithKYC_NilProvisioner(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)

	_, err := svc.ActivateWithKYC(context.Background(), uuid.New(), uuid.New(), "BVN", "22233344455")
	if !errors.Is(err, ErrPMProvisioningUnavailable) {
		t.Fatalf("want ErrPMProvisioningUnavailable, got %v", err)
	}
}

// ── Central inbound: no virtual account ─────────────────────────────────────
//
// Once P2P buyers are routed to the company account, a merchant's virtual
// account is never shown to anyone. Minting one would still ask the merchant
// for a BVN/NIN purely to create an account nothing reads.

func TestActivateWithKYC_VirtualAccountsRetiredActivatesWithoutAnID(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	fake := &fakeNombaProvisioner{}
	svc := NewPMOnboardingService(db, fake)
	svc.SetVirtualAccountsRetired(true)

	userID := uuid.New()
	pm := approvedPM(t, svc, userID)

	out, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "", "")
	if err != nil {
		t.Fatalf("ActivateWithKYC: %v", err)
	}
	if out.Status != models.PaymentMerchantStatusActive {
		t.Errorf("status = %q, want active", out.Status)
	}
	if fake.createVirtualAccountCalls != 0 {
		t.Errorf("CreateVirtualAccount called %d times, want 0 — nothing routes buyers to it", fake.createVirtualAccountCalls)
	}
	var raw models.PaymentMerchant
	db.First(&raw, "id = ?", pm.ID)
	if raw.Status != models.PaymentMerchantStatusActive || raw.InboundAccountNumber != "" {
		t.Errorf("persisted status=%q inbound=%q, want active with no account", raw.Status, raw.InboundAccountNumber)
	}
}

func TestActivateWithKYC_VirtualAccountsRetiredNeedsNoProvisioner(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetVirtualAccountsRetired(true)

	userID := uuid.New()
	pm := approvedPM(t, svc, userID)

	if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "", ""); err != nil {
		t.Fatalf("ActivateWithKYC: %v — with no account to create, no provider is needed", err)
	}
}

func TestNeedsIdentity_FollowsVirtualAccountRetirement(t *testing.T) {
	svc := NewPMOnboardingService(newSQLiteForOnboarding(t), &fakeNombaProvisioner{})
	if !svc.NeedsIdentity() {
		t.Error("NeedsIdentity = false by default, want true — VA mode mints an account from the BVN/NIN")
	}
	svc.SetVirtualAccountsRetired(true)
	if svc.NeedsIdentity() {
		t.Error("NeedsIdentity = true with virtual accounts retired, want false")
	}
}
