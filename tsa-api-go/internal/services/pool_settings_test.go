package services

import (
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestPoolSettings_EnsureRowsIdempotent(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	svc := NewFeeConfigService(db)
	for i := 0; i < 2; i++ {
		if err := svc.ensurePoolSettingRows(); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	for feature, flat := range map[string]float64{models.FeaturePoolLPMin: 5, models.FeaturePoolLPMax: 5000} {
		var rows []models.FeeConfig
		db.Where("feature = ?", feature).Find(&rows)
		if len(rows) != 1 || rows[0].FlatAmountUSD != flat || rows[0].TokenSymbol != models.FeeTokenAny ||
			rows[0].ChainID != nil || rows[0].Direction != nil || !rows[0].Active {
			t.Fatalf("%s rows = %+v", feature, rows)
		}
	}
	// An admin edit survives the next boot.
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolLPMax).Update("flat_amount_usd", 7000)
	if err := svc.ensurePoolSettingRows(); err != nil {
		t.Fatal(err)
	}
	var row models.FeeConfig
	db.Where("feature = ?", models.FeaturePoolLPMax).First(&row)
	if row.FlatAmountUSD != 7000 {
		t.Fatalf("admin edit overwritten: %v", row.FlatAmountUSD)
	}
}

func TestInitFeeConfigService_EnsuresPoolRows(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	db := setupFeeConfigTestDB(t)
	InitFeeConfigService(db)
	if min, max := PoolLPLimitsUSD(); min != 5 || max != 5000 {
		t.Fatalf("limits = %v/%v, want 5/5000 from the ensured rows", min, max)
	}
	if _, _, _, found := Fees.RateFor(models.FeaturePoolLPMax, models.FeeTokenAny, nil); !found {
		t.Fatal("pool_lp_max not in the replica after boot")
	}
}

func TestPoolSettings_LPLimitsFallBackToDefaults(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil
	if min, max := PoolLPLimitsUSD(); min != 5 || max != 5000 {
		t.Fatalf("defaults = %v/%v", min, max)
	}
}

// Review Focus 5: USD → 18-dp base units, exact (1 USDT = $1, spec §2).
func TestPoolLPLimitUnits_18dp(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil
	min, max, err := PoolLPLimitUnits(18)
	if err != nil || min.String() != "5000000000000000000" || max.String() != "5000000000000000000000" {
		t.Fatalf("18 dp: %v %v %v", min, max, err)
	}
	min, max, _ = PoolLPLimitUnits(6)
	if min.String() != "5000000" || max.String() != "5000000000" {
		t.Fatalf("6 dp: %v %v", min, max)
	}
	if _, _, err := PoolLPLimitUnits(2); err == nil {
		t.Fatal("a token below 6 decimals must be refused (S7)")
	}
}

func TestPoolSettings_ValidationRanges(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	svc := NewFeeConfigService(db)
	if err := svc.ensurePoolSettingRows(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		feature string
		bps     int
		flat    float64
		wantErr string
	}{
		{models.FeaturePoolLPMin, 0, 5, ""},
		{models.FeaturePoolLPMin, 0, 0.5, "between 1 and 1000"},
		{models.FeaturePoolLPMin, 0, 1001, "between 1 and 1000"},
		{models.FeaturePoolLPMin, 10, 5, "flat USD"},
		{models.FeaturePoolLPMax, 0, 5000, ""},
		{models.FeaturePoolLPMax, 0, 4, "at least the minimum"}, // min is 5
		{models.FeaturePoolLPMax, 0, 100001, "at most 100000"},
		{"pool_unknown", 0, 1, "unknown TSA Liquidity setting"},
	} {
		err := ValidatePoolSetting(db, tc.feature, tc.bps, tc.flat)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s %v: unexpected %v", tc.feature, tc.flat, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s %v: err = %v, want it to mention %q", tc.feature, tc.flat, err, tc.wantErr)
		}
	}
	// The min is checked against the CURRENT max row.
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolLPMax).Update("flat_amount_usd", 500)
	if err := ValidatePoolSetting(db, models.FeaturePoolLPMin, 0, 600); err == nil || !strings.Contains(err.Error(), "cannot exceed the maximum") {
		t.Fatalf("min above the current max: %v", err)
	}
}
