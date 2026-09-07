package services

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// stubPaycrestOrderer implements PaycrestOrderer. Its methods panic when
// called — the kill-switch gate in CreateBuy/CreateSell must return
// ErrPaycrestDisabled before ever reaching the client, so a test that hits
// these methods has a bug in the gate, not in the stub.
type stubPaycrestOrderer struct{}

func (stubPaycrestOrderer) CreateOnrampOrder(context.Context, PaycrestOnrampRequest) (*PaycrestOrder, error) {
	panic("CreateOnrampOrder should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestOrderer) CreateOfframpOrder(context.Context, PaycrestOfframpRequest) (*PaycrestOrder, error) {
	panic("CreateOfframpOrder should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestOrderer) GetOrder(context.Context, string) (*PaycrestOrder, error) {
	panic("GetOrder should not be reached by this test")
}

// newPaycrestOrderTestService builds a PaycrestOrderService against an
// in-memory SQLite DB with both tables it touches before any provider call:
// paycrest_order_records (assertNoOpenOrder) and currencies (the kill-switch
// gate, once wired).
func newPaycrestOrderTestService(t *testing.T) (*PaycrestOrderService, *CurrencyRegistry) {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}

	quotes := NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil)
	svc := NewPaycrestOrderService(db, stubPaycrestOrderer{}, quotes, nil, "", "")
	if svc == nil {
		t.Fatal("NewPaycrestOrderService returned nil")
	}

	registry := NewCurrencyRegistry(db)
	return svc, registry
}

// stubPaycrestQuoter satisfies PaycrestQuoter. Never called in the disabled
// path, same reasoning as stubPaycrestOrderer.
type stubPaycrestQuoter struct{}

func (stubPaycrestQuoter) Markets(context.Context) (*PaycrestMarkets, error) {
	panic("Markets should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestQuoter) Rates(context.Context, string, string, float64, string) (*PaycrestRates, error) {
	panic("Rates should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestQuoter) MarketRate(context.Context, string, string) (float64, error) {
	panic("MarketRate should not be reached when the currency's ramp provider is not paycrest")
}

// TestPaycrestOrderService_NilRegistryIsNoGate proves SetCurrencyRegistry is
// truly optional: without it, an order proceeds past the currency check (and
// fails downstream on the stub client, not on ErrPaycrestDisabled) — so
// existing/other construction sites that never call SetCurrencyRegistry keep
// working unmodified.
func TestPaycrestOrderService_NilRegistryIsNoGate(t *testing.T) {
	svc, _ := newPaycrestOrderTestService(t)
	// Deliberately do not call SetCurrencyRegistry.

	// With no gate, the request proceeds past the currency check and reaches
	// the stub client (panic) instead of stopping on ErrPaycrestDisabled.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the request to reach the stub client (panic) since no registry is wired")
		}
	}()
	_, _ = svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:            uuid.New(),
		WalletAddress:     "0xabc",
		FiatCode:          "KES",
		FiatAmount:        "1000",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	})
}

// TestPaycrestOrderService_CreateBuy_RefusesWhenNotPaycrest is finding 2: the
// documented kill switch (flip a currency's ramp_provider column) must
// actually stop CreateBuy from opening a new order, not just change what
// GET /it/currencies advertises to app builds that haven't refreshed.
func TestPaycrestOrderService_CreateBuy_RefusesWhenNotPaycrest(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)

	// KES is payout-enabled but (per finding 1) carries no ramp provider by
	// default — exactly the "not paycrest" case the gate exists for.
	if got := registry.RampProvider("KES"); got == RampProviderPaycrest {
		t.Fatalf("test fixture invalid: KES resolves to paycrest (got %q)", got)
	}

	_, err := svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:            uuid.New(),
		WalletAddress:     "0xabc",
		FiatCode:          "KES",
		FiatAmount:        "1000",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	})
	if !errors.Is(err, ErrPaycrestDisabled) {
		t.Fatalf("CreateBuy err = %v, want ErrPaycrestDisabled", err)
	}
}

// TestPaycrestOrderService_CreateSell_RefusesWhenNotPaycrest mirrors the buy
// case for the offramp path.
func TestPaycrestOrderService_CreateSell_RefusesWhenNotPaycrest(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)

	_, err := svc.CreateSell(context.Background(), PaycrestSellRequest{
		UserID:        uuid.New(),
		WalletAddress: "0xabc",
		FiatCode:      "KES",
		FiatAmount:    "1000",
		BankCode:      "044",
		AccountNo:     "0123456789",
		AccountName:   "Test User",
	})
	if !errors.Is(err, ErrPaycrestDisabled) {
		t.Fatalf("CreateSell err = %v, want ErrPaycrestDisabled", err)
	}
}

// TestPaycrestOrderService_CreateBuy_AllowsWhenPaycrest proves the gate does
// not over-refuse: NGN, whose default resolves to paycrest, must pass the
// gate and fail downstream instead (on the stub's panic guard, surfaced as a
// test failure with a clear message, or on a validation error before the
// stub is reached).
func TestPaycrestOrderService_CreateBuy_AllowsWhenPaycrest(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)

	if got := registry.RampProvider("NGN"); got != RampProviderPaycrest {
		t.Fatalf("test fixture invalid: NGN resolves to %q, want paycrest", got)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the request to reach the stub client (panic) once past the currency gate")
		}
	}()
	_, _ = svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:            uuid.New(),
		WalletAddress:     "0xabc",
		FiatCode:          "NGN",
		FiatAmount:        "1000",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	})
}

// The user types what they will send. Paycrest adds its sender fee ON TOP of
// the submitted amount, so submitting the user's figure directly told someone
// who entered 800 to transfer 804.02 — and the screen then asserted both "you
// pay 800" and "transfer 804.02", which cannot both be true. Dividing first
// puts the fee in the rate instead.
func TestGrossDownForSenderFee(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		feePct    string
		want      string
		applied   bool
	}{
		{"0.5% on 800 round-trips to 800", "800", "0.5", "796.02", true},
		{"0.5% on 5000 round-trips to 5000", "5000", "0.5", "4975.12", true},
		{"1% on 1000", "1000", "1", "990.10", true},
		// Unknown fee: the percent lives in the dashboard profile, not our env.
		// Guessing would put the transfer a few kobo off the amount the provider
		// waits for, and an inexact transfer is what strands a payment.
		{"unknown fee leaves the amount alone", "800", "", "800", false},
		{"unparseable fee leaves the amount alone", "800", "abc", "800", false},
		{"zero fee leaves the amount alone", "800", "0", "800", false},
		{"unparseable amount is passed through", "abc", "0.5", "abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, applied := grossDownForSenderFee(tc.requested, tc.feePct)
			if got != tc.want || applied != tc.applied {
				t.Fatalf("grossDownForSenderFee(%q, %q) = (%q, %v), want (%q, %v)",
					tc.requested, tc.feePct, got, applied, tc.want, tc.applied)
			}
		})
	}
}

// The whole point: whatever we submit, the provider's own arithmetic must land
// back on the number the user typed. This asserts the round trip rather than
// the intermediate value, because the intermediate is only ever a means to it.
func TestGrossDownForSenderFee_RoundTripsToTheUsersNumber(t *testing.T) {
	for _, requested := range []string{"800", "1000", "5000", "137500", "999.99"} {
		for _, pct := range []string{"0.5", "1", "2.5"} {
			submitted, applied := grossDownForSenderFee(requested, pct)
			if !applied {
				t.Fatalf("expected gross-down to apply for %s @ %s%%", requested, pct)
			}
			sub, _ := strconv.ParseFloat(submitted, 64)
			p, _ := strconv.ParseFloat(pct, 64)
			want, _ := strconv.ParseFloat(requested, 64)
			// What Paycrest will compute as amountToTransfer.
			got := sub * (1 + p/100)
			// One kobo of rounding slack; more than that and the user is told to
			// transfer a different number from the one they entered.
			if diff := got - want; diff > 0.01 || diff < -0.01 {
				t.Errorf("%s @ %s%%: submitted %s -> provider asks %.4f, wanted %.2f (%.4f off)",
					requested, pct, submitted, got, want, diff)
			}
		}
	}
}
