package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Review Focus 2 / P4: exact and on time, still never priced (the claim parks it).
func TestPriceRoutedArrival_UnknownProviderNeverHonoured(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusPendingPayment)
	a, err := s.priceRoutedArrival(context.Background(), intent, intent.AmountMinor, time.Now(), PayInCentral)
	if !errors.Is(err, ErrUnknownITProvider) || a != nil {
		t.Fatalf("arrival = %+v err = %v, want ErrUnknownITProvider and nothing priced", a, err)
	}
}

func TestPriceRoutedArrival_KnownProvidersUnchanged(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	for _, p := range []string{models.ITProviderTreasury, models.ITProviderFlutterwave, ""} {
		intent := seedProviderIntent(t, s, userID, p, models.ITStatusPendingPayment)
		a, err := s.priceRoutedArrival(context.Background(), intent, intent.AmountMinor, time.Now(), PayInCentral)
		if err != nil || a == nil || !a.honoured || a.units.String() != "7278150" {
			t.Fatalf("provider %q: arrival = %+v err = %v, want honoured at the locked units", p, a, err)
		}
	}
}

// Review C1 / P4: on the central account (parkOnBadRate=false) an unknown
// provider must PARK, not return an error the matcher retries every 30 s.
func TestClaim_UnknownProviderParksForOperator_Central(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusPendingPayment)
	logs := captureLog(t)
	claimed, err := s.claimAndPriceStableBuy(context.Background(), intent, "chg_unknown_central", intent.AmountMinor, time.Now(), false, PayInCentral)
	if err != nil || !claimed {
		t.Fatalf("claimed = %v err = %v, want the money claimed into a park", claimed, err)
	}
	assertUnknownProviderPark(t, s, intent, "chg_unknown_central", logs.String())
}

func TestClaim_UnknownProviderParksForOperator_VA(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusPendingPayment)
	logs := captureLog(t)
	claimed, err := s.claimAndPriceStableBuy(context.Background(), intent, "chg_unknown_va", intent.AmountMinor, time.Now(), true, PayInVA)
	if err != nil || !claimed {
		t.Fatalf("claimed = %v err = %v", claimed, err)
	}
	assertUnknownProviderPark(t, s, intent, "chg_unknown_va", logs.String())
}

func assertUnknownProviderPark(t *testing.T, s *ITService, intent *models.ITIntent, ref, logs string) {
	t.Helper()
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || got.PaymentRef != ref ||
		!strings.Contains(got.FailureReason, "has no terms") || got.AssetAmount != intent.AssetAmount {
		t.Fatalf("intent = status %s ref %q reason %q asset %s; want a park holding the money, quote untouched",
			got.Status, got.PaymentRef, got.FailureReason, got.AssetAmount)
	}
	// Review M5: an operator is paged, not just a row parked.
	if !strings.Contains(logs, "ALERT it_stable_buy_parked_unpriceable intent="+intent.ID.String()+" payment="+ref) ||
		!strings.Contains(logs, "METRIC ramp_unknown_parked") {
		t.Fatalf("park must emit its ALERT and metric; logs:\n%s", logs)
	}
	if parkReasonOf(ErrUnknownITProvider) != "unknown_provider" || !isDeterministicPricingError(ErrUnknownITProvider) {
		t.Fatal("an unknown provider must be a deterministic pricing error with its own park reason")
	}
}

func TestQuoteRoutedLiveFor_UnknownProviderRefused(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusPendingPayment)
	if q, err := s.quoteRoutedLiveFor(context.Background(), intent, big.NewInt(1_000_000), PayInCentral); !errors.Is(err, ErrUnknownITProvider) || q != nil {
		t.Fatalf("quote = %+v err = %v", q, err)
	}
}

func TestQuoteForIntent_UnknownProviderRefused(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusBuyAwaitingRate)
	if q, err := s.quoteForIntent(context.Background(), intent, "1000000"); !errors.Is(err, ErrUnknownITProvider) || q != nil {
		t.Fatalf("quote = %+v err = %v", q, err)
	}
}

// Review Focus 3.
func TestQuoteForIntent_CompanyWalletRefusalTextUnchanged(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, models.ITProviderTreasury, models.ITStatusBuyAwaitingRate)
	_, err := s.quoteForIntent(context.Background(), intent, "1000000")
	if err == nil || err.Error() != "invalid IT intent state: a company-wallet buy is re-priced only under its stock lock" {
		t.Fatalf("err = %v", err)
	}
}

func TestRepriceParkedStableBuy_UnknownProviderRefused(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusBuyAwaitingRate)
	if _, err := s.RepriceParkedStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrUnknownITProvider) {
		t.Fatalf("err = %v, want ErrUnknownITProvider", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s, want untouched", got.Status)
	}
}

func TestRoutedMetric_NeverAttributesUnknownToFlutterwave(t *testing.T) {
	for p, want := range map[string]string{"": "fw", "flutterwave": "fw", "treasury": "treasury", "bogus": "unknown"} {
		if got := routedMetric(&models.ITIntent{Provider: p}); got != want {
			t.Fatalf("routedMetric(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestRoutedDeliveryAmounts_OnlyFlutterwaveHoldsTheSendFee(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	units := big.NewInt(7_278_150)
	for _, p := range []string{"bogus", models.ITProviderTreasury} {
		intent := seedProviderIntent(t, s, userID, p, models.ITStatusDelivering)
		send, hold := s.routedDeliveryAmounts(intent, units)
		if send.Cmp(units) != 0 || hold.Cmp(units) != 0 {
			t.Fatalf("provider %q: send = %s hold = %s, want both %s", p, send, hold, units)
		}
	}
}

func TestDeliverParkedTreasuryBuy_UnknownProviderRefused(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusBuyAwaitingRate)
	if _, err := s.DeliverParkedTreasuryBuy(context.Background(), userID, intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

func TestGetStableBuyForUser_UnknownProviderMatchesNoKnownName(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusPendingPayment)
	for _, p := range knownITProviders {
		if _, err := s.GetStableBuyForUser(context.Background(), userID, intent.ID, p); !errors.Is(err, ErrStableIntentNotFound) {
			t.Fatalf("as %q: err = %v, want ErrStableIntentNotFound", p, err)
		}
	}
}
