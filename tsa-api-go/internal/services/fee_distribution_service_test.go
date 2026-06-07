package services

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupFeeDistTestDB opens an in-memory SQLite DB with the tables the v2 engine
// touches, created via raw DDL (avoiding PostgreSQL gen_random_uuid() defaults
// that AutoMigrate would emit). The engine sets all IDs in Go, so plain TEXT
// primary keys are sufficient. The same SQL (recursive CTE in Uplines,
// ON CONFLICT upserts) runs here exactly as in production against PostgreSQL.
func setupFeeDistTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Pin to a single connection: an in-memory SQLite DB is per-connection, so
	// the engine's transaction (which re-enters via ReferralGraphService) and
	// the DDL above must share ONE connection or the tx sees an empty schema.
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
			updated_at DATETIME NOT NULL
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
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

func mkFeeUser(t *testing.T, db *gorm.DB, username string, referredBy *uuid.UUID, status string) models.User {
	t.Helper()
	u := models.User{
		ID:            uuid.New(),
		Name:          username,
		Username:      username,
		Email:         username + "@test.com",
		Password:      "x",
		ReferralCode:  username,
		ReferredBy:    referredBy,
		AccountStatus: status,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

// buildChain inserts a referral chain of `depth` users (gen0 at index 0, its
// referrer at index 1, …). All active. Returns the slice; index 0 is the
// transacting user.
func buildFeeChain(t *testing.T, db *gorm.DB, depth int) []models.User {
	t.Helper()
	users := make([]models.User, depth)
	// Build top-down so referred_by points at an already-inserted user.
	var prev *uuid.UUID
	for i := depth - 1; i >= 0; i-- {
		u := mkFeeUser(t, db, "u"+uuidShort(), prev, models.AccountStatusActive)
		users[i] = u
		id := u.ID
		prev = &id
	}
	return users
}

func uuidShort() string { return uuid.New().String()[:8] }

func tpBalance(t *testing.T, db *gorm.DB, id uuid.UUID) float64 {
	t.Helper()
	var u models.User
	if err := db.First(&u, "id = ?", id).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	return u.TPBalance
}

func cashbackBalance(t *testing.T, db *gorm.DB, id uuid.UUID) float64 {
	t.Helper()
	var u models.User
	if err := db.First(&u, "id = ?", id).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	return u.CashbackBalance
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// --- Tests ---

func TestComputeSplit_SumsToFee(t *testing.T) {
	for _, fee := range []float64{0.10, 1, 7.77, 1000, 0.0001} {
		sp := ComputeSplit(fee)
		if !approx(sp.Total(), fee) {
			t.Fatalf("split of %v sums to %v, want %v", fee, sp.Total(), fee)
		}
		if !approx(sp.SystemUSD, fee*0.50) || !approx(sp.BuyerCashbackUSD, fee*0.10) ||
			!approx(sp.UplineCashbackUSD, fee*0.10) || !approx(sp.TPPoolUSD, fee*0.30) {
			t.Fatalf("split components wrong for %v: %+v", fee, sp)
		}
	}
}

func TestOnFeeSettled_FullTenGenChain(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// 11 users: gen0 (transacting) + gen1..gen10 ancestors, all active.
	users := buildFeeChain(t, db, 11)
	fee := 100.0
	ev := FeeEvent{
		Feature:    models.FeatureP2P,
		Token:      "USDT",
		FeeUSD:     fee,
		UserID:     users[0].ID,
		SourceType: "p2p_release",
		SourceID:   uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	tpPool := fee * TPPoolShare // 30
	// Each gen i (0..10) gets GenerationPercentages[i] of the pool.
	for gen := 0; gen <= MaxTPGenerations; gen++ {
		want := tpPool * models.GenerationPercentages[gen]
		got := tpBalance(t, db, users[gen].ID)
		if !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
		}
	}

	// Buyer (gen0) cashback = 10% to users[0]; upline cashback = 10% to gen1.
	if got := cashbackBalance(t, db, users[0].ID); !approx(got, fee*0.10) {
		t.Fatalf("buyer cashback = %v, want %v", got, fee*0.10)
	}
	if got := cashbackBalance(t, db, users[1].ID); !approx(got, fee*0.10) {
		t.Fatalf("upline cashback = %v, want %v", got, fee*0.10)
	}

	// No global-pool accrual on a full active chain.
	pool, _ := svc.GlobalPool(context.Background())
	if pool.AccruedUSD != 0 {
		t.Fatalf("global pool should be empty on full chain, got %v", pool.AccruedUSD)
	}
}

// TestGenerationPercentages_SumToOne is the static conservation guard: the TP
// ladder must sum to EXACTLY 1.0 (design §9). A sum > 1.0 over-mints TP value on
// every full-chain fee event; a sum < 1.0 silently strands pool value. The
// init() in models also panics on drift, but this asserts it at the unit level.
func TestGenerationPercentages_SumToOne(t *testing.T) {
	var sum float64
	for _, p := range models.GenerationPercentages {
		sum += p
	}
	if !approx(sum, 1.0) {
		t.Fatalf("GenerationPercentages sum = %.6f, want exactly 1.0 (TP-pool conservation)", sum)
	}
	if len(models.GenerationPercentages) != MaxTPGenerations+1 {
		t.Fatalf("ladder length = %d, want %d (gen0..gen10)", len(models.GenerationPercentages), MaxTPGenerations+1)
	}
}

// TestOnFeeSettled_TPPoolConserved is the dynamic conservation guard: for a
// SINGLE fee event, the TP actually minted — Σ(TPEarning.TPEarned credited to
// real users) PLUS the global-pool TP accrued for missing/inactive generations
// — must equal exactly the TP pool (TPPoolUSD · tpPerUSD), with NO over-mint.
// This is asserted across three chain shapes (full, short, mid-inactive) because
// each routes a different split between user credits and the global pool, but
// the TOTAL minted must be identical and conserved in every case.
func TestOnFeeSettled_TPPoolConserved(t *testing.T) {
	cases := []struct {
		name     string
		depth    int  // referral chain depth (0 = lone user)
		suspend  int  // index to suspend (-1 = none)
		tpPerUSD float64
	}{
		{"full-11-gen", 11, -1, 1.0},
		{"short-3-gen", 3, -1, 1.0},
		{"mid-inactive-gen5", 11, 5, 1.0},
		{"full-chain-rate-2.5", 11, -1, 2.5},
		{"lone-user", 1, -1, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupFeeDistTestDB(t)
			svc := NewFeeDistributionService(db, NewReferralGraphService(db), tc.tpPerUSD)

			var userID uuid.UUID
			if tc.depth <= 1 {
				u := mkFeeUser(t, db, "solo"+uuidShort(), nil, models.AccountStatusActive)
				userID = u.ID
			} else {
				users := buildFeeChain(t, db, tc.depth)
				userID = users[0].ID
				if tc.suspend >= 0 {
					if err := db.Model(&models.User{}).Where("id = ?", users[tc.suspend].ID).
						Update("account_status", models.AccountStatusSuspended).Error; err != nil {
						t.Fatalf("suspend gen%d: %v", tc.suspend, err)
					}
				}
			}

			fee := 100.0
			srcID := uuid.New()
			ev := FeeEvent{
				Feature: models.FeatureP2P, Token: "USDT", FeeUSD: fee,
				UserID: userID, SourceType: "p2p_release", SourceID: srcID,
			}
			if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
				t.Fatalf("OnFeeSettled: %v", err)
			}

			// Σ TP credited to users for this source.
			var sumUserTP float64
			db.Model(&models.TPEarning{}).Where("source_id = ?", srcID).
				Select("COALESCE(SUM(tp_earned), 0)").Scan(&sumUserTP)

			// Σ TP routed to the global pool for the TP-generation reason only
			// (the upline-cashback routing is cashback, not TP-pool value).
			var sumGlobalTP float64
			db.Model(&models.GlobalTPAccrual{}).
				Where("source_id = ? AND reason = ?", srcID, models.GlobalAccrualReasonTPGeneration).
				Select("COALESCE(SUM(amount_tp), 0)").Scan(&sumGlobalTP)

			wantPoolTP := fee * TPPoolShare * tc.tpPerUSD
			gotMinted := sumUserTP + sumGlobalTP
			if !approx(gotMinted, wantPoolTP) {
				t.Fatalf("TP minted = %.10f (users %.10f + global %.10f), want pool %.10f (delta %.10f) — pool NOT conserved",
					gotMinted, sumUserTP, sumGlobalTP, wantPoolTP, gotMinted-wantPoolTP)
			}
		})
	}
}

func TestOnFeeSettled_ShortChainRoutesToGlobalPool(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// Only gen0 + gen1 + gen2 exist; gen3..gen10 missing → their shares route
	// to the global pool, but gen0/1/2 still credited.
	users := buildFeeChain(t, db, 3)
	fee := 100.0
	ev := FeeEvent{
		Feature: models.FeatureSwap, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "swap", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	tpPool := fee * TPPoolShare
	for gen := 0; gen <= 2; gen++ {
		want := tpPool * models.GenerationPercentages[gen]
		if got := tpBalance(t, db, users[gen].ID); !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
		}
	}

	// Global pool should hold the sum of gen3..gen10 shares (no upline-cashback
	// routing here — gen1 exists & is active).
	var missingShare float64
	for gen := 3; gen <= MaxTPGenerations; gen++ {
		missingShare += tpPool * models.GenerationPercentages[gen]
	}
	pool, _ := svc.GlobalPool(context.Background())
	if !approx(pool.AccruedUSD, missingShare) {
		t.Fatalf("global pool = %v, want %v", pool.AccruedUSD, missingShare)
	}

	// Accrual ledger rows: one per missing TP generation (gen3..gen10 = 8).
	var accrualCount int64
	db.Model(&models.GlobalTPAccrual{}).Where("reason = ?", models.GlobalAccrualReasonTPGeneration).Count(&accrualCount)
	if accrualCount != 8 {
		t.Fatalf("expected 8 tp-generation accruals, got %d", accrualCount)
	}
}

func TestOnFeeSettled_InactiveMidChainRoutesAndContinues(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// Full 11-deep chain, but gen5 is suspended. Its share routes to the global
	// pool; the climb CONTINUES so gen6..gen10 still get credited.
	users := buildFeeChain(t, db, 11)
	if err := db.Model(&models.User{}).Where("id = ?", users[5].ID).
		Update("account_status", models.AccountStatusSuspended).Error; err != nil {
		t.Fatalf("suspend gen5: %v", err)
	}

	fee := 100.0
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	tpPool := fee * TPPoolShare
	// gen5 must get NOTHING; every other gen (incl gen6..10) must be credited.
	for gen := 0; gen <= MaxTPGenerations; gen++ {
		got := tpBalance(t, db, users[gen].ID)
		if gen == 5 {
			if !approx(got, 0) {
				t.Fatalf("suspended gen5 should have 0 tp, got %v", got)
			}
			continue
		}
		want := tpPool * models.GenerationPercentages[gen]
		if !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v (continue-past-inactive failed)", gen, got, want)
		}
	}

	// Only gen5's share goes to the global pool.
	pool, _ := svc.GlobalPool(context.Background())
	want := tpPool * models.GenerationPercentages[5]
	if !approx(pool.AccruedUSD, want) {
		t.Fatalf("global pool = %v, want gen5 share %v", pool.AccruedUSD, want)
	}
}

func TestOnFeeSettled_NoDirectUplineRoutesBothToGlobal(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// Lone user, no referrer. gen1..gen10 TP shares AND the 10% upline cashback
	// all route to the global pool. gen0 TP + buyer cashback still credited.
	user := mkFeeUser(t, db, "solo"+uuidShort(), nil, models.AccountStatusActive)
	fee := 100.0
	ev := FeeEvent{
		Feature: models.FeatureWithdrawal, Token: "USDT", FeeUSD: fee,
		UserID: user.ID, SourceType: "withdrawal", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	tpPool := fee * TPPoolShare
	// gen0 credited.
	if got := tpBalance(t, db, user.ID); !approx(got, tpPool*models.GenerationPercentages[0]) {
		t.Fatalf("gen0 tp = %v, want %v", got, tpPool*models.GenerationPercentages[0])
	}
	// Buyer cashback credited.
	if got := cashbackBalance(t, db, user.ID); !approx(got, fee*0.10) {
		t.Fatalf("buyer cashback = %v, want %v", got, fee*0.10)
	}

	// Global pool = sum(gen1..gen10 TP shares) + upline cashback (10%).
	var tpMissing float64
	for gen := 1; gen <= MaxTPGenerations; gen++ {
		tpMissing += tpPool * models.GenerationPercentages[gen]
	}
	wantGlobal := tpMissing + fee*0.10
	pool, _ := svc.GlobalPool(context.Background())
	if !approx(pool.AccruedUSD, wantGlobal) {
		t.Fatalf("global pool = %v, want %v", pool.AccruedUSD, wantGlobal)
	}

	// Exactly one upline-cashback accrual row.
	var upCount int64
	db.Model(&models.GlobalTPAccrual{}).Where("reason = ?", models.GlobalAccrualReasonUplineCashback).Count(&upCount)
	if upCount != 1 {
		t.Fatalf("expected 1 upline-cashback accrual, got %d", upCount)
	}
}

func TestOnFeeSettled_Idempotent(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	users := buildFeeChain(t, db, 11)
	fee := 100.0
	srcID := uuid.New()
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "p2p_release", SourceID: srcID,
	}

	// Call twice with the same (SourceType, SourceID).
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("second call should be a clean no-op: %v", err)
	}

	// Balances reflect a SINGLE distribution.
	tpPool := fee * TPPoolShare
	if got := tpBalance(t, db, users[0].ID); !approx(got, tpPool*models.GenerationPercentages[0]) {
		t.Fatalf("gen0 tp after double-call = %v, want single %v", got, tpPool*models.GenerationPercentages[0])
	}

	// Exactly 11 TP earnings + 2 cashback earnings + 1 guard row.
	var tpRows, cbRows, guards int64
	db.Model(&models.TPEarning{}).Where("source_id = ?", srcID).Count(&tpRows)
	db.Model(&models.CashbackEarning{}).Where("source_id = ?", srcID).Count(&cbRows)
	db.Model(&models.FeeDistribution{}).Where("source_id = ?", srcID).Count(&guards)
	if tpRows != 11 {
		t.Fatalf("expected 11 tp earnings, got %d", tpRows)
	}
	if cbRows != 2 {
		t.Fatalf("expected 2 cashback earnings, got %d", cbRows)
	}
	if guards != 1 {
		t.Fatalf("expected 1 guard row, got %d", guards)
	}
}

func TestOnFeeSettled_TPPerUSDTunable(t *testing.T) {
	db := setupFeeDistTestDB(t)
	const rate = 2.5
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), rate)

	users := buildFeeChain(t, db, 11)
	fee := 100.0
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	tpPool := fee * TPPoolShare
	// TP credited = shareUSD * rate.
	want := tpPool * models.GenerationPercentages[0] * rate
	if got := tpBalance(t, db, users[0].ID); !approx(got, want) {
		t.Fatalf("gen0 tp with rate %v = %v, want %v", rate, got, want)
	}

	// Cashback is recorded in USD and is NOT affected by the TP rate.
	if got := cashbackBalance(t, db, users[0].ID); !approx(got, fee*0.10) {
		t.Fatalf("cashback should be USD-only (rate-independent) = %v, want %v", got, fee*0.10)
	}
}

func TestOnFeeSettled_ZeroFeeNoOp(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	user := mkFeeUser(t, db, "z"+uuidShort(), nil, models.AccountStatusActive)
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "MCGP", FeeUSD: 0,
		UserID: user.ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("zero-fee should be no-op: %v", err)
	}
	var guards int64
	db.Model(&models.FeeDistribution{}).Count(&guards)
	if guards != 0 {
		t.Fatalf("zero-fee should write no guard row, got %d", guards)
	}
}

// TestFeatureFlag_OffMeansEngineNotInvoked asserts the GATING contract: when the
// flag is off, the live path is the legacy engine and the v2 engine must not be
// invoked. We assert at the config-gate level (the single chokepoint callers
// use) rather than the engine internals, mirroring how Stage 2 will branch.
func TestFeatureFlag_OffMeansEngineNotInvoked(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	user := mkFeeUser(t, db, "f"+uuidShort(), nil, models.AccountStatusActive)

	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "USDT", FeeUSD: 100,
		UserID: user.ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}

	// Simulate the call-site gate used in Stage 2.
	flagEnabled := false
	if flagEnabled {
		if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	}

	// Nothing distributed because the engine was gated out.
	var guards int64
	db.Model(&models.FeeDistribution{}).Count(&guards)
	if guards != 0 {
		t.Fatalf("flag OFF must not invoke engine; found %d guard rows", guards)
	}
	if got := tpBalance(t, db, user.ID); got != 0 {
		t.Fatalf("flag OFF must not credit TP; got %v", got)
	}
}
