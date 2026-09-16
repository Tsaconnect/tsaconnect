package services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The gasless/transaction-tax spec prices a flow by DIRECTION as well as by
// (feature, token, chain): the PM's schedule taxes an MCGP sell but not an MCGP
// buy, an OTC buy differently from an OTC sell, and so on. Direction is the
// most-specific dimension — a direction row overrides the direction-agnostic
// row for the same (feature, token, chain).
//
// The load-bearing property for shipping this ahead of the rate decisions:
// with NO direction rows present, every lookup resolves exactly as before.

func setupFeeConfigDirectionTestDB(t *testing.T) *gorm.DB {
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
		direction TEXT,
		bps INTEGER NOT NULL DEFAULT 0,
		flat_amount_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME,
		UNIQUE(feature, token_symbol, chain_id, direction)
	)`).Error; err != nil {
		t.Fatalf("create fee_configs: %v", err)
	}
	return db
}

func insertDirRow(t *testing.T, db *gorm.DB, feature, token string, chainID *int64, direction string, bps int) {
	t.Helper()
	row := models.FeeConfig{
		ID:          uuid.New(),
		Feature:     feature,
		TokenSymbol: token,
		ChainID:     chainID,
		BPS:         bps,
		Active:      true,
	}
	if direction != "" {
		d := direction
		row.Direction = &d
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert %s/%s/%s: %v", feature, token, direction, err)
	}
}

func TestRateForDirection_DirectionRowBeatsAgnosticRow(t *testing.T) {
	db := setupFeeConfigDirectionTestDB(t)
	svc := NewFeeConfigService(db)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, "", 100)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, models.FeeDirectionSell, 250)
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	bps, _, _, found := svc.RateForDirection(models.FeatureOTC, "MCGP", nil, models.FeeDirectionSell)
	if !found || bps != 250 {
		t.Fatalf("sell: got (%d, %v), want (250, true)", bps, found)
	}
}

func TestRateForDirection_FallsBackToAgnosticRow(t *testing.T) {
	db := setupFeeConfigDirectionTestDB(t)
	svc := NewFeeConfigService(db)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, "", 100)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, models.FeeDirectionSell, 250)
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// No buy-specific row — the direction-agnostic row answers.
	bps, _, _, found := svc.RateForDirection(models.FeatureOTC, "MCGP", nil, models.FeeDirectionBuy)
	if !found || bps != 100 {
		t.Fatalf("buy: got (%d, %v), want (100, true)", bps, found)
	}
}

func TestRateFor_IgnoresDirectionSpecificRows(t *testing.T) {
	db := setupFeeConfigDirectionTestDB(t)
	svc := NewFeeConfigService(db)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, "", 100)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, models.FeeDirectionSell, 250)
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Every existing caller asks without a direction. It must never pick up a
	// directional rate it did not ask for — that would silently reprice flows
	// that have not been migrated to the directional call yet.
	bps, _, _, found := svc.RateFor(models.FeatureOTC, "MCGP", nil)
	if !found || bps != 100 {
		t.Fatalf("agnostic: got (%d, %v), want (100, true)", bps, found)
	}
}

func TestRateForDirection_DirectionOutranksChainAndToken(t *testing.T) {
	db := setupFeeConfigDirectionTestDB(t)
	svc := NewFeeConfigService(db)
	sonic := int64(146)
	// A very specific agnostic row versus a less specific directional one.
	insertDirRow(t, db, models.FeatureSpot, "USDC", &sonic, "", 100)
	insertDirRow(t, db, models.FeatureSpot, "*", nil, models.FeeDirectionSell, 300)
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	bps, _, _, found := svc.RateForDirection(models.FeatureSpot, "USDC", &sonic, models.FeeDirectionSell)
	if !found || bps != 300 {
		t.Fatalf("got (%d, %v), want (300, true) — direction is the outermost dimension", bps, found)
	}
}

func TestRateForDirection_NoDirectionRowsMatchesLegacyResolution(t *testing.T) {
	db := setupFeeConfigDirectionTestDB(t)
	svc := NewFeeConfigService(db)
	sonic := int64(146)
	insertDirRow(t, db, models.FeatureP2P, "USDC", &sonic, "", 50)
	insertDirRow(t, db, models.FeatureP2P, "USDC", nil, "", 75)
	insertDirRow(t, db, models.FeatureP2P, "*", nil, "", 90)
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// With the table free of direction rows, a directional lookup must land on
	// exactly what the pre-direction ladder would have returned.
	for _, tc := range []struct {
		name    string
		token   string
		chain   *int64
		wantBPS int
	}{
		{"exact token + chain", "USDC", &sonic, 50},
		{"exact token, global", "USDC", nil, 75},
		{"wildcard token", "DAI", nil, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirBPS, _, _, dirFound := svc.RateForDirection(models.FeatureP2P, tc.token, tc.chain, models.FeeDirectionSell)
			legacyBPS, _, _, legacyFound := svc.RateFor(models.FeatureP2P, tc.token, tc.chain)
			if dirBPS != tc.wantBPS || !dirFound {
				t.Fatalf("directional: got (%d, %v), want (%d, true)", dirBPS, dirFound, tc.wantBPS)
			}
			if dirBPS != legacyBPS || dirFound != legacyFound {
				t.Fatalf("directional (%d, %v) diverged from legacy (%d, %v)", dirBPS, dirFound, legacyBPS, legacyFound)
			}
		})
	}
}

// The on-chain FeeManager keys rates by (feature, token) only — it has no
// direction concept. So the drift reconciler, which treats the chain as the
// source of truth and OVERWRITES the replica on mismatch, must never look at a
// directional row: it would compare a per-leg tax against a direction-agnostic
// on-chain rate, call it drift, and wipe the tax rate on the next pass.
// Both insertion orders are exercised: an unfiltered First() returns whichever
// row the engine reaches first, so a query without an explicit direction filter
// passes or fails on row order alone.
func TestReplicaRowFor_IgnoresDirectionalRows(t *testing.T) {
	sonic := int64(146)
	for _, tc := range []struct {
		name             string
		directionalFirst bool
	}{
		{"agnostic row inserted first", false},
		{"directional row inserted first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupFeeConfigDirectionTestDB(t)
			svc := NewFeeConfigService(db)
			if tc.directionalFirst {
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, models.FeeDirectionSell, 900)
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, "", 100)
			} else {
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, "", 100)
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, models.FeeDirectionSell, 900)
			}

			bps, _, _, _, hasRow := svc.replicaRowFor(models.FeatureOTC, "USDC", &sonic)
			if !hasRow {
				t.Fatal("expected the direction-agnostic row to be found")
			}
			if bps != 100 {
				t.Fatalf("got bps=%d, want 100 — the reconciler must only ever see the direction-agnostic row", bps)
			}
		})
	}
}

func TestFeeDirection_ValidatesAgainstTheKnownSet(t *testing.T) {
	for _, d := range []string{
		models.FeeDirectionBuy, models.FeeDirectionSell, models.FeeDirectionSend,
		models.FeeDirectionPay, models.FeeDirectionWithdraw,
	} {
		if !models.IsValidFeeDirection(d) {
			t.Errorf("%q should be a valid direction", d)
		}
	}
	// Empty means "any direction" and is stored as NULL, not as a value.
	if models.IsValidFeeDirection("") {
		t.Error(`"" must not validate as a direction — it is the NULL/any sentinel`)
	}
	if models.IsValidFeeDirection("swap") {
		t.Error(`"swap" is not in the PM's direction set and must not validate`)
	}
}

// WriteReplicaRowTx is the other half of the hazard replicaRowFor closed. The
// reconciler detects drift with a direction-filtered read, but it RESOLVES that
// drift through RefreshFromConfirmedRate → WriteReplicaRowTx, and the admin
// confirm endpoint (ConfirmAndApply) writes through the same function. An
// unfiltered First() there picks a row by UUID order, so a directional tax row
// can be the one the confirmed on-chain rate overwrites — the exact silent wipe
// the read-side filter was added to prevent, one call down.
//
// It also leaves the agnostic row stale, so the next reconciler pass sees the
// same drift and clobbers the tax row again, every hour, forever.
func TestWriteReplicaRowTx_NeverTouchesADirectionalRow(t *testing.T) {
	sonic := int64(146)
	for _, tc := range []struct {
		name             string
		directionalFirst bool
	}{
		{"agnostic row inserted first", false},
		{"directional row inserted first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupFeeConfigDirectionTestDB(t)
			svc := NewFeeConfigService(db)
			if tc.directionalFirst {
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, models.FeeDirectionSell, 900)
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, "", 100)
			} else {
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, "", 100)
				insertDirRow(t, db, models.FeatureOTC, "USDC", &sonic, models.FeeDirectionSell, 900)
			}

			// Chain truth for (otc, USDC) is 250 bps. Only the direction-agnostic
			// row has an on-chain counterpart, so only it may move.
			if err := svc.RefreshFromConfirmedRate(models.FeatureOTC, "USDC", &sonic, 250, 0, false, true); err != nil {
				t.Fatalf("RefreshFromConfirmedRate: %v", err)
			}

			var rows []models.FeeConfig
			if err := db.Where("feature = ? AND token_symbol = ?", models.FeatureOTC, "USDC").Find(&rows).Error; err != nil {
				t.Fatalf("read back: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("got %d rows, want 2 — the write must update in place, not insert a third", len(rows))
			}
			for _, r := range rows {
				if r.Direction == nil {
					if r.BPS != 250 {
						t.Errorf("agnostic row bps=%d, want 250 — chain truth must land on it", r.BPS)
					}
					continue
				}
				if r.BPS != 900 {
					t.Errorf("directional row bps=%d, want 900 — it has no on-chain counterpart and must never be overwritten", r.BPS)
				}
			}
		})
	}
}

// Direction is compared as a string key, so an unnormalised caller ("SELL", or
// a row hand-written as " Sell ") would miss its own row and fall through to the
// untaxed agnostic rate — a silent undercharge that looks like working config.
func TestRateForDirection_IsCaseAndSpaceInsensitive(t *testing.T) {
	db := setupFeeConfigDirectionTestDB(t)
	svc := NewFeeConfigService(db)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, "", 100)
	insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, " SELL ", 250)
	if err := svc.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	for _, d := range []string{"sell", "SELL", " Sell "} {
		bps, _, _, found := svc.RateForDirection(models.FeatureOTC, "MCGP", nil, d)
		if !found || bps != 250 {
			t.Errorf("direction %q: got (%d, %v), want (250, true)", d, bps, found)
		}
	}
}

// A non-NULL but EMPTY direction is the poisoning case: it is not NULL, so every
// `direction IS NULL` filter skips it, yet its in-memory key is the same "" the
// agnostic row uses — so it can shadow the real agnostic rate in the cache while
// being invisible to the reconciler. Load must refuse to cache any row whose
// direction is non-NULL and not a known direction.
func TestLoad_IgnoresRowsWithAnInvalidDirection(t *testing.T) {
	for _, bad := range []string{"", "swap", "BUYY"} {
		t.Run("direction="+bad, func(t *testing.T) {
			db := setupFeeConfigDirectionTestDB(t)
			svc := NewFeeConfigService(db)
			insertDirRow(t, db, models.FeatureOTC, "MCGP", nil, "", 100)
			// Insert the malformed row directly — insertDirRow maps "" to NULL.
			badRow := models.FeeConfig{
				ID: uuid.New(), Feature: models.FeatureOTC, TokenSymbol: "MCGP",
				Direction: &bad, BPS: 900, Active: true,
			}
			if err := db.Create(&badRow).Error; err != nil {
				t.Fatalf("insert bad row: %v", err)
			}
			if err := svc.Load(); err != nil {
				t.Fatalf("load: %v", err)
			}

			bps, _, _, found := svc.RateFor(models.FeatureOTC, "MCGP", nil)
			if !found || bps != 100 {
				t.Fatalf("agnostic lookup: got (%d, %v), want (100, true) — a malformed direction row must not shadow it", bps, found)
			}
		})
	}
}
