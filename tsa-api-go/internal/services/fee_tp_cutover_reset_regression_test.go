package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupCutoverRegressionDB builds an in-memory SQLite DB carrying the FULL v2
// schema: the SP3/SP4 accrual tables AND the SP5 payout tables AND the SP7
// reward_flags table — everything ResetForV2Cutover archives, deletes, or
// clears. It is the union of setupFeeDistTestDB + setupRewardTestDB plus
// reward_flags, so a single test can drive OnFeeSettled (accrual) → reset →
// ProcessUserRewards (payout) end-to-end.
func setupCutoverRegressionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			persona_inquiry_id TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE tp_earnings (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			source_user_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			fee_amount_usd REAL NOT NULL,
			percentage REAL NOT NULL,
			tp_earned REAL NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE cashback_earnings (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			source_user_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			amount_usd REAL NOT NULL,
			tx_hash TEXT,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE fee_distributions (
			id TEXT PRIMARY KEY,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			feature TEXT NOT NULL,
			token TEXT NOT NULL,
			fee_usd REAL NOT NULL,
			system_usd REAL NOT NULL,
			buyer_cashback_usd REAL NOT NULL,
			upline_cashback_usd REAL NOT NULL,
			tp_pool_usd REAL NOT NULL,
			global_pool_usd REAL NOT NULL DEFAULT 0,
			tp_per_usd REAL NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			UNIQUE(source_type, source_id)
		)`,
		`CREATE TABLE global_tp_pool (
			singleton INTEGER PRIMARY KEY,
			accrued_usd REAL NOT NULL DEFAULT 0,
			accrued_tp REAL NOT NULL DEFAULT 0,
			per_user_index_scaled INTEGER NOT NULL DEFAULT 0,
			indexed_tp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE global_tp_epochs (
			id TEXT PRIMARY KEY,
			epoch_at DATETIME NOT NULL,
			delta_tp REAL NOT NULL,
			user_count INTEGER NOT NULL,
			per_user_scaled INTEGER NOT NULL,
			index_after_scaled INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE global_tp_accruals (
			id TEXT PRIMARY KEY,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			reason TEXT NOT NULL,
			generation INTEGER NOT NULL DEFAULT -1,
			amount_usd REAL NOT NULL,
			amount_tp REAL NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE reward_entries (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			source_level INTEGER NOT NULL DEFAULT 0,
			source_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
			reward_usd REAL NOT NULL,
			mcgp_amount REAL NOT NULL,
			rate_usd_per_mcgp REAL NOT NULL,
			created_at DATETIME NOT NULL,
			UNIQUE(user_id, kind, source_level, source_id)
		)`,
		`CREATE TABLE reward_claimables (
			user_id TEXT PRIMARY KEY,
			claimable_mcgp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE user_reward_states (
			user_id TEXT PRIMARY KEY,
			highest_awarded_level INTEGER NOT NULL DEFAULT 0,
			dirty INTEGER NOT NULL DEFAULT 0,
			last_processed_at DATETIME,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE prize_dues (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			level INTEGER NOT NULL,
			prize TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at DATETIME NOT NULL,
			UNIQUE(user_id, level)
		)`,
		`CREATE TABLE reward_flags (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			reason TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'admin',
			status TEXT NOT NULL DEFAULT 'active',
			flagged_by TEXT,
			cleared_by TEXT,
			cleared_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

// rowCount is a tiny helper for asserting a table is empty.
func rowCount(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM " + table).Scan(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestCutoverReset_RegressionNoDoublePay is the ★ regression for BLOCKER 1+2:
//
//	seed pre-cutover state (cashback_earnings + tp_earnings + reward state via
//	OnFeeSettled), run ResetForV2Cutover, then run ProcessUserRewards and assert
//	  - claimable MCGP == 0 (no re-mint / double-pay), AND
//	  - every v2 table is empty/zeroed, AND
//	  - the live tp_earnings/cashback_earnings rows are GONE (archived in *_legacy).
//
// This proves the cutover both deletes the live ledgers (so SP5 can't see them)
// AND clears all v2 state (so a fresh accrual starts from zero).
func TestCutoverReset_RegressionNoDoublePay(t *testing.T) {
	db := setupCutoverRegressionDB(t)
	feeSvc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// Seed pre-cutover accrual: a 3-deep chain + a settled fee. OnFeeSettled
	// writes tp_earnings, cashback_earnings, fee_distributions, and bumps
	// tp_balance/cashback_balance — exactly the pre-cutover state we must wipe.
	users := buildFeeChain(t, db, 3)
	gen0 := users[0]
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "USDT", FeeUSD: 100,
		UserID: gen0.ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}
	if err := feeSvc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("seed OnFeeSettled: %v", err)
	}

	// Process rewards BEFORE the cutover so reward_entries / reward_claimables /
	// user_reward_states are populated (the pre-cutover v2 state we must clear).
	rs := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))
	if _, err := rs.ProcessUserRewards(context.Background(), gen0.ID); err != nil {
		t.Fatalf("pre-cutover ProcessUserRewards: %v", err)
	}
	// Add an anti-sybil flag too, to prove reward_flags is cleared.
	if err := db.Exec(`INSERT INTO reward_flags (id, user_id, reason, source, status, created_at, updated_at)
		VALUES (?, ?, 'seed', 'admin', 'active', ?, ?)`,
		uuid.New().String(), gen0.ID.String(), time.Now(), time.Now()).Error; err != nil {
		t.Fatalf("seed reward flag: %v", err)
	}

	// Preconditions: there IS pre-cutover state to wipe.
	if rowCount(t, db, "tp_earnings") == 0 {
		t.Fatal("precondition: expected tp_earnings rows before cutover")
	}
	if rowCount(t, db, "cashback_earnings") == 0 {
		t.Fatal("precondition: expected cashback_earnings rows before cutover")
	}
	if claimable(t, db, gen0.ID) == 0 {
		t.Fatal("precondition: expected non-zero claimable before cutover")
	}
	preTPRows := rowCount(t, db, "tp_earnings")
	preCBRows := rowCount(t, db, "cashback_earnings")

	// ── CUTOVER ──
	sum, err := ResetForV2Cutover(context.Background(), db, "2026-06-08-cutover", ResetConfirmPhrase)
	if err != nil {
		t.Fatalf("cutover reset: %v", err)
	}

	// Live ledgers archived AND deleted.
	if sum.TPEarningsArchived != preTPRows || sum.TPEarningsDeleted != preTPRows {
		t.Fatalf("tp_earnings: archived=%d deleted=%d, want %d/%d", sum.TPEarningsArchived, sum.TPEarningsDeleted, preTPRows, preTPRows)
	}
	if sum.CashbackArchived != preCBRows || sum.CashbackDeleted != preCBRows {
		t.Fatalf("cashback: archived=%d deleted=%d, want %d/%d", sum.CashbackArchived, sum.CashbackDeleted, preCBRows, preCBRows)
	}
	if rowCount(t, db, "tp_earnings") != 0 {
		t.Fatal("tp_earnings live rows not deleted")
	}
	if rowCount(t, db, "cashback_earnings") != 0 {
		t.Fatal("cashback_earnings live rows not deleted")
	}
	// History preserved in *_legacy.
	if rowCount(t, db, "tp_earnings_legacy") != preTPRows {
		t.Fatalf("tp_earnings_legacy = %d, want %d", rowCount(t, db, "tp_earnings_legacy"), preTPRows)
	}
	if rowCount(t, db, "cashback_earnings_legacy") != preCBRows {
		t.Fatalf("cashback_earnings_legacy = %d, want %d", rowCount(t, db, "cashback_earnings_legacy"), preCBRows)
	}

	// Every v2 state table empty/zeroed.
	for _, table := range []string{
		"reward_entries", "reward_claimables", "user_reward_states",
		"fee_distributions", "global_tp_accruals", "global_tp_epochs", "reward_flags",
	} {
		if got := rowCount(t, db, table); got != 0 {
			t.Fatalf("%s not cleared after cutover: %d rows", table, got)
		}
	}
	// Global pool singleton zeroed.
	var pool struct {
		AccruedTP          float64
		PerUserIndexScaled int64
		IndexedTP          float64
	}
	db.Raw(`SELECT accrued_tp AS accrued_tp, per_user_index_scaled AS per_user_index_scaled, indexed_tp AS indexed_tp FROM global_tp_pool`).Scan(&pool)
	if pool.AccruedTP != 0 || pool.PerUserIndexScaled != 0 || pool.IndexedTP != 0 {
		t.Fatalf("global_tp_pool not reset: %+v", pool)
	}
	// Balances zeroed + global index NULLed.
	for _, u := range users {
		if tpBalance(t, db, u.ID) != 0 || cashbackBalance(t, db, u.ID) != 0 {
			t.Fatalf("balances not zeroed for %s", u.ID)
		}
	}
	var nonNullIdx int64
	db.Raw(`SELECT COUNT(*) FROM users WHERE last_settled_global_index IS NOT NULL`).Scan(&nonNullIdx)
	if nonNullIdx != 0 {
		t.Fatalf("last_settled_global_index not NULLed for %d users", nonNullIdx)
	}

	// ── POST-CUTOVER: re-mint must NOT double-pay ──
	// tp_balance is 0 and there is no pending cashback (live rows deleted), so a
	// fresh ProcessUserRewards awards nothing — claimable stays 0.
	res, err := rs.ProcessUserRewards(context.Background(), gen0.ID)
	if err != nil {
		t.Fatalf("post-cutover ProcessUserRewards: %v", err)
	}
	if res.LevelsAwarded != 0 || res.CashbacksConverted != 0 {
		t.Fatalf("post-cutover awarded levels=%d cashbacks=%d, want 0/0 (double-pay!)", res.LevelsAwarded, res.CashbacksConverted)
	}
	if got := claimable(t, db, gen0.ID); got != 0 {
		t.Fatalf("post-cutover claimable MCGP = %g, want 0 (double-pay!)", got)
	}
}

// TestConvertPendingCashback_SkipsPreCutoverRows is the BLOCKER 1
// defense-in-depth: even if a pre-cutover cashback_earnings row SURVIVES the
// delete (a stray late-dated insert), SP5 must skip it because its created_at is
// before the cutover sentinel timestamp. A post-cutover row IS converted.
func TestConvertPendingCashback_SkipsPreCutoverRows(t *testing.T) {
	db := setupCutoverRegressionDB(t)
	u := mkRewardUser(t, db, 0) // no level rewards, just cashback

	// Run a cutover with no data so the sentinel timestamp exists as the floor.
	if _, err := ResetForV2Cutover(context.Background(), db, "floor-cutover", ResetConfirmPhrase); err != nil {
		t.Fatalf("cutover: %v", err)
	}
	floor, err := CutoverTimestamp(context.Background(), db)
	if err != nil {
		t.Fatalf("cutover ts: %v", err)
	}

	// A "stray" pre-cutover cashback row (created_at one hour BEFORE the floor)
	// that somehow survived — must be SKIPPED.
	stray := models.CashbackEarning{
		ID: uuid.New(), UserID: u.ID, SourceUserID: u.ID, SourceType: "checkout",
		SourceID: uuid.New(), AmountUSD: 10, CreatedAt: floor.Add(-time.Hour),
	}
	if err := db.Create(&stray).Error; err != nil {
		t.Fatalf("seed stray: %v", err)
	}
	// A legitimate post-cutover cashback row (after the floor) — must be CONVERTED.
	fresh := models.CashbackEarning{
		ID: uuid.New(), UserID: u.ID, SourceUserID: u.ID, SourceType: "checkout",
		SourceID: uuid.New(), AmountUSD: 6, CreatedAt: floor.Add(time.Hour),
	}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatalf("seed fresh: %v", err)
	}

	rs := NewRewardService(db, nil, &noopSettler{}, fixedPrice(2.0)) // $2 per MCGP
	res, err := rs.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	// Only the fresh ($6) row converts: 1 conversion, 6/2 = 3 MCGP. The stray is
	// skipped by the cutover floor.
	if res.CashbacksConverted != 1 {
		t.Fatalf("cashbacks converted = %d, want 1 (stray pre-cutover row must be skipped)", res.CashbacksConverted)
	}
	if got := claimable(t, db, u.ID); !approxEq(got, 3.0) {
		t.Fatalf("claimable = %g, want 3.0 (only the post-cutover $6 row)", got)
	}
}

// TestConvertPendingCashback_NoCutoverConvertsAll proves the floor is a no-op
// before any cutover has run: with the zero-time floor, EVERY cashback row
// converts — byte-for-byte the pre-cutover behavior.
func TestConvertPendingCashback_NoCutoverConvertsAll(t *testing.T) {
	db := setupCutoverRegressionDB(t)
	u := mkRewardUser(t, db, 0)

	// Two rows, one dated well in the past — with no cutover sentinel, the floor
	// is the zero time, so BOTH must convert.
	old := models.CashbackEarning{
		ID: uuid.New(), UserID: u.ID, SourceUserID: u.ID, SourceType: "checkout",
		SourceID: uuid.New(), AmountUSD: 4, CreatedAt: time.Now().Add(-72 * time.Hour),
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("seed old: %v", err)
	}
	addCashback(t, db, u.ID, 6)

	rs := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))
	res, err := rs.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.CashbacksConverted != 2 {
		t.Fatalf("cashbacks converted = %d, want 2 (no cutover ⇒ no floor)", res.CashbacksConverted)
	}
	if got := claimable(t, db, u.ID); !approxEq(got, 10.0) {
		t.Fatalf("claimable = %g, want 10.0", got)
	}
}
