package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestSettleStableBuyFromCharge_FundsAnOpenIntent(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_1", "16502500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("charge did not fund the open intent")
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q, want payment_received", got.Status)
	}
	if got.PaymentRef != "chg_1" {
		t.Fatalf("payment ref = %q, want the charge id", got.PaymentRef)
	}
	if got.ReceivedAmount != "16502500" {
		t.Fatalf("received = %q, want what actually landed", got.ReceivedAmount)
	}
}

func TestSettleStableBuyFromCharge_NoOpenIntentFallsThrough(t *testing.T) {
	// A user with no open stablecoin buy must reach today's MCGP path
	// unchanged. Returning true here would silently swallow their payment.
	s, userID := newStableBuyDBService(t)

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_2", "500000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Fatal("claimed a charge with no open stablecoin intent")
	}
}

func TestSettleStableBuyFromCharge_RecordsWhatLandedNotWhatWasQuoted(t *testing.T) {
	// Underpayment is priced on what arrived, mirroring the sell leg's
	// received/quoted scaling. Crediting the quoted amount would deliver
	// stablecoin the user did not pay for.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_3", "8251250"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.ReceivedAmount != "8251250" {
		t.Fatalf("received = %q, want the underpaid amount", got.ReceivedAmount)
	}
}

func TestSettleStableBuyFromCharge_PricesAtTheLiveRateNotTheQuote(t *testing.T) {
	// Spec §4: the binding price is struck when the naira lands. If the rate
	// moved after the user tapped Buy they get the live rate — carrying the
	// quoted asset amount forward would be a rate lock by another name.
	s, userID := newStableBuyDBService(t)
	// Widen the plausibility band for THIS test only. The fixture's fx feed is
	// 1500 and the default band is ±25%, so the doubled rate below would be
	// refused as implausible — the guard working correctly, but it would mask
	// what this test is actually about. The guard has its own test at the
	// default band (TestQuoteStableBuy_RejectsImplausibleRate).
	s.SetStableRateToleranceBps(12500)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	// The rate doubles before the money arrives: the same naira must now buy
	// roughly half the stablecoin.
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 3300.50})

	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_live", "16502500"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.AssetAmount == testBuyQuote().AssetUnits.String() {
		t.Fatal("delivered the amount quoted at creation; the rate was locked in effect")
	}
	if got.AssetAmount != "49225000" { // ~49.225 USDC after the 155bps spread
		t.Fatalf("asset amount = %q, want the live-rate figure", got.AssetAmount)
	}
}

func TestSettleStableBuyFromCharge_ParksWhenItCannotPrice(t *testing.T) {
	// The naira has landed and cannot be handed back, so an unusable rate must
	// park for an operator rather than error the webhook or price on a bad feed.
	s, userID := newStableBuyDBService(t)
	s.SetRateQuoter(&buyRateQuoter{err: errors.New("provider down")})
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_norate", "16502500")
	if err != nil {
		t.Fatalf("webhook errored on landed money: %v", err)
	}
	if !claimed {
		t.Fatal("charge fell through to MCGP; the user bought stablecoin")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %q, want buy_awaiting_rate", got.Status)
	}
	if got.ReceivedAmount != "16502500" {
		t.Fatalf("received = %q, want what landed recorded even when unpriced", got.ReceivedAmount)
	}
}

func TestSettleStableBuyFromCharge_ParksWhenUnderpaidBelowTheFloor(t *testing.T) {
	// Below Flutterwave's outbound floor the delivery would be refused with the
	// user's naira already taken, so park instead of advancing.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_dust", "165025"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %q, want buy_awaiting_rate (parked)", got.Status)
	}
}

func TestSettleStableBuyFromCharge_IsIdempotent(t *testing.T) {
	// Webhook redelivery must not double-settle.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	for i := 0; i < 2; i++ {
		if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_4", "16502500"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Where("payment_ref = ?", "chg_4").Count(&n)
	if n != 1 {
		t.Fatalf("%d intents claim the charge, want 1", n)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q after redelivery, want payment_received", got.Status)
	}
}

func TestRepriceAndCreditAwaitingRate_RefusesStablecoinBuys(t *testing.T) {
	// A parked stablecoin buy has Direction=buy AND Status=awaiting_rate, so it
	// satisfies the MCGP recovery's guard. Without an asset check an operator
	// recovering it would credit MCGP from COMPANY SUPPLY — wrong asset to the
	// user, company funds spent, and the stablecoin purchase still unfulfilled.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Update("status", models.ITStatusBuyAwaitingRate)

	// newStableBuyDBService wires a nil swap (it never needs one for the
	// stablecoin leg), so IsConfigured() is false and RepriceAndCreditAwaitingRate
	// would return ErrITNotConfigured before ever reaching the guard under
	// test. Configure one here, same pattern as newITTestService, so the
	// asset check is what this test actually exercises.
	swap := NewMCGPSwapService(nil, &config.Config{Env: "development", MCGPSwapBAddress: "0x00000000000000000000000000000000000bEEF0"})
	swap.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", hash: "0xdead"})
	s.swap = swap

	_, err := s.RepriceAndCreditAwaitingRate(context.Background(), intent.ID)
	if !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState — the MCGP path must refuse a stablecoin intent", err)
	}
}

func TestRepriceParkedStableBuy_ResumesAParkedPurchase(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Updates(map[string]any{
		"status":          models.ITStatusBuyAwaitingRate,
		"received_amount": "16502500",
	})

	got, err := s.RepriceParkedStableBuy(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q, want payment_received so the watcher picks it up", got.Status)
	}
	if got.AssetAmount != "98450000" {
		t.Fatalf("asset amount = %q, want the freshly priced figure", got.AssetAmount)
	}
}

func TestRepriceParkedStableBuy_RefusesNonParkedOrNonStable(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	// Still pending_payment — nothing has landed, so there is nothing to reprice.
	if _, err := s.RepriceParkedStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

func TestSettleStableBuyFromCharge_RedeliveryCompletesUnfinishedPricing(t *testing.T) {
	// A crash between claiming the charge and pricing it would otherwise leave
	// the creation-time amount in place — the rate lock the design removed,
	// reappearing in a crash window. Redelivery must finish the job.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	// Simulate the crash: claimed, but never priced.
	s.db.Model(intent).Updates(map[string]any{
		"status":          models.ITStatusPaymentReceived,
		"payment_ref":     "chg_crash",
		"received_amount": "16502500",
		"repriced":        false,
	})

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_crash", "16502500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("redelivery did not claim the charge")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if !got.Repriced {
		t.Fatal("redelivery left the intent unpriced")
	}
	if got.AssetAmount != "98450000" {
		t.Fatalf("asset amount = %q, want the priced figure", got.AssetAmount)
	}
}


func TestSettleStableBuyFromCharge_DoesNotClaimAnAbandonedIntent(t *testing.T) {
	// C2: nothing else expires or cancels a stablecoin buy, so an abandoned
	// intent must stop claiming money on its own once it is old enough that
	// it can no longer plausibly be the bank transfer the user is completing
	// — otherwise it hijacks a later, unrelated payment from the same user.
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	old := time.Now().Add(-stableBuyClaimWindow - time.Hour)
	if err := s.db.Model(intent).UpdateColumn("created_at", old).Error; err != nil {
		t.Fatalf("age intent: %v", err)
	}

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_stale", "16502500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Fatal("claimed a charge against an intent past the claim window")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, want the abandoned intent left untouched", got.Status)
	}
}

func TestSettleStableBuyFromCharge_ClaimsWithinTheWindow(t *testing.T) {
	// The other side of the same guard: a legitimately slow (but plausible)
	// bank transfer must still be claimable.
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	recent := time.Now().Add(-stableBuyClaimWindow + time.Hour)
	if err := s.db.Model(intent).UpdateColumn("created_at", recent).Error; err != nil {
		t.Fatalf("age intent: %v", err)
	}

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_still_open", "16502500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("did not claim a charge against an intent still inside the claim window")
	}
}

func TestCancelStableBuy_CancelsAnOpenPurchase(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.CancelStableBuy(context.Background(), userID, intent.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}

	// The self-service exit ErrStableBuyAlreadyOpen otherwise wedges a user
	// behind: a cancelled intent must not block a new one.
	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("cancelled intent still blocked a new purchase: %v", err)
	}
}

func TestCancelStableBuy_RefusesAnotherUsersIntent(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.CancelStableBuy(context.Background(), uuid.New(), intent.ID); !errors.Is(err, ErrStableIntentNotFound) {
		t.Fatalf("err = %v, want ErrStableIntentNotFound", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, another user's cancel must not touch it", got.Status)
	}
}

func TestCancelStableBuy_RefusesOnceMoneyHasLanded(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_landed", "16502500"); err != nil {
		t.Fatalf("settle: %v", err)
	}

	if err := s.CancelStableBuy(context.Background(), userID, intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState — money already landed, this must not cancel it", err)
	}
}
