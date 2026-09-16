package services

import (
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
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

// ─── Per-currency payout floors ──────────────────────────────────────────────

// TestMinPayoutMinorFor_IsPerCurrency pins that the floor comes from the
// currency's own configuration.
//
// Flutterwave's limits differ per currency: ₦100 says nothing about the cedi
// minimum. Applying the naira figure to cedis would reject roughly 100x too
// much — every legitimate small Ghanaian payout refused, with a message quoting
// a naira amount at a Ghanaian user.
func TestMinPayoutMinorFor_IsPerCurrency(t *testing.T) {
	db := newCurrencyTestDB(t)
	reg := NewCurrencyRegistry(db)
	// Operator confirms the GHS floor after sandbox-checking the rail.
	if err := db.Model(&models.Currency{}).Where("code = ?", "GHS").
		Update("min_payout_minor", 500).Error; err != nil {
		t.Fatalf("set GHS floor: %v", err)
	}
	if err := reg.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	s := newITTestService(t)
	s.SetCurrencyRegistry(reg)

	if got, label := s.minPayoutMinorFor("NGN"); got != defaultMinSellNgnKobo || label != "NGN" {
		t.Errorf("NGN floor = %d/%s, want %d/NGN", got, label, defaultMinSellNgnKobo)
	}
	if got, _ := s.minPayoutMinorFor("GHS"); got != 500 {
		t.Errorf("GHS floor = %d, want 500 — the naira floor must not be reused for cedis", got)
	}
}

// TestMinPayoutMinorFor_UnconfiguredCurrencyIsNotGated pins that an
// unconfigured floor means "not yet confirmed", not "reject everything".
//
// Inventing a floor would refuse legitimate payouts. The provider still rejects
// a genuinely too-small transfer; the cost is that it does so after the debit —
// which is already the situation for every currency whose limit we have not
// confirmed, so this changes nothing while avoiding a fabricated rule.
func TestMinPayoutMinorFor_UnconfiguredCurrencyIsNotGated(t *testing.T) {
	s := newITTestService(t)
	s.SetCurrencyRegistry(NewCurrencyRegistry(newCurrencyTestDB(t)))

	// USD is seeded with no floor (unconfirmed).
	if got, _ := s.minPayoutMinorFor("USD"); got != 0 {
		t.Errorf("USD floor = %d, want 0 (ungated) — a fabricated floor rejects valid payouts", got)
	}
	// A currency the registry does not know at all is likewise ungated here;
	// it is refused earlier, by the catalog and the registry itself.
	if got, _ := s.minPayoutMinorFor("KES"); got != 0 {
		t.Errorf("unknown currency floor = %d, want 0", got)
	}
}

// TestMinPayoutMinorFor_NoRegistryKeepsNgnBehaviour confirms the pre-registry
// posture is preserved when no registry is wired.
func TestMinPayoutMinorFor_NoRegistryKeepsNgnBehaviour(t *testing.T) {
	s := newITTestService(t)
	if got, _ := s.minPayoutMinorFor("NGN"); got != defaultMinSellNgnKobo {
		t.Errorf("NGN floor without a registry = %d, want %d", got, defaultMinSellNgnKobo)
	}
	if got, _ := s.minPayoutMinorFor("GHS"); got != 0 {
		t.Errorf("GHS floor without a registry = %d, want 0 (ungated, not the naira floor)", got)
	}
}
