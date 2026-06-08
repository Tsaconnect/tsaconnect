package services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupFeeConfigChainTestDB mirrors setupFeeConfigTestDB but adds the nullable
// chain_id column and the (feature, token_symbol, chain_id) unique index that
// the configurable-FeeManager design §4 introduces.
func setupFeeConfigChainTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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

func i64p(v int64) *int64 { return &v }

// newSeededChainService returns a FeeConfigService over a chain-aware seeded DB.
func newSeededChainService(t *testing.T) (*FeeConfigService, *gorm.DB) {
	t.Helper()
	db := setupFeeConfigChainTestDB(t)
	svc := NewFeeConfigService(db)
	return svc, db
}

// insertRow is a small helper to insert a fee_config row with an explicit chain.
func insertRow(t *testing.T, db *gorm.DB, feature, token string, chainID *int64, bps int) {
	t.Helper()
	row := models.FeeConfig{
		ID:          uuid.New(),
		Feature:     feature,
		TokenSymbol: token,
		ChainID:     chainID,
		BPS:         bps,
		Active:      true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert row %s/%s/%v: %v", feature, token, chainID, err)
	}
}

// TestRateForPerChainResolution covers the 4-tier resolution: a chain-specific
// override beats the global row; a token-specific row beats the wildcard; an
// unconfigured chain falls back to global; nil chainID resolves global directly.
func TestRateForPerChainResolution(t *testing.T) {
	svc, db := newSeededChainService(t)

	const bsc int64 = 56
	const sonic int64 = 146

	// Global withdrawal is the seeded 50 bps wildcard. Add a BSC override at 60.
	insertRow(t, db, models.FeatureWithdrawal, models.FeeTokenAny, i64p(bsc), 60)
	// Add a Sonic+USDT token+chain specific override at 70.
	insertRow(t, db, models.FeatureWithdrawal, "USDT", i64p(sonic), 70)
	if err := svc.Invalidate(); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	cases := []struct {
		name    string
		token   string
		chain   *int64
		wantBPS int
	}{
		{"bsc override beats global wildcard", "BTC", i64p(bsc), 60},
		{"sonic+usdt exact beats everything", "USDT", i64p(sonic), 70},
		{"sonic+btc has no chain override -> global 50", "BTC", i64p(sonic), 50},
		{"unconfigured chain -> global 50", "BTC", i64p(999), 50},
		{"nil chain -> global 50", "BTC", nil, 50},
		{"bsc+usdt: no token+chain row, chain wildcard 60 wins over global", "USDT", i64p(bsc), 60},
	}
	for _, tc := range cases {
		bps, _, _, found := svc.RateFor(models.FeatureWithdrawal, tc.token, tc.chain)
		if !found {
			t.Errorf("%s: not found", tc.name)
			continue
		}
		if bps != tc.wantBPS {
			t.Errorf("%s: bps=%d want %d", tc.name, bps, tc.wantBPS)
		}
	}
}

// TestFallbackLadderEmptyCacheReturnsSeedDefault is the §11 cold-start test: an
// empty cache with no data must return the per-feature SEED DEFAULT (non-zero,
// bounded) and fire the loud cache-miss alert — NEVER 0.
func TestFallbackLadderEmptyCacheReturnsSeedDefault(t *testing.T) {
	// A service with a nil DB never loads any rows and has no last-known-good,
	// so RateFor must fall all the way to the seed default.
	svc := &FeeConfigService{
		db:            nil,
		cache:         map[string]models.FeeConfig{},
		lastKnownGood: map[string]models.FeeConfig{},
	}

	before := FeeCacheMissTotal()

	// withdrawal seed default is 50 bps (non-zero, bounded).
	bps, _, _, found := svc.RateFor(models.FeatureWithdrawal, "BTC", i64p(56))
	if !found {
		t.Fatal("expected seed-default fallback to report found=true, got false")
	}
	if bps == 0 {
		t.Fatal("fallback returned 0 bps — MUST never charge $0")
	}
	if bps != 50 {
		t.Errorf("withdrawal seed default bps=%d, want 50", bps)
	}
	if bps > MaxFeeBPS {
		t.Errorf("seed default bps=%d exceeds MaxFeeBPS=%d", bps, MaxFeeBPS)
	}

	if FeeCacheMissTotal() <= before {
		t.Error("expected fee_rate_cache_miss_total to increment (loud alert hook), it did not")
	}

	// p2p_platform seed default is 100 bps — also non-zero.
	if pbps, _, _, pfound := svc.RateFor(models.FeatureP2PPlatform, "USDT", nil); !pfound || pbps != 100 {
		t.Errorf("p2p_platform seed default found=%v bps=%d, want true/100", pfound, pbps)
	}
}

// TestFallbackLadderLastKnownGood asserts that when the live cache is emptied
// (e.g. a transient reload) a previously good load still answers via the
// last-known-good tier rather than the seed.
func TestFallbackLadderLastKnownGood(t *testing.T) {
	svc, db := newSeededChainService(t)

	// Bump the withdrawal global to a distinctive 77 bps and reload so 77 is the
	// last-known-good.
	if err := db.Model(&models.FeeConfig{}).
		Where("feature = ? AND token_symbol = ? AND chain_id IS NULL", models.FeatureWithdrawal, models.FeeTokenAny).
		Update("bps", 77).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Now blow away the live cache (simulate cold/empty cache mid-reload) while
	// last-known-good stays populated.
	svc.mu.Lock()
	svc.cache = map[string]models.FeeConfig{}
	svc.mu.Unlock()

	before := FeeCacheMissTotal()
	bps, _, _, found := svc.RateFor(models.FeatureWithdrawal, "BTC", nil)
	if !found {
		t.Fatal("expected last-known-good to answer, found=false")
	}
	if bps != 77 {
		t.Errorf("last-known-good bps=%d, want 77 (not the seed 50)", bps)
	}
	if FeeCacheMissTotal() <= before {
		t.Error("expected cache-miss alert to fire on the last-known-good path")
	}
}

// TestRateForClampsOverMaxRow asserts a corrupt replica row above MaxFeeBPS is
// clamped, never charged unbounded (§11 step 4).
func TestRateForClampsOverMaxRow(t *testing.T) {
	svc, db := newSeededChainService(t)

	// Insert a corrupt withdrawal global at 50000 bps (500%!).
	if err := db.Model(&models.FeeConfig{}).
		Where("feature = ? AND token_symbol = ? AND chain_id IS NULL", models.FeatureWithdrawal, models.FeeTokenAny).
		Update("bps", 50000).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := svc.Invalidate(); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	bps, _, _, found := svc.RateFor(models.FeatureWithdrawal, "BTC", nil)
	if !found {
		t.Fatal("not found")
	}
	if bps != MaxFeeBPS {
		t.Errorf("corrupt over-max row bps=%d, want clamped to MaxFeeBPS=%d", bps, MaxFeeBPS)
	}

	// FeeRate helper must also clamp.
	if hbps, _ := FeeRateViaService(svc, models.FeatureWithdrawal, "BTC"); hbps != MaxFeeBPS {
		t.Errorf("FeeRateViaService over-max bps=%d, want %d", hbps, MaxFeeBPS)
	}
}

// TestRefreshFromConfirmedRateWritesAndInvalidates verifies the read-replica
// refresh path: it writes the confirmed value into fee_config (creating the row
// when absent) and the cache then serves it. This is what #43's confirm endpoint
// calls.
func TestRefreshFromConfirmedRateWritesAndInvalidates(t *testing.T) {
	svc, db := newSeededChainService(t)

	const bsc int64 = 56

	// No BSC withdrawal override exists yet -> resolves to global seed 50.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", i64p(bsc)); bps != 50 {
		t.Fatalf("pre-refresh BSC withdrawal bps=%d, want global 50", bps)
	}

	// Confirm an on-chain BSC override of 60 bps.
	if err := svc.RefreshFromConfirmedRate(models.FeatureWithdrawal, models.FeeTokenAny, i64p(bsc), 60, 0, false, true); err != nil {
		t.Fatalf("RefreshFromConfirmedRate create: %v", err)
	}

	// Cache must now serve the BSC-specific 60.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", i64p(bsc)); bps != 60 {
		t.Errorf("post-refresh BSC withdrawal bps=%d, want 60", bps)
	}
	// Global is untouched.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", nil); bps != 50 {
		t.Errorf("global withdrawal bps=%d, want still 50", bps)
	}

	// A second refresh UPDATES the existing BSC row in place (no duplicate).
	if err := svc.RefreshFromConfirmedRate(models.FeatureWithdrawal, models.FeeTokenAny, i64p(bsc), 80, 0, false, true); err != nil {
		t.Fatalf("RefreshFromConfirmedRate update: %v", err)
	}
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", i64p(bsc)); bps != 80 {
		t.Errorf("post-update BSC withdrawal bps=%d, want 80", bps)
	}
	var count int64
	db.Model(&models.FeeConfig{}).
		Where("feature = ? AND token_symbol = ? AND chain_id = ?", models.FeatureWithdrawal, models.FeeTokenAny, bsc).
		Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 BSC withdrawal row after two refreshes, got %d", count)
	}
}

// TestFlagOffUnaffected is the byte-for-byte guard: the §11 ladder and chain
// dimension must not change the rate a flag-OFF deployment resolves for the
// legacy global path. nil chainID + a populated replica returns exactly the
// seeded global rate, identical to the pre-change two-arg behavior.
func TestFlagOffUnaffected(t *testing.T) {
	svc, _ := newSeededChainService(t)

	// Every seeded feature resolved with nil chain == its seed value, with no
	// fallback-ladder miss fired (cache hit path).
	before := FeeCacheMissTotal()
	cases := []struct {
		feature string
		token   string
		wantBPS int
	}{
		{models.FeatureProductMerchant, "USDC", 200},
		{models.FeatureWithdrawal, "BTC", 50},
		{models.FeatureSwap, "ETH", 10},
		{models.FeatureP2P, "USDT", 50},
		{models.FeatureP2PPlatform, "USDT", 100},
	}
	for _, tc := range cases {
		bps, _, _, found := svc.RateFor(tc.feature, tc.token, nil)
		if !found || bps != tc.wantBPS {
			t.Errorf("%s/%s nil-chain: found=%v bps=%d, want true/%d", tc.feature, tc.token, found, bps, tc.wantBPS)
		}
	}
	if FeeCacheMissTotal() != before {
		t.Error("flag-off cache-hit path must NOT fire the fallback-ladder alert")
	}
}
