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
