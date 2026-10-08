package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
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

// Roadmap risk 4: never both a merchant and an LP.
func TestApproveFromRequest_KindConflictRefused(t *testing.T) {
	db := newSQLiteForOnboarding(t)
	svc := NewPMOnboardingService(db, nil)
	svc.SetLiquidity(97, nil, nil)
	userID := uuid.New()
	if _, err := svc.ApproveFromRequest(context.Background(), fakeMerchantRequest(userID)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveFromRequest(context.Background(), fakeLPRequest(userID)); !errors.Is(err, ErrPMKindConflict) {
		t.Fatalf("err = %v, want ErrPMKindConflict", err)
	}
	var n int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", userID).Count(&n)
	if n != 1 {
		t.Fatalf("%d rows for the user, want 1", n)
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
