package services

import (
	"math/big"
	"testing"
)

func TestMinSellKobo_DefaultAndOverride(t *testing.T) {
	s := newITTestService(t)
	if got := s.minSellKobo(); got != defaultMinSellNgnKobo {
		t.Fatalf("default minSellKobo = %d, want %d (₦100)", got, defaultMinSellNgnKobo)
	}
	s.SetMinSellNgnKobo(20000) // ₦200
	if got := s.minSellKobo(); got != 20000 {
		t.Fatalf("after override = %d, want 20000", got)
	}
	// A non-positive override is ignored (keeps the current value).
	s.SetMinSellNgnKobo(0)
	if got := s.minSellKobo(); got != 20000 {
		t.Fatalf("non-positive override changed the value to %d", got)
	}
}

// The guard rejects a sell whose NGN payout is under the ₦100 floor, using the
// exact production arithmetic. Rate 2758 kobo/MCGP reproduces the real incident:
// 2 MCGP → 5516 kobo (₦55.16), which Flutterwave rejected as below ₦100.
func TestSellMinimum_ThresholdMatchesFlutterwaveFloor(t *testing.T) {
	sellRate := big.NewInt(2758) // ₦27.58 / MCGP
	floor := big.NewInt(defaultMinSellNgnKobo)

	two := new(big.Int).Mul(big.NewInt(2), oneE18)
	ngn2 := ngnForMcgp(two, sellRate)
	if ngn2.String() != "5516" {
		t.Fatalf("2 MCGP = %s kobo, expected 5516 (the observed value)", ngn2)
	}
	if ngn2.Cmp(floor) >= 0 {
		t.Fatalf("2 MCGP (%s kobo) should be BELOW the ₦100 floor — guard would wrongly allow it", ngn2)
	}

	// A sell just over the floor is allowed. 4 MCGP → 11032 kobo (₦110.32).
	four := new(big.Int).Mul(big.NewInt(4), oneE18)
	if ngnForMcgp(four, sellRate).Cmp(floor) < 0 {
		t.Fatal("4 MCGP should clear the ₦100 floor")
	}
}
