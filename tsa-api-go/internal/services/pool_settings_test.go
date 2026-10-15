package services

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func TestPoolSettings_EnsureRowsIdempotent(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	svc := NewFeeConfigService(db)
	for i := 0; i < 2; i++ {
		if err := svc.ensurePoolSettingRows(); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	// An admin edit survives the next boot.
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolNetworkFee).Update("flat_amount_usd", 0.07)
	if err := svc.ensurePoolSettingRows(); err != nil {
		t.Fatal(err)
	}
	var rows []models.FeeConfig
	db.Where("feature = ?", models.FeaturePoolNetworkFee).Find(&rows)
	if len(rows) != 1 || rows[0].FlatAmountUSD != 0.07 {
		t.Fatalf("admin edit overwritten: %+v", rows)
	}
}

func TestInitFeeConfigService_EnsuresPoolRows(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	db := setupFeeConfigTestDB(t)
	InitFeeConfigService(db)
	if _, flat, _, found := Fees.RateFor(models.FeaturePoolNetworkFee, models.FeeTokenAny, nil); !found || flat != 0.04 {
		t.Fatalf("pool_network_fee in the replica after boot = %v found=%v, want 0.04", flat, found)
	}
}

// seedPoolRow writes one pool row as an earlier release left it.
func seedPoolRow(t *testing.T, db *gorm.DB, feature string, bps int, flat float64, active bool) {
	t.Helper()
	row := models.FeeConfig{ID: uuid.New(), Feature: feature, TokenSymbol: models.FeeTokenAny, BPS: bps, FlatAmountUSD: flat, Active: true}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !active {
		db.Model(&row).Update("active", false)
	}
}

func poolRowCount(db *gorm.DB, feature string) int64 {
	var n int64
	db.Model(&models.FeeConfig{}).Where("feature = ?", feature).Count(&n)
	return n
}

// Cleanup 2026-10-15: on a database the earlier release ensured, the boot
// folds the two gas rows into pool_network_fee (their sum, edits included)
// and deletes every retired row — once.
func TestPoolSettings_RetireFoldsGasRowsAndDeletesRetired(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	seedPoolRow(t, db, models.RetiredPoolLPMin, 0, 5, true)
	seedPoolRow(t, db, models.RetiredPoolLPMax, 0, 5000, true)
	seedPoolRow(t, db, models.RetiredPoolSliceCap, 3, 0, true)
	seedPoolRow(t, db, models.RetiredPoolGasPerSlice, 0, 0.05, true) // admin-edited
	seedPoolRow(t, db, models.RetiredPoolGasDelivery, 0, 0.02, true)
	seedPoolRow(t, db, models.FeaturePoolSpread, 75, 0, true) // an edited live row
	svc := NewFeeConfigService(db)
	for i := 0; i < 2; i++ {
		if err := svc.ensurePoolSettingRows(); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	var fee []models.FeeConfig
	db.Where("feature = ?", models.FeaturePoolNetworkFee).Find(&fee)
	if len(fee) != 1 || fee[0].FlatAmountUSD != 0.07 || fee[0].BPS != 0 || !fee[0].Active || fee[0].ChainID != nil || fee[0].Direction != nil {
		t.Fatalf("pool_network_fee = %+v, want one active 0.07 row", fee)
	}
	for _, f := range retiredPoolSettings {
		if n := poolRowCount(db, f); n != 0 {
			t.Fatalf("%s rows = %d after the retire, want 0", f, n)
		}
	}
	var spread models.FeeConfig
	db.Where("feature = ?", models.FeaturePoolSpread).First(&spread)
	if spread.BPS != 75 || poolRowCount(db, models.FeaturePoolSpread) != 1 {
		t.Fatalf("live row touched: %+v", spread)
	}
}

// The fold keeps the price an order is quoted at today: an inactive or
// missing gas row priced at its default, so it counts as that.
func TestPoolSettings_RetireFoldUsesDefaultsForInactiveOrMissing(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	seedPoolRow(t, db, models.RetiredPoolGasPerSlice, 0, 0.5, false) // inactive → 0.03
	// pool_gas_delivery missing → 0.01
	if err := NewFeeConfigService(db).ensurePoolSettingRows(); err != nil {
		t.Fatal(err)
	}
	var fee models.FeeConfig
	db.Where("feature = ?", models.FeaturePoolNetworkFee).First(&fee)
	if fee.FlatAmountUSD != 0.04 {
		t.Fatalf("pool_network_fee = %v, want 0.04", fee.FlatAmountUSD)
	}
}

// An existing pool_network_fee is never re-seeded from leftover gas rows.
func TestPoolSettings_RetireKeepsAnExistingNetworkFee(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	seedPoolRow(t, db, models.FeaturePoolNetworkFee, 0, 0.1, true)
	seedPoolRow(t, db, models.RetiredPoolGasPerSlice, 0, 1, true)
	if err := NewFeeConfigService(db).ensurePoolSettingRows(); err != nil {
		t.Fatal(err)
	}
	var fee []models.FeeConfig
	db.Where("feature = ?", models.FeaturePoolNetworkFee).Find(&fee)
	if len(fee) != 1 || fee[0].FlatAmountUSD != 0.1 || poolRowCount(db, models.RetiredPoolGasPerSlice) != 0 {
		t.Fatalf("pool_network_fee = %+v, gas rows = %d", fee, poolRowCount(db, models.RetiredPoolGasPerSlice))
	}
}

func TestPoolSettings_RetiredGasSumClampedToRange(t *testing.T) {
	got := retiredGasSumUSD([]models.FeeConfig{{Feature: models.RetiredPoolGasPerSlice, FlatAmountUSD: 4, Active: true},
		{Feature: models.RetiredPoolGasDelivery, FlatAmountUSD: 3, Active: true}})
	if got != 5 {
		t.Fatalf("sum = %v, want the 5 USD ceiling", got)
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
		{"pool_unknown", 0, 1, "unknown TSA Liquidity setting"},
		// Retired rows are no longer settings.
		{models.RetiredPoolLPMin, 0, 5, "unknown TSA Liquidity setting"},
		{models.RetiredPoolLPMax, 0, 5000, "unknown TSA Liquidity setting"},
		{models.RetiredPoolSliceCap, 3, 0, "unknown TSA Liquidity setting"},
		{models.RetiredPoolGasPerSlice, 0, 0.03, "unknown TSA Liquidity setting"},
		{models.RetiredPoolGasDelivery, 0, 0.01, "unknown TSA Liquidity setting"},
	} {
		err := ValidatePoolSetting(db, tc.feature, tc.bps, tc.flat)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s %v: unexpected %v", tc.feature, tc.flat, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s %v: err = %v, want it to mention %q", tc.feature, tc.flat, err, tc.wantErr)
		}
	}
}
