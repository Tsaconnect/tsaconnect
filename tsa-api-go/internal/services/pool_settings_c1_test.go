package services

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// specPoolSettings is spec §16.1's defaults (decision 19: spread 50 bps) as one
// value, so pricing and allocation tests never depend on the Fees replica.
func specPoolSettings() PoolSettings {
	return PoolSettings{SpreadBps: 50, TakeBps: 50, BandBps: 50,
		MinSliceUnits: big.NewInt(2_000_000), OrderMaxUnits: big.NewInt(2_000_000_000),
		UserDailyMaxUnits: big.NewInt(2_000_000_000), NetworkFeeUnits: big.NewInt(40_000)}
}

// S33 / decision 19: the seven pool rows ensured once, with the defaults.
func TestPoolSettings_EnsureRowsIdempotent_C1Rows(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	svc := NewFeeConfigService(db)
	for i := 0; i < 2; i++ {
		if err := svc.ensurePoolSettingRows(); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	for feature, want := range map[string]struct {
		bps  int
		flat float64
	}{
		models.FeaturePoolSpread: {50, 0}, models.FeaturePoolTake: {50, 0}, models.FeaturePoolBand: {50, 0},
		models.FeaturePoolMinSlice: {0, 2},
		models.FeaturePoolOrderMax: {0, 2000}, models.FeaturePoolUserDailyMax: {0, 2000},
		models.FeaturePoolNetworkFee: {0, 0.04},
	} {
		var rows []models.FeeConfig
		db.Where("feature = ?", feature).Find(&rows)
		if len(rows) != 1 || rows[0].BPS != want.bps || rows[0].FlatAmountUSD != want.flat ||
			rows[0].TokenSymbol != models.FeeTokenAny || rows[0].ChainID != nil || rows[0].Direction != nil || !rows[0].Active {
			t.Fatalf("%s rows = %+v, want bps %d flat %v", feature, rows, want.bps, want.flat)
		}
	}
	var n int64
	db.Model(&models.FeeConfig{}).Where("feature LIKE ?", "pool_%").Count(&n)
	if int(n) != len(poolSettingDefaults) || len(poolSettingDefaults) != 7 {
		t.Fatalf("pool rows = %d (defaults %d), want 7", n, len(poolSettingDefaults))
	}
}

// Roadmap risk 10 inputs: the snapshot an order prices at.
func TestCurrentPoolSettings_DefaultsMatchSpec(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil // every row missing → defaults with an ALERT, never 0 or unbounded
	s := CurrentPoolSettings()
	if s.SpreadBps != 50 || s.TakeBps != 50 || s.BandBps != 50 || s.OrdersStopped ||
		s.MinSliceUnits.String() != "2000000" || s.OrderMaxUnits.String() != "2000000000" || s.UserDailyMaxUnits.String() != "2000000000" ||
		s.NetworkFeeUnits.String() != "40000" {
		t.Fatalf("defaults = %+v", s)
	}
}

// S46: pool_order_max = 0 is the incident stop.
func TestCurrentPoolSettings_OrderMaxZeroStopsOrders(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	db := setupFeeConfigTestDB(t)
	InitFeeConfigService(db)
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolOrderMax).Update("flat_amount_usd", 0)
	if err := Fees.Invalidate(); err != nil {
		t.Fatal(err)
	}
	if s := CurrentPoolSettings(); !s.OrdersStopped || s.OrderMaxUnits.Sign() != 0 {
		t.Fatalf("settings = %+v, want orders stopped", s)
	}
}

// Review M9: a lower RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER in prod than
// the ensured 2,000 row caps orders at read time, not only at the next edit.
func TestCurrentPoolSettings_OrderMaxClampedToTheTreasuryCeiling(t *testing.T) {
	prev, prevCeil := Fees, poolOrderMaxCeilingUSD
	t.Cleanup(func() { Fees, poolOrderMaxCeilingUSD = prev, prevCeil })
	db := setupFeeConfigTestDB(t)
	InitFeeConfigService(db) // ensures pool_order_max = 2000
	SetPoolOrderMaxCeilingUSD(500)
	if s := CurrentPoolSettings(); s.OrdersStopped || s.OrderMaxUnits.Cmp(usdToCanonical(500)) != 0 {
		t.Fatalf("order max = %s stopped=%v, want 500 USD", s.OrderMaxUnits, s.OrdersStopped)
	}
}

func TestPoolSettings_ValidationRanges_C1Rows(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	if err := NewFeeConfigService(db).ensurePoolSettingRows(); err != nil {
		t.Fatal(err)
	}
	prevCeil := poolOrderMaxCeilingUSD
	t.Cleanup(func() { poolOrderMaxCeilingUSD = prevCeil })
	SetPoolOrderMaxCeilingUSD(2000)
	for _, tc := range []struct {
		feature string
		bps     int
		flat    float64
		wantErr string
	}{
		{models.FeaturePoolSpread, 50, 0, ""},
		{models.FeaturePoolSpread, 301, 0, "between 0 and 300"},
		{models.FeaturePoolSpread, 50, 1, "is a rate"},
		{models.FeaturePoolTake, 0, 0, ""},
		{models.FeaturePoolBand, 501, 0, "between 0 and 500"},
		{models.FeaturePoolMinSlice, 0, 2, ""},
		{models.FeaturePoolMinSlice, 0, 0.25, ""},
		{models.FeaturePoolMinSlice, 0, 0.5, ""},
		{models.FeaturePoolMinSlice, 0, 0.24, "between 0.25 and 100"},
		{models.FeaturePoolMinSlice, 0, 101, "between 0.25 and 100"},
		{models.FeaturePoolNetworkFee, 0, 0, ""},
		{models.FeaturePoolNetworkFee, 0, 0.04, ""},
		{models.FeaturePoolNetworkFee, 0, 5, ""},
		{models.FeaturePoolNetworkFee, 0, 5.01, "between 0 and 5"},
		{models.FeaturePoolNetworkFee, 0, -0.01, "between 0 and 5"},
		{models.FeaturePoolNetworkFee, 3, 0, "flat USD"},
		{models.FeaturePoolNetworkFee, 0, 0.0000001, "at most 6 decimal places"},
		{models.FeaturePoolMinSlice, 0, 0.2500005, "at most 6 decimal places"},
		{models.FeaturePoolNetworkFee, 0, 0.000001, ""},
		{models.FeaturePoolMinSlice, 10, 2, "flat USD"},
		{models.FeaturePoolOrderMax, 0, 0, ""}, // the incident stop
		{models.FeaturePoolOrderMax, 0, 2001, "at most 2000"},
		{models.FeaturePoolOrderMax, 0, 1, "at least the minimum order"},
		{models.FeaturePoolUserDailyMax, 0, 1999, "at least the order maximum"},
		{models.FeaturePoolUserDailyMax, 0, 100001, "at most 100000"},
	} {
		err := ValidatePoolSetting(db, tc.feature, tc.bps, tc.flat)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s bps=%d flat=%v: unexpected %v", tc.feature, tc.bps, tc.flat, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s bps=%d flat=%v: err = %v, want %q", tc.feature, tc.bps, tc.flat, err, tc.wantErr)
		}
	}
	// With the stop set, any daily maximum ≥ 1 is accepted.
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolOrderMax).Update("flat_amount_usd", 0)
	if err := ValidatePoolSetting(db, models.FeaturePoolUserDailyMax, 0, 10); err != nil {
		t.Fatalf("daily max while stopped: %v", err)
	}
}
