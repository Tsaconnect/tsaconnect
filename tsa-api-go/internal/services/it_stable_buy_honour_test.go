package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// openRoutedForSettle opens the Flutterwave worked example (₦165,025 →
// 94.5375 USDC at ₦1745.61) and returns it waiting for payment.
func openRoutedForSettle(t *testing.T) (*ITService, uuid.UUID, *models.ITIntent) {
	t.Helper()
	s, userID := newRoutedBuyService(t)
	intent, _, err := s.OpenRoutedStableBuy(context.Background(), userID, routedTestQuote(t, s), "POLYGON")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, userID, intent
}

func watchRateUpdates(t *testing.T, s *ITService) chan events.Event {
	t.Helper()
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	told := make(chan events.Event, 4)
	bus.Subscribe(events.RampRateUpdated, func(e events.Event) { told <- e })
	s.SetEventBus(bus)
	return told
}

func expectTold(t *testing.T, told chan events.Event, userID uuid.UUID, units *big.Int) {
	t.Helper()
	select {
	case e := <-told:
		want := "Rate updated: you'll receive " + FormatStableUnits(units.String()) + " USDC"
		if e.UserID != userID || !strings.Contains(e.Message, want) {
			t.Fatalf("event = %+v, want %q", e, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the user was not told the rate changed")
	}
}

func centralCredit(ref string, kobo int64, arrived time.Time) *models.InboundCredit {
	return &models.InboundCredit{ProviderRef: ref, Currency: "NGN", AmountKobo: kobo, GrossKobo: kobo,
		TransactionDate: arrived, SeenAt: time.Now()}
}

func TestRoutedBuy_OnTimeExactPaymentIsHonoured(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1800}) // the market moved; she still gets what she was shown
	logs := captureLog(t)
	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_on_time", "16502500")
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	got := reloadIntent(t, s, intent.ID)
	if got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "94537500" || got.LockedRate != "174561" ||
		got.Repriced || StableBuyDisplayRate(&got) != "169257" {
		t.Fatalf("status=%s asset=%s rate=%s display=%s repriced=%v", got.Status, got.AssetAmount, got.LockedRate, got.DisplayRate, got.Repriced)
	}
	out := logs.String()
	if !strings.Contains(out, "ramp_fw_honoured intent="+intent.ID.String()) ||
		strings.Contains(out, "drift_bps=unknown") || strings.Contains(out, "shortfall_units=unknown") {
		t.Fatalf("log = %q", out)
	}
}

// Owner 2026-10-04 #1: any other amount is priced live, and she is told; the
// stored breakdown follows the live quote (§14, Decision D41).
func TestRoutedBuy_OffAmountInsideTheWindowIsPricedLive(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	told := watchRateUpdates(t, s)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1800})
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_half", "8251250"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	want, err := s.routedQuoteAt("USDC", "NGN", big.NewInt(8_251_250), big.NewRat(1800, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	got := reloadIntent(t, s, intent.ID)
	if got.AssetAmount != want.AssetUnits.String() || got.LockedRate != want.RatePerUnitMinor.String() || !got.Repriced ||
		got.DisplayRate != want.DisplayRateMinor.String() || len(StableBuyFees(&got)) != 2 ||
		StableBuyFees(&got)[0].Units != want.Fees[0].Units {
		t.Fatalf("intent = %+v, want the live quote %+v", got, want)
	}
	expectTold(t, told, userID, want.AssetUnits)
}

func TestRoutedBuy_LargeOverpaymentIsPricedLiveWithNoMaximum(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_big", "500000000"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	got := reloadIntent(t, s, intent.ID)
	units, _ := new(big.Int).SetString(got.AssetAmount, 10)
	if got.Status != models.ITStatusPaymentReceived || !got.Repriced || units.Cmp(big.NewInt(2_000_000_000)) <= 0 {
		t.Fatalf("status=%s asset=%s, want priced live above the 2000 cap", got.Status, got.AssetAmount)
	}
}

// D32: money that can never price parks on BOTH paths.
func TestRoutedBuy_BelowTheMinimumParksOnBothPaths(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	logs := captureLog(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_small", "800000"); err != nil {
		t.Fatalf("VA settle: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate || !strings.Contains(got.FailureReason, "minimum") {
		t.Fatalf("VA: status=%s reason=%q", got.Status, got.FailureReason)
	}
	if !strings.Contains(logs.String(), "ALERT it_stable_buy_parked_unpriceable") {
		t.Fatalf("log = %q", logs.String())
	}
	s2, _, intent2 := openRoutedForSettle(t)
	if err := NewITStableClaimSource(s2).Accept(context.Background(), intent2.ID,
		centralCredit("100004261003120000000000000099", 800_000, time.Now())); err != nil {
		t.Fatalf("central: %v, want a park, not a retry", err)
	}
	if got := reloadIntent(t, s2, intent2.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("central: status = %s", got.Status)
	}
}

func TestRoutedBuy_CentralRateOutageStillRetries(t *testing.T) {
	s, _, intent := openRoutedForSettle(t)
	s.SetRateQuoter(&buyRateQuoter{err: errors.New("provider down")})
	err := NewITStableClaimSource(s).Accept(context.Background(), intent.ID,
		centralCredit("100004261003120000000000000098", 900_000_000, time.Now()))
	if err == nil {
		t.Fatal("a transient outage must return an error so the poller retries")
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %s, want untouched", got.Status)
	}
}

func TestRoutedBuy_LatePaymentIsRepricedAndTheUserIsTold(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	told := watchRateUpdates(t, s)
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("rate_expires_at", time.Now().Add(-time.Minute))
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1800})
	logs := captureLog(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_late", "16502500"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	want, _ := s.routedQuoteAt("USDC", "NGN", big.NewInt(16_502_500), big.NewRat(1800, 1), false)
	got := reloadIntent(t, s, intent.ID)
	if got.AssetAmount != want.AssetUnits.String() || !StableBuyRateUpdated(&got) ||
		!strings.Contains(logs.String(), "ramp_fw_repriced intent="+intent.ID.String()) {
		t.Fatalf("asset=%s rate-updated=%v log=%q", got.AssetAmount, StableBuyRateUpdated(&got), logs.String())
	}
	expectTold(t, told, userID, want.AssetUnits)
}

// Review Focus 3 / D33: arrived at minute 14, processed at minute 16 → honoured.
func TestRoutedBuy_ArrivedOnTimeProcessedInsideTheGraceIsHonoured(t *testing.T) {
	s, _, intent := openRoutedForSettle(t)
	expiry := time.Now().Add(-2 * time.Minute)
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("rate_expires_at", expiry)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1800})
	if err := NewITStableClaimSource(s).Accept(context.Background(), intent.ID,
		centralCredit("100004261003120000000000000097", 16_502_500, expiry.Add(-time.Minute))); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.AssetAmount != "94537500" || got.Repriced {
		t.Fatalf("asset=%s repriced=%v, want honoured", got.AssetAmount, got.Repriced)
	}
}

// Review Focus 3 / S8: on time, but processed after the 10-minute grace → live.
func TestRoutedBuy_ProcessedAfterTheGraceIsPricedLive(t *testing.T) {
	s, _, intent := openRoutedForSettle(t)
	expiry := time.Now().Add(-11 * time.Minute)
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("rate_expires_at", expiry)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1800})
	if err := NewITStableClaimSource(s).Accept(context.Background(), intent.ID,
		centralCredit("100004261003120000000000000096", 16_502_500, expiry.Add(-time.Minute))); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.AssetAmount == "94537500" || !got.Repriced {
		t.Fatalf("asset=%s repriced=%v, want priced live", got.AssetAmount, got.Repriced)
	}
}

func TestCreditArrival_UsesATimedTransactionDateElseSeenAt(t *testing.T) {
	seen := time.Date(2026, 10, 4, 9, 30, 0, 0, time.UTC)
	if got := creditArrival(&models.InboundCredit{TransactionDate: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), SeenAt: seen}, time.Now()); !got.Equal(seen) {
		t.Fatalf("date-only arrival = %v, want SeenAt", got)
	}
	timed := time.Date(2026, 10, 4, 9, 14, 0, 0, time.UTC)
	if got := creditArrival(&models.InboundCredit{TransactionDate: timed, SeenAt: seen}, time.Now()); !got.Equal(timed) {
		t.Fatalf("timed arrival = %v", got)
	}
	// Review minor: a statement time after we saw the credit is a zone error
	// (it carries none and is parsed as UTC) — clamp to SeenAt, never later.
	late := time.Date(2026, 10, 4, 10, 14, 0, 0, time.UTC)
	if got := creditArrival(&models.InboundCredit{TransactionDate: late, SeenAt: seen}, time.Now()); !got.Equal(seen) {
		t.Fatalf("arrival after SeenAt = %v, want SeenAt %v", got, seen)
	}
	now := time.Now()
	if got := creditArrival(nil, now); !got.Equal(now) {
		t.Fatal("nil credit")
	}
}

// D2: the watcher must not re-price an honoured buy.
func TestRoutedBuy_ConvertDoesNotRepriceAnHonouredIntent(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_conv", "16502500"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1800})
	s.SetStableConverter(&stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 200000}},
		transfer: FWTransfer{ID: "trf_honoured", Status: "NEW"},
	}, "merchant_1")
	if err := s.ConvertStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusConverting || got.AssetAmount != "94537500" || got.Repriced {
		t.Fatalf("status=%s asset=%s repriced=%v", got.Status, got.AssetAmount, got.Repriced)
	}
}

// Review Focus 3 / D2: a redelivered webhook, through the entry point
// Flutterwave calls, leaves the honoured claim alone even with the rate source down.
func TestRoutedBuy_WebhookRedeliveryOfAnHonouredChargeDoesNotReprice(t *testing.T) {
	s, user, va := newVATestService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	wireStableBuyPrereqs(s)
	if err := s.db.AutoMigrate(&models.WalletAddress{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	acceptITAgreement(t, s, user.ID)
	ctx := context.Background()
	intent, _, err := s.OpenRoutedStableBuy(ctx, user.ID, routedTestQuote(t, s), "POLYGON")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, "chg_va_honour", "165025.00"); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	s.SetRateQuoter(&buyRateQuoter{err: errors.New("provider down")})
	if err := s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, "chg_va_honour", "165025.00"); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if replayed, claimed, err := s.finishStableBuyReplay(ctx, "chg_va_honour", false); err != nil || !replayed || !claimed {
		t.Fatalf("poller replay: replayed=%v claimed=%v err=%v", replayed, claimed, err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "94537500" || got.Repriced {
		t.Fatalf("status=%s asset=%s repriced=%v", got.Status, got.AssetAmount, got.Repriced)
	}
}

// D3: an operator re-price of a routed Flutterwave buy uses the routed terms.
// ₦33,005 → 20 gross → 19.7075… → 17.7075 delivered.
func TestRepriceParkedStableBuy_RoutedUsesTheRoutedTerms(t *testing.T) {
	s, _, intent := openRoutedForSettle(t)
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Updates(map[string]any{
		"status": models.ITStatusBuyAwaitingRate, "payment_ref": "chg_parked", "received_amount": "3300500",
	})
	got, err := s.RepriceParkedStableBuy(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("reprice: %v", err)
	}
	if got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "17707500" || !got.Repriced {
		t.Fatalf("status=%s asset=%s repriced=%v", got.Status, got.AssetAmount, got.Repriced)
	}
}

// An honoured Flutterwave buy waiting on the float is not re-priced.
func TestRepriceParkedStableBuy_RefusesAnHonouredBuyShortOfFloat(t *testing.T) {
	s, userID, intent := openRoutedForSettle(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_float", "16502500"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusDelivering)
	if _, err := s.RepriceParkedStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

func deliveringRoutedIntent(t *testing.T, balance float64) (*ITService, *models.ITIntent, *stubDeliverer) {
	t.Helper()
	s, userID, intent := openRoutedForSettle(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_deliver", "16502500"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusDelivering)
	s.SetStableConverter(&stubConverter{balances: []FWBalance{{Currency: "USDC", AvailableBalance: balance}}}, "merchant_1")
	d := &stubDeliverer{id: "trf_out"}
	s.SetStableDeliverer(d)
	return s, intent, d
}

// D31: the gate holds amount + send fee (94.5375 + 1.50).
func TestDeliverStableBuy_RoutedGateHoldsAmountPlusFee(t *testing.T) {
	s, intent, d := deliveringRoutedIntent(t, 96)
	if err := s.DeliverStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrStableFloatShort) || d.calls != 0 {
		t.Fatalf("err = %v calls = %d, want float short before sending", err, d.calls)
	}
	s.SetStableConverter(&stubConverter{balances: []FWBalance{{Currency: "USDC", AvailableBalance: 96.0375}}}, "merchant_1")
	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := d.got.PaymentInstruction.Amount.Value; got != 94.5375 {
		t.Fatalf("sent %v, want exactly AssetAmount (wallet mode)", got)
	}
}

func TestDeliverStableBuy_AmountModeSendsTheFeeOnTop(t *testing.T) {
	s, intent, d := deliveringRoutedIntent(t, 96.0375)
	if err := s.SetRoutedStableBuyLimits(RoutedStableBuyLimits{SendFeeMode: SendFeeFromAmount}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := d.got.PaymentInstruction.Amount.Value; got != 96.0375 {
		t.Fatalf("sent %v, want 96.0375 (94.5375 + the 1.50 fee)", got)
	}
}

// Controller ruling (Task 1 carry): a 0302 park taken on the tenth attempt
// cannot convert again, so re-pricing it would only stall the watcher on
// ErrStableConversionExhausted. Refuse it here, where the operator sees why.
func TestRepriceParkedStableBuy_RefusesAParkWithNoConversionAttemptsLeft(t *testing.T) {
	s, _, intent := openRoutedForSettle(t)
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Updates(map[string]any{
		"status": models.ITStatusBuyAwaitingRate, "payment_ref": "chg_spent", "received_amount": "3300500",
		"conversion_attempts": maxStableConversionAttempts,
	})
	_, err := s.RepriceParkedStableBuy(context.Background(), intent.ID)
	if !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "conversion attempts") {
		t.Fatalf("err = %v, want a clear bad-state refusal", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s, want it left parked", got.Status)
	}
}
