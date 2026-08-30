package services

import (
	"context"
	"errors"
	"testing"

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

