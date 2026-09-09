package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Past buyOrderClaimWindow an MCGP order can no longer claim a payment, but
// nothing used to close it: OpenBuyOrder only supersedes when the user opens
// ANOTHER MCGP buy, there is no user-facing cancel for this leg, and no job
// swept it. The order therefore sat in the hub reading "awaiting transfer"
// forever — a row the system had already decided to ignore.
func TestExpireStaleBuyOrders_CancelsAnUnpaidOrderPastTheWindow(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	abandoned, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open buy order: %v", err)
	}
	ageOrder(t, s, abandoned.ID, buyOrderClaimWindow+time.Hour)

	n, err := s.ExpireStaleBuyOrders(ctx)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 1 {
		t.Fatalf("expired %d orders, want 1", n)
	}

	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", abandoned.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.ITStatusCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
	if got.FailureReason == "" {
		t.Error("no failure reason recorded — the hub has nothing to explain the cancellation with")
	}
}

// The window is the same one AutoCreditFromVA matches against, so anything the
// settle path would still honour must survive the sweep. Cancelling an order a
// transfer is on its way to would strand that payment in the fall-through path
// and split one purchase across two rows.
func TestExpireStaleBuyOrders_LeavesAnOrderStillInsideTheWindow(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	fresh, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open buy order: %v", err)
	}
	ageOrder(t, s, fresh.ID, buyOrderClaimWindow-time.Hour)

	if n, err := s.ExpireStaleBuyOrders(ctx); err != nil || n != 0 {
		t.Fatalf("expired %d orders (err %v), want 0 — the settle path still honours this one", n, err)
	}
}

// The decisive guard. A row whose payment_ref is a PROVIDER charge id has real
// money attributed to it and is mid-settlement; only rows still carrying the
// ref this service minted at creation are unpaid. Sweeping on age alone would
// cancel an order a charge had just landed against, and the recovery path for
// that ("superseded in the window between reading and claiming") exists but
// costs a re-settle nobody needed.
func TestExpireStaleBuyOrders_LeavesAnOrderAChargeIsAttributedTo(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	claimed, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open buy order: %v", err)
	}
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", claimed.ID).
		Update("payment_ref", "chg_money_is_landing").Error; err != nil {
		t.Fatalf("attribute charge: %v", err)
	}
	ageOrder(t, s, claimed.ID, buyOrderClaimWindow+time.Hour)

	if n, err := s.ExpireStaleBuyOrders(ctx); err != nil || n != 0 {
		t.Fatalf("expired %d orders (err %v), want 0 — a charge is attributed to it", n, err)
	}
}

// Stablecoin purchases have their own lifecycle: a user-facing cancel endpoint,
// a resume view, and the one-open-purchase rule that makes a stale row visible
// immediately. Sweeping them here would cancel orders from underneath that
// flow, so this leg is deliberately MCGP-only.
func TestExpireStaleBuyOrders_LeavesStablecoinPurchasesAlone(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	stable := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, Direction: models.ITDirectionBuy,
		Status: models.ITStatusPendingPayment, Asset: models.ITAssetUSDC,
		Currency: "NGN", PaymentRef: mintedRefPrefix + "STABLEROW00001",
		McgpAmount: "0", NgnKobo: "200000", RateKobo: "0",
	}
	if err := s.db.Create(stable).Error; err != nil {
		t.Fatalf("seed stablecoin order: %v", err)
	}
	ageOrder(t, s, stable.ID, buyOrderClaimWindow+time.Hour)

	if n, err := s.ExpireStaleBuyOrders(ctx); err != nil || n != 0 {
		t.Fatalf("expired %d orders (err %v), want 0 — stablecoin buys own their own lifecycle", n, err)
	}
}

func ageOrder(t *testing.T, s *ITService, id uuid.UUID, by time.Duration) {
	t.Helper()
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", id).
		Update("created_at", time.Now().Add(-by)).Error; err != nil {
		t.Fatalf("age order: %v", err)
	}
}
