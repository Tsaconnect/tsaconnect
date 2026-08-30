package services

import (
	"errors"
	"math/big"
	"testing"
)

func TestApplySellSpread_TakesTheCompanyMargin(t *testing.T) {
	// 1,000,000 kobo (₦10,000) less 155bps = 984,500 kobo.
	got := ApplySellSpread(big.NewInt(1000000), 155)
	if got.String() != "984500" {
		t.Fatalf("ApplySellSpread = %s, want 984500", got)
	}
}

func TestApplySellSpread_RoundsDownAgainstTheUser(t *testing.T) {
	// 101 * 9845 / 10000 = 99.4345 → must floor to 99, never 100. Rounding up
	// would pay out more than the platform received.
	got := ApplySellSpread(big.NewInt(101), 155)
	if got.String() != "99" {
		t.Fatalf("ApplySellSpread(101) = %s, want 99 (floored)", got)
	}
}

func TestApplySellSpread_ZeroBpsIsIdentity(t *testing.T) {
	got := ApplySellSpread(big.NewInt(12345), 0)
	if got.String() != "12345" {
		t.Fatalf("zero spread changed the amount: %s", got)
	}
}

func TestApplySellSpread_DoesNotMutateItsInput(t *testing.T) {
	in := big.NewInt(1000000)
	_ = ApplySellSpread(in, 155)
	if in.String() != "1000000" {
		t.Fatalf("input mutated to %s", in)
	}
}

func TestDeriveFiatPerUnit_UsesAmountsNotTheRateField(t *testing.T) {
	// FWRate.Rate is deliberately absurd here: the vendor's own docs give a
	// self-contradictory example and never define its unit direction, so the
	// derivation must ignore it entirely.
	r := FWRate{
		Rate:        0.000707,
		Source:      FWMoney{Currency: "USDC", Amount: 1},
		Destination: FWMoney{Currency: "NGN", Amount: 1650.25},
	}
	got, err := DeriveFiatPerUnit(r)
	if err != nil {
		t.Fatalf("DeriveFiatPerUnit: %v", err)
	}
	want := new(big.Rat).SetFloat64(1650.25)
	if got.Cmp(want) != 0 {
		t.Fatalf("DeriveFiatPerUnit = %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

func TestDeriveFiatPerUnit_RejectsUnusableProbes(t *testing.T) {
	for name, r := range map[string]FWRate{
		"zero source":      {Source: FWMoney{Amount: 0}, Destination: FWMoney{Amount: 100}},
		"zero destination": {Source: FWMoney{Amount: 1}, Destination: FWMoney{Amount: 0}},
		"negative":         {Source: FWMoney{Amount: -1}, Destination: FWMoney{Amount: 100}},
	} {
		if _, err := DeriveFiatPerUnit(r); err == nil {
			t.Errorf("%s: accepted an unusable probe", name)
		}
	}
}

func TestDeriveBuyFiatPerUnit(t *testing.T) {
	// Buy probes source=NGN, destination=asset: "to receive 1 USDC you pay
	// 1650.25 NGN". The answer must be NGN per USDC, i.e. src/dst.
	rate := FWRate{
		Source:      FWMoney{Currency: "NGN", Amount: 1650.25},
		Destination: FWMoney{Currency: "USDC", Amount: 1},
	}
	got, err := DeriveBuyFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := new(big.Rat).SetFloat64(1650.25)
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

func TestDeriveBuyFiatPerUnit_IsTheInverseOfSell(t *testing.T) {
	// Guards the inversion bug directly: the same amounts read in opposite
	// directions must produce reciprocal rates.
	rate := FWRate{
		Source:      FWMoney{Currency: "NGN", Amount: 1650.25},
		Destination: FWMoney{Currency: "USDC", Amount: 1},
	}
	buy, err := DeriveBuyFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sell, err := DeriveFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	product := new(big.Rat).Mul(buy, sell)
	if product.Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatalf("buy * sell = %s, want exactly 1", product.FloatString(10))
	}
}

func TestDeriveBuyFiatPerUnit_RejectsUnusableProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate FWRate
	}{
		{"zero source", FWRate{Source: FWMoney{Amount: 0}, Destination: FWMoney{Amount: 1}}},
		{"zero destination", FWRate{Source: FWMoney{Amount: 1}, Destination: FWMoney{Amount: 0}}},
		{"negative source", FWRate{Source: FWMoney{Amount: -5}, Destination: FWMoney{Amount: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeriveBuyFiatPerUnit(tc.rate); !errors.Is(err, ErrBadFxProbe) {
				t.Fatalf("err = %v, want ErrBadFxProbe", err)
			}
		})
	}
}

func TestApplyBuySpread(t *testing.T) {
	// The user receives LESS asset. Flooring means we never deliver a unit we
	// did not buy — the mirror of ApplySellSpread never paying out one.
	got := ApplyBuySpread(big.NewInt(100_000_000), 155) // 100 USDC, 1.55%
	want := big.NewInt(98_450_000)                      // 98.45 USDC
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s, want %s", got, want)
	}

	if got := ApplyBuySpread(big.NewInt(100), 0); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("zero spread changed the amount: %s", got)
	}
	if got := ApplyBuySpread(nil, 155); got.Sign() != 0 {
		t.Fatalf("nil input = %s, want 0", got)
	}

	// Flooring, never rounding up: 1 unit at 1.55% must round DOWN to 0.
	if got := ApplyBuySpread(big.NewInt(1), 155); got.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("got %s, want 0 (must floor against the user)", got)
	}
}
