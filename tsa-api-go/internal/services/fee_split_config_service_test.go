package services

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSplitConfigDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE fee_split_configs (
		id TEXT PRIMARY KEY,
		feature TEXT NOT NULL UNIQUE,
		system_share REAL NOT NULL,
		buyer_share REAL NOT NULL,
		upline_share REAL NOT NULL,
		provider_share REAL NOT NULL,
		tp_share REAL NOT NULL,
		active INTEGER NOT NULL DEFAULT 1,
		updated_by TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return db
}

func splitCfg(feature string, system, buyer, upline, provider, tp float64) models.FeeSplitConfig {
	return models.FeeSplitConfig{
		Feature: feature, SystemShare: system, BuyerShare: buyer,
		UplineShare: upline, ProviderShare: provider, TPShare: tp, Active: true,
	}
}

func TestValidFeeSplit(t *testing.T) {
	if !models.ValidFeeSplit(splitCfg("spot", 0.70, 0, 0, 0, 0.30)) {
		t.Fatal("a split summing to 1.0 must be valid")
	}
	// Under- and over-committing both leak value; neither may be stored.
	if models.ValidFeeSplit(splitCfg("spot", 0.70, 0, 0, 0, 0.20)) {
		t.Fatal("0.9 total must be rejected")
	}
	if models.ValidFeeSplit(splitCfg("spot", 0.80, 0, 0, 0, 0.30)) {
		t.Fatal("1.1 total must be rejected")
	}
	if models.ValidFeeSplit(splitCfg("spot", 1.30, 0, 0, 0, -0.30)) {
		t.Fatal("a negative share must be rejected even when the sum is 1.0")
	}
}

func TestUpsertRejectsInvalidSplit(t *testing.T) {
	db := setupSplitConfigDB(t)
	svc := NewFeeSplitConfigService(db)

	if _, err := svc.Upsert(splitCfg("spot", 0.70, 0, 0, 0, 0.20)); err == nil {
		t.Fatal("expected an error for a split that doesn't sum to 1.0")
	}
	var n int64
	db.Model(&models.FeeSplitConfig{}).Count(&n)
	if n != 0 {
		t.Fatalf("invalid split must not persist; got %d rows", n)
	}
}

func TestUpsertThenEffectiveOverridesDefault(t *testing.T) {
	db := setupSplitConfigDB(t)
	svc := NewFeeSplitConfigService(db)

	defaults := DefaultFeeSplits()
	if defaults[models.FeatureSpot].TP != 0.30 {
		t.Fatalf("precondition: default spot TP share = %v, want 0.30", defaults[models.FeatureSpot].TP)
	}

	// Halve spot's TP share; system takes the difference.
	if _, err := svc.Upsert(splitCfg(models.FeatureSpot, 0.85, 0, 0, 0, 0.15)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	eff := svc.Effective()
	if eff[models.FeatureSpot].TP != 0.15 {
		t.Fatalf("effective spot TP = %v, want the override 0.15", eff[models.FeatureSpot].TP)
	}
	// Untouched features still read their compiled-in default.
	if eff[models.FeatureOTC].TP != defaults[models.FeatureOTC].TP {
		t.Fatal("an override for one feature must not disturb another")
	}
}

func TestInactiveAndInvalidRowsFallBackToDefaults(t *testing.T) {
	db := setupSplitConfigDB(t)
	svc := NewFeeSplitConfigService(db)

	// Inactive override is ignored (the documented escape hatch).
	inactive := splitCfg(models.FeatureSpot, 0.85, 0, 0, 0, 0.15)
	inactive.Active = false
	if _, err := svc.Upsert(inactive); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if svc.Effective()[models.FeatureSpot].TP != DefaultFeeSplits()[models.FeatureSpot].TP {
		t.Fatal("an inactive override must fall back to the default")
	}

	// A row hand-edited in the DB to an invalid split bypasses Upsert's check;
	// applying it would leak value, so it must be ignored too.
	if err := db.Exec(
		`UPDATE fee_split_configs SET active = 1, tp_share = 0.90 WHERE feature = ?`,
		models.FeatureSpot).Error; err != nil {
		t.Fatalf("hand-edit: %v", err)
	}
	svc.Invalidate()
	if svc.Effective()[models.FeatureSpot].TP != DefaultFeeSplits()[models.FeatureSpot].TP {
		t.Fatal("an invalid stored override must fall back to the default, not be applied")
	}
}

func TestEngineAppliesOverride(t *testing.T) {
	db := setupSplitConfigDB(t)
	cfgSvc := NewFeeSplitConfigService(db)
	if _, err := cfgSvc.Upsert(splitCfg(models.FeatureSpot, 0.85, 0, 0, 0, 0.15)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	engine := NewFeeDistributionService(db, NewReferralGraphService(db), 100).WithSplitConfigs(cfgSvc)

	// TP is carved from System+TP (what the platform keeps), so moving the
	// per-feature TP share no longer changes how much TP a fee mints: 12% of
	// the $10 kept either way. The override still decides cashback shares.
	got := computeSplitWith(engine.splitForFeature(models.FeatureSpot), 10, models.DefaultTPProgramConfig())
	if !approx(got.TPPoolUSD, 1.2) {
		t.Fatalf("TP pool = %v, want 1.2 under the override", got.TPPoolUSD)
	}
	if d := got.Total() - 10; d > 1e-9 || d < -1e-9 {
		t.Fatalf("split must still conserve the fee; total = %v", got.Total())
	}

	// Without the config service the engine keeps compiled-in behaviour.
	plain := NewFeeDistributionService(db, NewReferralGraphService(db), 100)
	if plain.splitForFeature(models.FeatureSpot).TP != DefaultFeeSplits()[models.FeatureSpot].TP {
		t.Fatal("an engine with no split-config service must use defaults")
	}
}
