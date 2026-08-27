package services

import (
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
