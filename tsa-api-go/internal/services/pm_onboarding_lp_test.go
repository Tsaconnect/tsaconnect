package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

type fakePoolOpen bool

func (f fakePoolOpen) Open() bool { return bool(f) }

func fakeLPRequest(userID uuid.UUID) *models.MerchantRequest {
	r := fakeMerchantRequest(userID)
	r.BusinessType = models.BusinessTypeLiquidityProvider
	r.BusinessName = "ADA OBI"
	return r
}

func TestApproveFromRequest_LPRowHasKindPoolChainAndNoCaps(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, nil, nil)
	userID := uuid.New()

	pm, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
	if err != nil {
		t.Fatalf("ApproveFromRequest: %v", err)
	}
	var raw models.PaymentMerchant
	db.First(&raw, "id = ?", pm.ID)
	if !raw.IsLP() || raw.PoolChainID == nil || *raw.PoolChainID != 97 || raw.Status != models.PaymentMerchantStatusApproved ||
		raw.PerTradeCapUSD != 0 || raw.DailyCapUSD != 0 || raw.OperatorWalletAddress != "0xDeAdBeEf" {
		t.Fatalf("LP row = %+v", raw)
	}
}

func TestApproveFromRequest_LPNeedsAPoolChain(t *testing.T) {
	svc := NewPMOnboardingService(newSQLiteForOnboarding(t), nil) // no SetLiquidity: pool dormant
	if _, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(uuid.New())); !errors.Is(err, ErrLiquidityUnavailable) {
		t.Fatalf("err = %v, want ErrLiquidityUnavailable", err)
	}
}

// Owner 2026-10-13: one user may hold both roles, each in its own row.
func TestApproveFromRequest_MerchantThenLPMakesTwoRowsLeavingTheMerchantAlone(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, nil, nil)
	userID := uuid.New()
	m, err := svc.ApproveFromRequest(context.Background(), fakeMerchantRequest(userID))
	if err != nil {
		t.Fatal(err)
	}
	db.Model(m).Update("status", models.PaymentMerchantStatusActive)
	lp, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
	if err != nil || !lp.IsLP() || lp.ID == m.ID {
		t.Fatalf("lp = %+v err = %v", lp, err)
	}
	var after models.PaymentMerchant
	db.First(&after, "id = ?", m.ID)
	if after.Status != models.PaymentMerchantStatusActive || after.SettlementAccountName != m.SettlementAccountName || after.IsLP() {
		t.Fatalf("merchant row changed by the LP approval: %+v", after)
	}
	// Re-approving the LP request reuses the LP row, never the merchant row.
	again, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
	if err != nil || again.ID != lp.ID {
		t.Fatalf("re-approve = %v %v", again, err)
	}
}

// Review I2: a rejected LP who re-applies is approved again, not left rejected.
func TestApproveFromRequest_RejectedLPReapprovesToApproved(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, nil, nil)
	userID := uuid.New()
	pm, _ := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
	db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).Updates(map[string]any{"status": models.PaymentMerchantStatusRejected, "review_note": "blurry"})
	again, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
	if err != nil || again.ID != pm.ID || again.Status != models.PaymentMerchantStatusApproved {
		t.Fatalf("re-approve = %+v %v", again, err)
	}
}

// Security review of 6b250c31: the merchant activation must never take an LP
// live, in either inbound mode. It skips the LP's own checks (S34 name match,
// pool gate), and in VA mode it would mint a virtual account that makes the LP
// row attributable to merchant inbound credits.
func TestActivateWithKYC_NeverActivatesAnLPRow(t *testing.T) {
	for _, retired := range []bool{false, true} {
		db := newSQLiteForOnboarding(t)
		prov := &fakeNombaProvisioner{}
		svc := NewPMOnboardingService(db, prov)
		svc.SetVirtualAccountsRetired(retired)
		svc.SetLiquidity(97, nil, fakePoolOpen(true))
		userID := uuid.New()
		seedKYCUser(t, db, userID, models.VerificationStatusVerified)
		pm, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "BVN", "12345678901"); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("retired=%v: err = %v, want ErrInvalidPMTransition", retired, err)
		}
		var raw models.PaymentMerchant
		db.First(&raw, "id = ?", pm.ID)
		if raw.Status != models.PaymentMerchantStatusApproved || raw.NombaAccountRef != "" || prov.createVirtualAccountCalls != 0 {
			t.Fatalf("retired=%v: row=%+v vaCalls=%d: an LP went live as a merchant", retired, raw, prov.createVirtualAccountCalls)
		}
	}
}

// Security review of 6b250c31: an LP is admitted only through its application
// (pool configured, still verified, wallet unique). The by-id merchant approval
// must not move a pending LP row past those checks.
func TestApprovePM_RefusesAnLPRow(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	chain := int64(97)
	lp := models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "ADA OBI", Kind: models.PaymentMerchantKindLP,
		PoolChainID: &chain, Status: models.PaymentMerchantStatusPending, ProvisioningStatus: models.PMProvisioningPending}
	if err := db.Create(&lp).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePM(context.Background(), lp.ID, uuid.New()); !errors.Is(err, ErrInvalidPMTransition) {
		t.Fatalf("err = %v, want ErrInvalidPMTransition", err)
	}
	var raw models.PaymentMerchant
	db.First(&raw, "id = ?", lp.ID)
	if raw.Status != models.PaymentMerchantStatusPending {
		t.Fatalf("status = %s: the merchant approval admitted an LP", raw.Status)
	}
}

type fakeAccounts struct {
	name          string
	err           error
	calls         int
	bank, account string // the last Resolve call
}

func (f *fakeAccounts) Resolve(_ context.Context, bank, account string) (NombaLookupResult, error) {
	f.calls++
	f.bank, f.account = bank, account
	return NombaLookupResult{AccountName: f.name}, f.err
}

// Owner 2026-10-13: a dual user's LP activation checks the LP's own settlement
// account (bank lookup and S34 name match), never the merchant's, and leaves
// the merchant row alone. A guard: ActivateLP already resolves by row id.
func TestActivateLP_DualUserUsesOwnSettlementBankNotMerchants(t *testing.T) {
	svc, acc, userID, lp := lpOnboarding(t, "ADA OBI", true)
	m := &models.PaymentMerchant{ID: uuid.New(), UserID: userID, Name: "Ada Foods Ltd", Kind: models.PaymentMerchantKindMerchant,
		Status: models.PaymentMerchantStatusActive, SettlementBankCode: "044", SettlementAccountNumber: "9999999999",
		SettlementAccountName: "ADA FOODS LTD"}
	svc.db.Create(m)
	out, err := svc.ActivateLP(context.Background(), lp.ID, userID) // KYC is the user's, already verified
	if err != nil || out.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("activate = %+v %v", out, err)
	}
	if acc.account != lp.SettlementAccountNumber || acc.bank != lp.SettlementBankCode || acc.account == m.SettlementAccountNumber {
		t.Fatalf("resolved %s/%s, want the LP's %s/%s", acc.bank, acc.account, lp.SettlementBankCode, lp.SettlementAccountNumber)
	}
	var after models.PaymentMerchant
	svc.db.First(&after, "id = ?", m.ID)
	if after.SettlementAccountName != "ADA FOODS LTD" || after.SettlementAccountNumber != "9999999999" || after.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("merchant row touched by LP activation: %+v", after)
	}
}

// approvedLP: an approved LP for a Didit-verified user (legal name ADA OBI).
func approvedLP(t *testing.T, svc *PMOnboardingService, userID uuid.UUID) *models.PaymentMerchant {
	t.Helper()
	pm, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID))
	if err != nil {
		t.Fatalf("approve LP: %v", err)
	}
	return pm
}

func lpOnboarding(t *testing.T, bankName string, open bool) (*PMOnboardingService, *fakeAccounts, uuid.UUID, *models.PaymentMerchant) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil) // VA mode, no provisioner: an LP needs neither
	acc := &fakeAccounts{name: bankName}
	svc.SetLiquidity(97, acc, fakePoolOpen(open))
	userID := uuid.New()
	seedKYCUser(t, db, userID, models.VerificationStatusVerified)
	return svc, acc, userID, approvedLP(t, svc, userID)
}

// Roadmap risk 3 (S34).
func TestActivateLP_RequiresSettlementNameMatch(t *testing.T) {
	svc, _, userID, pm := lpOnboarding(t, "CHUKWU EMEKA", true)
	if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); !errors.Is(err, ErrSettlementNameMismatch) {
		t.Fatalf("err = %v, want ErrSettlementNameMismatch", err)
	}
	var raw models.PaymentMerchant
	svc.db.First(&raw, "id = ?", pm.ID)
	if raw.Status != models.PaymentMerchantStatusApproved {
		t.Fatalf("status = %s: a mismatched LP went live", raw.Status)
	}
}

// The applicant typed "ADA OBI"; the bank's own name is what is matched and stored.
func TestActivateLP_UsesTheServerResolvedAccountName(t *testing.T) {
	svc, acc, userID, pm := lpOnboarding(t, "OBI ADA", true)
	out, err := svc.ActivateLP(context.Background(), pm.ID, userID)
	if err != nil {
		t.Fatalf("ActivateLP: %v", err)
	}
	if out.Status != models.PaymentMerchantStatusActive || acc.calls != 1 {
		t.Fatalf("status=%s resolves=%d", out.Status, acc.calls)
	}
	var raw models.PaymentMerchant
	svc.db.First(&raw, "id = ?", pm.ID)
	if raw.SettlementAccountName != "OBI ADA" || raw.InboundAccountNumber != "" || raw.NombaAccountRef != "" {
		t.Fatalf("row = %+v: stores the bank's name and mints no account", raw)
	}
	if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); err != nil || acc.calls != 1 {
		t.Fatalf("re-activation must be an idempotent no-op: %v calls=%d", err, acc.calls)
	}
}

// Security fix of 36c4a991: provisioning_status=active is what closes the
// merchant-request retry path (isPMRetry). Left pending, the old LP request
// stays re-approvable and ApproveFromRequest would overwrite the verified
// settlement name with the applicant-typed one.
func TestActivateLP_MarksProvisioningActive(t *testing.T) {
	svc, _, userID, pm := lpOnboarding(t, "OBI ADA", true)
	if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); err != nil {
		t.Fatalf("ActivateLP: %v", err)
	}
	var raw models.PaymentMerchant
	svc.db.First(&raw, "id = ?", pm.ID)
	if raw.ProvisioningStatus != models.PMProvisioningActive {
		t.Fatalf("provisioning_status = %q, want active", raw.ProvisioningStatus)
	}
}

func TestActivateLP_RefusedWhilePoolClosed(t *testing.T) {
	svc, acc, userID, pm := lpOnboarding(t, "ADA OBI", false)
	if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); !errors.Is(err, ErrLiquidityUnavailable) || acc.calls != 0 {
		t.Fatalf("err=%v resolves=%d", err, acc.calls)
	}
}

// O-B5: an LP approved onto one pool chain cannot go live once the pool chain
// is unconfigured, or is now a different chain than the row's.
func TestActivateLP_RefusedWhilePoolChainUnconfigured(t *testing.T) {
	for _, chain := range []int64{0, 56} {
		svc, acc, userID, pm := lpOnboarding(t, "ADA OBI", true)
		svc.SetLiquidity(chain, acc, fakePoolOpen(true))
		if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); !errors.Is(err, ErrLiquidityUnavailable) || acc.calls != 0 {
			t.Fatalf("pool chain %d: err=%v resolves=%d", chain, err, acc.calls)
		}
	}
}

func TestActivateLP_NeedsAVerifiedLegalName(t *testing.T) {
	for _, tc := range []struct {
		name, status, legal string
		want                error
	}{
		{"unverified", models.VerificationStatusRejected, "ADA OBI", ErrKYCVerificationRequired},
		{"no legal name", models.VerificationStatusVerified, "", ErrKYCLegalNameMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, acc, userID, pm := lpOnboarding(t, "ADA OBI", true)
			svc.db.Exec(`UPDATE users SET verification_status = ?, verified_legal_name = ? WHERE id = ?`, tc.status, tc.legal, userID)
			_, err := svc.ActivateLP(context.Background(), pm.ID, userID)
			if KYCRefusalCode(err) != KYCRefusalCode(tc.want) || acc.calls != 0 {
				t.Fatalf("err=%v resolves=%d, want %v", err, acc.calls, tc.want)
			}
		})
	}
}

func TestActivateLP_UnresolvableAccountIsRedacted(t *testing.T) {
	svc, acc, userID, pm := lpOnboarding(t, "", true)
	acc.err = errors.New(`Get "https://api.flutterwave.com/v3/accounts/resolve?key=sk_live_123": timeout`)
	_, err := svc.ActivateLP(context.Background(), pm.ID, userID)
	if !errors.Is(err, ErrSettlementAccountUnresolved) || strings.Contains(err.Error(), "sk_live_123") {
		t.Fatalf("err = %v", err)
	}
}

func TestActivateLP_RefusesAnotherUsersOrAMerchantRow(t *testing.T) {
	svc, acc, _, pm := lpOnboarding(t, "ADA OBI", true)
	if _, err := svc.ActivateLP(context.Background(), pm.ID, uuid.New()); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("other user: err = %v, want ErrPMNotFound", err)
	}
	merchantUser := uuid.New()
	seedKYCUser(t, svc.db, merchantUser, models.VerificationStatusVerified)
	merchant, err := svc.ApproveFromRequest(context.Background(), fakeMerchantRequest(merchantUser))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateLP(context.Background(), merchant.ID, merchantUser); !errors.Is(err, ErrInvalidPMTransition) {
		t.Fatalf("merchant row: err = %v, want ErrInvalidPMTransition", err)
	}
	if acc.calls != 0 {
		t.Fatalf("resolves = %d, want 0", acc.calls)
	}
}

func TestActivateWithKYC_RefusesLPRows(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetVirtualAccountsRetired(true)
	svc.SetLiquidity(97, &fakeAccounts{name: "ADA OBI"}, fakePoolOpen(true))
	userID := uuid.New()
	seedKYCUser(t, db, userID, models.VerificationStatusVerified)
	pm := approvedLP(t, svc, userID)
	if _, err := svc.ActivateWithKYC(context.Background(), pm.ID, userID, "", ""); !errors.Is(err, ErrInvalidPMTransition) {
		t.Fatalf("err = %v: the merchant path must never activate an LP (it skips S34)", err)
	}
}

// Defence in depth for 36c4a991: even reached directly, the approval never
// re-copies the request over a live LP's bank-verified settlement name.
func TestApproveFromRequest_RefusesALiveLP(t *testing.T) {
	for _, status := range []string{models.PaymentMerchantStatusActive, models.PaymentMerchantStatusPaused} {
		svc, _, userID, pm := lpOnboarding(t, "OBI ADA", true)
		if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); err != nil {
			t.Fatalf("ActivateLP: %v", err)
		}
		svc.db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).Update("status", status)
		if _, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID)); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("%s: err = %v, want ErrInvalidPMTransition", status, err)
		}
		var raw models.PaymentMerchant
		svc.db.First(&raw, "id = ?", pm.ID)
		if raw.SettlementAccountName != "OBI ADA" || raw.Status != status {
			t.Fatalf("%s: row = %+v: the approval overwrote a live LP", status, raw)
		}
	}
}

// hookAccounts runs hook inside Resolve: the window between ActivateLP's read
// of the row and its guarded write.
type hookAccounts struct {
	name string
	hook func()
}

func (a hookAccounts) Resolve(context.Context, string, string) (NombaLookupResult, error) {
	a.hook()
	return NombaLookupResult{AccountName: a.name}, nil
}

// assertActivationGuard: a row paused, or whose settlement account changed,
// while the bank was being asked is neither taken live nor given the name.
func assertActivationGuard(t *testing.T, newLP func() (*PMOnboardingService, *models.PaymentMerchant, uuid.UUID)) {
	t.Helper()
	for name, change := range map[string]map[string]any{
		"paused":          {"status": models.PaymentMerchantStatusPaused},
		"account changed": {"settlement_account_number": "9999999999"},
	} {
		svc, pm, userID := newLP()
		svc.accounts = hookAccounts{name: "OBI ADA", hook: func() {
			if err := svc.db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).Updates(change).Error; err != nil {
				t.Fatal(err)
			}
		}}
		if _, err := svc.ActivateLP(context.Background(), pm.ID, userID); !errors.Is(err, ErrInvalidPMTransition) {
			t.Fatalf("%s: err = %v, want ErrInvalidPMTransition", name, err)
		}
		var raw models.PaymentMerchant
		svc.db.First(&raw, "id = ?", pm.ID)
		if raw.Status == models.PaymentMerchantStatusActive || raw.SettlementAccountName == "OBI ADA" || raw.ProvisioningStatus == models.PMProvisioningActive {
			t.Fatalf("%s: row = %+v: the activation overwrote a row that changed under it", name, raw)
		}
	}
}

func TestActivateLP_GuardedWrite(t *testing.T) {
	assertActivationGuard(t, func() (*PMOnboardingService, *models.PaymentMerchant, uuid.UUID) {
		svc, _, userID, pm := lpOnboarding(t, "OBI ADA", true)
		return svc, pm, userID
	})
}

// ActivateLP on PostgreSQL: the bank's name and provisioning_status=active are
// persisted by the one guarded UPDATE, and the guard holds there too.
func TestActivateLP_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "pm_lp_activate_", &models.User{}, &models.PaymentMerchant{}, &models.MerchantRequest{})
	ctx := context.Background()
	newLP := func() (*PMOnboardingService, *models.PaymentMerchant, uuid.UUID) {
		user := &models.User{ID: uuid.New(), Email: "lp+" + uuid.New().String()[:8] + "@example.com", Name: "Ada Obi", Username: "lp" + uuid.New().String()[:8],
			VerificationStatus: models.VerificationStatusVerified, VerifiedLegalName: "ADA OBI"}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
		svc := NewPMOnboardingService(db, nil)
		svc.SetLiquidity(97, hookAccounts{name: "OBI ADA", hook: func() {}}, fakePoolOpen(true))
		req := fakeLPRequest(user.ID)
		req.MakerWalletAddress = "0x" + strings.Repeat("0", 32) + user.ID.String()[:8]
		pm, err := svc.ApproveFromRequest(ctx, req)
		if err != nil {
			t.Fatalf("approve LP: %v", err)
		}
		return svc, pm, user.ID
	}

	svc, pm, userID := newLP()
	if _, err := svc.ActivateLP(ctx, pm.ID, userID); err != nil {
		t.Fatalf("ActivateLP: %v", err)
	}
	var raw models.PaymentMerchant
	if err := db.First(&raw, "id = ?", pm.ID).Error; err != nil {
		t.Fatal(err)
	}
	if raw.Status != models.PaymentMerchantStatusActive || raw.ProvisioningStatus != models.PMProvisioningActive || raw.SettlementAccountName != "OBI ADA" {
		t.Fatalf("row = %+v", raw)
	}
	assertActivationGuard(t, newLP)

	// Two concurrent activations of one LP: both answer active, one write lands.
	svc3, pm3, userID3 := newLP()
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := svc3.ActivateLP(ctx, pm3.ID, userID3); errs <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent ActivateLP: %v", err)
		}
	}
	var raw3 models.PaymentMerchant
	db.First(&raw3, "id = ?", pm3.ID)
	if raw3.Status != models.PaymentMerchantStatusActive || raw3.SettlementAccountName != "OBI ADA" {
		t.Fatalf("row = %+v", raw3)
	}
}

// Review N2/N3: an approval takes the applicant's user-row lock (FOR NO KEY
// UPDATE) before it writes, so a second approval for the same user waits for
// the first to commit. Holding that lock elsewhere must hold the approval back.
func TestApproveFromRequest_WaitsOnTheUserRowLock_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "pm_lp_lock_", &models.User{}, &models.PaymentMerchant{}, &models.MerchantRequest{})
	user := &models.User{ID: uuid.New(), Email: "lock@example.com", Name: "Ada Obi", Username: "lock" + uuid.New().String()[:8]}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, nil, nil)

	holder := db.Begin()
	defer holder.Rollback()
	if err := holder.Exec(`SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE`, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(user.ID)); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("approval finished while the user row was locked (err=%v)", err)
	case <-time.After(400 * time.Millisecond):
	}
	holder.Rollback()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("approval after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval still blocked after the lock was released")
	}

	// NO KEY UPDATE, not UPDATE: a row merely referencing the user (FOR KEY
	// SHARE, as any foreign-key insert takes) must not stall an approval.
	other := &models.User{ID: uuid.New(), Email: "share@example.com", Name: "Ada Obi", Username: "share" + uuid.New().String()[:8]}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	sharer := db.Begin()
	defer sharer.Rollback()
	if err := sharer.Exec(`SELECT id FROM users WHERE id = ? FOR KEY SHARE`, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	req := fakeLPRequest(other.ID)
	req.MakerWalletAddress = "0x00000000000000000000000000000000000000e2"
	go func() { _, err := svc.ApproveFromRequest(context.Background(), req); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("approval beside a key-share lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a key-share lock on the user row stalled the approval")
	}
}

// companyRow seeds IT_PM_ID's merchant row for userID and pins it on svc.
func companyRow(t *testing.T, svc *PMOnboardingService, userID uuid.UUID) {
	t.Helper()
	pm := &models.PaymentMerchant{ID: uuid.New(), UserID: userID, Name: "TSA", Status: models.PaymentMerchantStatusActive}
	if err := svc.db.Create(pm).Error; err != nil {
		t.Fatal(err)
	}
	svc.SetCompanyPMID(pm.ID.String())
}

// Review M5: the company PM's user (the slice buyer) is never an LP, so the
// pool can never sit on both sides of a slice.
func TestApproveFromRequest_CompanyUserCannotBeLP(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, nil, nil)
	userID := uuid.New()
	companyRow(t, svc, userID)
	if _, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID)); !errors.Is(err, ErrCompanyUserCannotBeLP) {
		t.Fatalf("err = %v, want ErrCompanyUserCannotBeLP", err)
	}
	var n int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ? AND kind = ?", userID, models.PaymentMerchantKindLP).Count(&n)
	if n != 0 {
		t.Fatalf("LP rows = %d, want none for the company user", n)
	}
	// Another user is unaffected.
	if _, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(uuid.New())); err != nil {
		t.Fatalf("other user's LP approval: %v", err)
	}
}

func TestActivateLP_RefusesTheCompanyUser(t *testing.T) {
	svc, acc, userID, lp := lpOnboarding(t, "ADA OBI", true)
	companyRow(t, svc, userID) // IT_PM_ID repointed after the LP was approved
	if _, err := svc.ActivateLP(context.Background(), lp.ID, userID); !errors.Is(err, ErrCompanyUserCannotBeLP) || acc.calls != 0 {
		t.Fatalf("err=%v resolves=%d, want ErrCompanyUserCannotBeLP before any bank call", err, acc.calls)
	}
	var raw models.PaymentMerchant
	svc.db.First(&raw, "id = ?", lp.ID)
	if raw.Status != models.PaymentMerchantStatusApproved {
		t.Fatalf("status = %s: the company user went live as an LP", raw.Status)
	}
}
