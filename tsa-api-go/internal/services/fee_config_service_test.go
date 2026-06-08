package services

import (
	"math"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupFeeConfigTestDB opens an in-memory SQLite DB with the fee_configs table
// created via raw DDL (avoiding the PostgreSQL gen_random_uuid() default that
// AutoMigrate would emit). Mirrors the other service test setups.
func setupFeeConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// chain_id + the (feature, token_symbol, chain_id) unique index mirror the
	// configurable-FeeManager design §4 migration (the AutoMigrate path on
	// PostgreSQL). chain_id NULL = the legacy global row.
	if err := db.Exec(`CREATE TABLE fee_configs (
		id TEXT PRIMARY KEY,
		feature TEXT NOT NULL,
		token_symbol TEXT NOT NULL,
		chain_id INTEGER,
		bps INTEGER NOT NULL DEFAULT 0,
		flat_amount_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME,
		UNIQUE(feature, token_symbol, chain_id)
	)`).Error; err != nil {
		t.Fatalf("create fee_configs: %v", err)
	}
	return db
}

// newSeededService returns a FeeConfigService backed by a fresh seeded DB.
func newSeededService(t *testing.T) (*FeeConfigService, *gorm.DB) {
	t.Helper()
	db := setupFeeConfigTestDB(t)
	svc := NewFeeConfigService(db)
	return svc, db
}

func TestSeedsDefaultsWhenEmpty(t *testing.T) {
	svc, db := newSeededService(t)

	var count int64
	if err := db.Model(&models.FeeConfig{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if int(count) != len(DefaultFeeConfigs()) {
		t.Fatalf("expected %d seeded rows, got %d", len(DefaultFeeConfigs()), count)
	}
	if len(svc.All()) != len(DefaultFeeConfigs()) {
		t.Fatalf("cache holds %d rows, want %d", len(svc.All()), len(DefaultFeeConfigs()))
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	db := setupFeeConfigTestDB(t)
	_ = NewFeeConfigService(db)
	// Construct again over the same (already-seeded) DB — must not double-seed.
	_ = NewFeeConfigService(db)

	var count int64
	db.Model(&models.FeeConfig{}).Count(&count)
	if int(count) != len(DefaultFeeConfigs()) {
		t.Fatalf("re-init double-seeded: got %d rows, want %d", count, len(DefaultFeeConfigs()))
	}
}

// TestRateForSeededValues asserts each seeded row resolves to the exact current
// production rate it replaces.
func TestRateForSeededValues(t *testing.T) {
	svc, _ := newSeededService(t)

	cases := []struct {
		feature   string
		token     string
		wantBPS   int
		wantFlat  float64
		wantFound bool
	}{
		{models.FeatureProductMerchant, "USDC", models.MerchantFeeBPS, 0, true}, // 200 bps = 2%
		{models.FeatureProductMerchant, "USDT", 200, 0, true},
		{models.FeatureProduct, "USDC", 0, models.GasFeeUSD, true}, // 0% + $0.10 flat
		{models.FeatureServiceContact, "USDC", 0, 0.10, true},      // $0.10 flat
		{models.FeatureWithdrawal, "BTC", 50, 0, true},             // 0.5%
		{models.FeatureSwap, "ETH", 10, 0, true},                   // 0.1%
		{models.FeatureServiceOther, "USDC", 50, 0, true},
		{models.FeatureOTC, "USDC", 50, 0, true},
		{models.FeatureP2P, "USDT", 50, 0, true},
		{models.FeatureP2PPlatform, "USDT", 100, 0, true},
		{"nonexistent_feature", "USDC", 0, 0, false},
	}

	for _, tc := range cases {
		bps, flat, _, found := svc.RateFor(tc.feature, tc.token, nil)
		if found != tc.wantFound {
			t.Errorf("%s/%s: found=%v want %v", tc.feature, tc.token, found, tc.wantFound)
			continue
		}
		if !tc.wantFound {
			continue
		}
		if bps != tc.wantBPS {
			t.Errorf("%s/%s: bps=%d want %d", tc.feature, tc.token, bps, tc.wantBPS)
		}
		if flat != tc.wantFlat {
			t.Errorf("%s/%s: flat=%v want %v", tc.feature, tc.token, flat, tc.wantFlat)
		}
	}
}

// TestTokenSpecificBeatsWildcard verifies a (feature, specific-token) row is
// preferred over the (feature, "*") row.
func TestTokenSpecificBeatsWildcard(t *testing.T) {
	svc, db := newSeededService(t)

	// Insert a USDC-specific override for withdrawal at 99 bps.
	override := DefaultFeeConfigs()[0]
	override.Feature = models.FeatureWithdrawal
	override.TokenSymbol = "USDC"
	override.BPS = 99
	override.FlatAmountUSD = 0
	override.MCGPExempt = false
	if err := db.Create(&override).Error; err != nil {
		t.Fatalf("create override: %v", err)
	}
	if err := svc.Invalidate(); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	// USDC must hit the specific row...
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "USDC", nil); bps != 99 {
		t.Errorf("USDC withdrawal bps=%d, want 99 (token-specific)", bps)
	}
	// ...while another token still uses the wildcard 50 bps.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", nil); bps != 50 {
		t.Errorf("BTC withdrawal bps=%d, want 50 (wildcard)", bps)
	}
}

// TestMCGPExempt verifies MCGP resolves to exempt+zero on exempt rows, while
// non-exempt features keep their rate for MCGP.
func TestMCGPExempt(t *testing.T) {
	svc, _ := newSeededService(t)

	// product_merchant is MCGP-exempt.
	bps, flat, exempt, found := svc.RateFor(models.FeatureProductMerchant, "MCGP", nil)
	if !found || !exempt || bps != 0 || flat != 0 {
		t.Errorf("MCGP product_merchant: found=%v exempt=%v bps=%d flat=%v, want found=true exempt=true 0/0", found, exempt, bps, flat)
	}

	// product flat gas fee is MCGP-exempt.
	if flat, exempt := FeeFlatUSD(models.FeatureProduct, "MCGP", nil); flat != 0 || !exempt {
		t.Errorf("MCGP product flat=%v exempt=%v, want 0/true", flat, exempt)
	}

	// withdrawal is NOT MCGP-exempt — MCGP keeps the 0.5% rate.
	wbps, _, wexempt, _ := svc.RateFor(models.FeatureWithdrawal, "MCGP", nil)
	if wbps != 50 || wexempt {
		t.Errorf("MCGP withdrawal bps=%d exempt=%v, want 50/false", wbps, wexempt)
	}
}

func TestInvalidateRefreshesCache(t *testing.T) {
	svc, db := newSeededService(t)

	if bps, _ := FeeRateViaService(svc, models.FeatureWithdrawal, "BTC"); bps != 50 {
		t.Fatalf("pre-update bps=%d, want 50", bps)
	}
	// Bump withdrawal wildcard to 75 bps directly in the DB.
	if err := db.Model(&models.FeeConfig{}).
		Where("feature = ? AND token_symbol = ?", models.FeatureWithdrawal, models.FeeTokenAny).
		Update("bps", 75).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	// Stale cache still reads 50 until invalidated.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", nil); bps != 50 {
		t.Fatalf("cache should be stale at 50, got %d", bps)
	}
	if err := svc.Invalidate(); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", nil); bps != 75 {
		t.Fatalf("post-invalidate bps=%d, want 75", bps)
	}
}

// FeeRateViaService is a tiny helper so the test reads from a specific service
// instance (not the package singleton).
func FeeRateViaService(s *FeeConfigService, feature, token string) (int, bool) {
	b, _, e, _ := s.RateFor(feature, token, nil)
	return b, e
}

// ---------- Behavior-preservation checks ----------
//
// Each refactored call site must compute the SAME fee as the old hardcoded
// constant for representative inputs. These reproduce the exact arithmetic of
// each site against the seeded service.

// productMerchantFee reproduces CalculatePlatformFee's big.Int math.
func productMerchantFee(svc *FeeConfigService, productAmount *big.Int, token string) *big.Int {
	bps, _, _, _ := svc.RateFor(models.FeatureProductMerchant, token, nil)
	fee := new(big.Int).Mul(productAmount, big.NewInt(int64(bps)))
	fee.Div(fee, big.NewInt(10000))
	return fee
}

func TestBehaviorPreservation_ProductMerchantFee(t *testing.T) {
	svc, _ := newSeededService(t)

	type tc struct {
		amount *big.Int
		token  string
	}
	cases := []tc{
		{big.NewInt(10000000), "USDC"},     // 10 USDC (6dp) -> 2% = 200000
		{big.NewInt(15500000), "USDT"},     // 15.5 USDT -> 310000
		{big.NewInt(1), "USDC"},            // rounds down to 0
		{big.NewInt(10000000000000000), "MCGP"}, // MCGP exempt -> 0
	}
	for _, c := range cases {
		// Old behavior: MCGP -> 0; else amount*200/10000.
		var want *big.Int
		if c.token == "MCGP" {
			want = big.NewInt(0)
		} else {
			want = new(big.Int).Mul(c.amount, big.NewInt(int64(models.MerchantFeeBPS)))
			want.Div(want, big.NewInt(10000))
		}
		got := productMerchantFee(svc, c.amount, c.token)
		if got.Cmp(want) != 0 {
			t.Errorf("product_merchant fee for %s %s: got %s want %s", c.amount, c.token, got, want)
		}
	}
}

func TestBehaviorPreservation_WithdrawalFee(t *testing.T) {
	svc, _ := newSeededService(t)
	for _, amount := range []float64{100, 250.75, 1_000_000, 0.01, 33.33} {
		bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", nil)
		got := amount * (float64(bps) / 10000.0)
		want := amount * 0.005 // old literal
		if got != want {
			t.Errorf("withdrawal fee for %v: got %v want %v", amount, got, want)
		}
	}
}

func TestBehaviorPreservation_SwapFee(t *testing.T) {
	svc, _ := newSeededService(t)
	for _, amount := range []float64{100, 250.75, 1_000_000, 0.01, 33.33} {
		bps, _, _, _ := svc.RateFor(models.FeatureSwap, "ETH", nil)
		got := amount * (float64(bps) / 10000.0)
		want := amount * 0.001 // old literal
		if got != want {
			t.Errorf("swap fee for %v: got %v want %v", amount, got, want)
		}
	}
}

func TestBehaviorPreservation_ServiceContactFlat(t *testing.T) {
	svc, _ := newSeededService(t)
	_, flat, _, _ := svc.RateFor(models.FeatureServiceContact, "USDC", nil)
	got := big.NewInt(int64(math.Round(flat * 1e6)))
	want := big.NewInt(100000) // old GetFeeAmount literal
	if got.Cmp(want) != 0 {
		t.Errorf("service_contact fee amount: got %s want %s", got, want)
	}
}

func TestBehaviorPreservation_ProductGasFee(t *testing.T) {
	svc, _ := newSeededService(t)

	// Non-MCGP: flat == GasFeeUSD ($0.10); 6dp wei == 100000.
	_, flat, _, _ := svc.RateFor(models.FeatureProduct, "USDC", nil)
	if flat != models.GasFeeUSD {
		t.Errorf("product gas flat=%v, want %v", flat, models.GasFeeUSD)
	}
	gotWei := int64(math.Round(flat * 1e6))
	wantWei := int64(math.Round(models.GasFeeUSD * 1e6))
	if gotWei != wantWei {
		t.Errorf("product gas wei=%d, want %d", gotWei, wantWei)
	}

	// MCGP: exempt -> flat 0 (old code added no gas fee for MCGP).
	_, mcgpFlat, mcgpExempt, _ := svc.RateFor(models.FeatureProduct, "MCGP", nil)
	if mcgpFlat != 0 || !mcgpExempt {
		t.Errorf("MCGP product gas flat=%v exempt=%v, want 0/true", mcgpFlat, mcgpExempt)
	}
}

// TestSeedMatchesCartConstants is the guard the cart.go SP1 notes rely on.
//
// models.CalculateCartSummary (the buyer-facing quote) and models.GetFeeConfig
// (the displayed fee schedule) still read the hardcoded constants because the
// models package cannot import services (import cycle). The actual on-chain
// CHARGE reads the fee_config service. Those two sources agree ONLY while the
// seed equals the constants. This test fails loudly if anyone changes one
// without the other — turning the silent quote-vs-charge divergence finding #2
// names into a build break instead of a production money bug.
//
// (Note: an admin editing a row at runtime still creates divergence — that is
// the documented FOLLOW-UP in cart.go, out of scope for SP1's behavior-
// preserving refactor. This guard only covers the source-of-truth seed.)
func TestSeedMatchesCartConstants(t *testing.T) {
	svc, _ := newSeededService(t)

	// Buyer product platform fee — CalculateCartSummary sets PlatformFee = 0.
	if bps, _, _, _ := svc.RateFor(models.FeatureProduct, "USDC", nil); bps != 0 {
		t.Errorf("product bps=%d, want 0 (cart buyer PlatformFee is 0)", bps)
	}

	// Per-order flat gas fee — CalculateCartSummary uses models.GasFeeUSD and
	// GetFeeConfig reports it as gasFeeUSD.
	if flat, _ := FeeFlatUSD(models.FeatureProduct, "USDC", nil); flat != models.GasFeeUSD {
		t.Errorf("product flat=%v, want GasFeeUSD=%v", flat, models.GasFeeUSD)
	}

	// Merchant fee — GetFeeConfig reports merchantFeeBPS = models.MerchantFeeBPS.
	if bps, _ := FeeRate(models.FeatureProductMerchant, "USDC", nil); bps != models.MerchantFeeBPS {
		t.Errorf("product_merchant bps=%d, want MerchantFeeBPS=%d", bps, models.MerchantFeeBPS)
	}

	// MCGP merchant fee — GetFeeConfig reports mcgpMerchantFeeBPS = 0; the
	// product_merchant row is MCGP-exempt, so MCGP resolves to bps=0/exempt.
	if bps, exempt := FeeRate(models.FeatureProductMerchant, "MCGP", nil); bps != models.MCGPMerchantFeeBPS || !exempt {
		t.Errorf("MCGP product_merchant bps=%d exempt=%v, want %d/true", bps, exempt, models.MCGPMerchantFeeBPS)
	}
}
