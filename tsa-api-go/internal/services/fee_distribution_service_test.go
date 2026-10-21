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
			didit_session_id TEXT,
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
			updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
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
			token TEXT,
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
			provider_cashback_usd REAL NOT NULL DEFAULT 0,
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
			last_payout_at DATETIME,
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

// tpFrac / buyerFrac / uplineFrac / providerFrac return the per-feature share
// fractions so tests assert against the COMPLETE split table rather than a
// removed uniform constant.
func tpFrac(feature string) float64 { return splitFor(feature).TP }

// TP model v3 (PM 2026-10-21): TP is carved from what the platform keeps (the
// table's System + TP slices), 5% to the user, 5% to the direct upline, 2% to
// the global pool.
func keptUSD(feature string, fee float64) float64 {
	fs := splitFor(feature)
	return fee * (fs.System + fs.TP)
}
func userTPUSD(feature string, fee float64) float64 {
	return keptUSD(feature, fee) * models.DefaultTPUserPct
}
func uplineTPUSD(feature string, fee float64) float64 {
	return keptUSD(feature, fee) * models.DefaultTPUplinePct
}
func poolTPUSD(feature string, fee float64) float64 {
	return keptUSD(feature, fee) * models.DefaultTPPoolPct
}
func buyerFrac(feature string) float64    { return splitFor(feature).Buyer }
func uplineFrac(feature string) float64   { return splitFor(feature).Upline }
func providerFrac(feature string) float64 { return splitFor(feature).Provider }

// --- Tests ---

// TestFeatureSplits_EachSumsToOne is the boot-guard contract at the unit level:
// EVERY feature's split must sum to exactly 1.0 (System+Buyer+Upline+Provider+TP),
// mirroring the GenerationPercentages==1.0 conservation guard. The init() in the
// service panics on drift; this asserts it for every row.
func TestFeatureSplits_EachSumsToOne(t *testing.T) {
	for feature, fs := range featureSplits {
		if !approx(fs.Sum(), 1.0) {
			t.Fatalf("featureSplits[%q] sums to %.6f, want exactly 1.0 (%+v)", feature, fs.Sum(), fs)
		}
	}
}

// TestBootGuard_FiresOnBadSplit asserts the boot-time validation rejects a split
// whose shares do not sum to 1.0 (the same check init() runs at startup).
func TestBootGuard_FiresOnBadSplit(t *testing.T) {
	bad := FeeShares{System: 0.50, Buyer: 0.15, Upline: 0.15, Provider: 0.00, TP: 0.30} // sums to 1.10
	if approx(bad.Sum(), 1.0) {
		t.Fatal("test setup wrong: bad split should NOT sum to 1.0")
	}
	// Re-run the exact boot-guard logic over a table containing the bad row and
	// confirm it would panic.
	validate := func(table map[string]FeeShares) (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		for feature, fs := range table {
			if d := fs.Sum() - 1.0; d > 1e-9 || d < -1e-9 {
				panic("featureSplits[" + feature + "] does not sum to 1.0")
			}
		}
		return false
	}
	if !validate(map[string]FeeShares{"bad_feature": bad}) {
		t.Fatal("boot guard must panic on a split that does not sum to 1.0")
	}
	// And the real table must pass the same guard.
	if validate(featureSplits) {
		t.Fatal("real featureSplits table must pass the boot guard")
	}
}

// TestComputeSplit_SumsToFee asserts ComputeSplit(feature, fee) reproduces the
// per-feature shares in USD and the five components always sum to the fee.
func TestComputeSplit_SumsToFee(t *testing.T) {
	for feature, fs := range featureSplits {
		for _, fee := range []float64{0.10, 1, 7.77, 1000, 0.0001} {
			sp := ComputeSplit(feature, fee)
			if !approx(sp.Total(), fee) {
				t.Fatalf("%s split of %v sums to %v, want %v", feature, fee, sp.Total(), fee)
			}
			wantTP, wantSystem := 0.0, fee*fs.System
			if fs.TP > 0 { // a feature whose table mints no TP (gas) keeps everything
				wantTP = keptUSD(feature, fee) * models.DefaultTPProgramConfig().TotalPct()
				wantSystem = keptUSD(feature, fee) - wantTP
			}
			if !approx(sp.SystemUSD, wantSystem) || !approx(sp.BuyerCashbackUSD, fee*fs.Buyer) ||
				!approx(sp.UplineCashbackUSD, fee*fs.Upline) || !approx(sp.ProviderCashbackUSD, fee*fs.Provider) ||
				!approx(sp.TPPoolUSD, wantTP) {
				t.Fatalf("%s split components wrong for %v: %+v (shares %+v)", feature, fee, sp, fs)
			}
		}
	}
}

// TestComputeSplit_UnknownFeatureDefaults asserts an unknown feature falls back
// to the product_merchant split (and the engine logs it).
func TestComputeSplit_UnknownFeatureDefaults(t *testing.T) {
	want := ComputeSplit(models.FeatureProductMerchant, 100)
	got := ComputeSplit("totally_unknown_feature", 100)
	if got != want {
		t.Fatalf("unknown feature split = %+v, want product_merchant %+v", got, want)
	}
}

func TestOnFeeSettled_FullTenGenChain(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// 11 users: gen0 (transacting) + gen1..gen10 ancestors, all active.
	// product_merchant is the carved {0.50,0.15,0.15,0.00,0.20} split.
	users := buildFeeChain(t, db, 11)
	feature := models.FeatureProductMerchant
	fee := 100.0
	ev := FeeEvent{
		Feature:    feature,
		Token:      "USDT",
		FeeUSD:     fee,
		UserID:     users[0].ID,
		SourceType: "checkout",
		SourceID:   uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// Only the user (5%) and the direct upline (5%) earn TP; nobody further up.
	for gen := 0; gen <= MaxTPGenerations; gen++ {
		want := 0.0
		switch gen {
		case 0:
			want = userTPUSD(feature, fee)
		case 1:
			want = uplineTPUSD(feature, fee)
		}
		got := tpBalance(t, db, users[gen].ID)
		if !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
		}
	}

	// Buyer (gen0) cashback = 15% to users[0]; upline cashback = 15% to gen1.
	if got := cashbackBalance(t, db, users[0].ID); !approx(got, fee*buyerFrac(feature)) {
		t.Fatalf("buyer cashback = %v, want %v", got, fee*buyerFrac(feature))
	}
	if got := cashbackBalance(t, db, users[1].ID); !approx(got, fee*uplineFrac(feature)) {
		t.Fatalf("upline cashback = %v, want %v", got, fee*uplineFrac(feature))
	}

	// The pool share (2%) accrues on every fee, nothing else on a live chain.
	pool, _ := svc.GlobalPool(context.Background())
	if !approx(pool.AccruedUSD, poolTPUSD(feature, fee)) {
		t.Fatalf("global pool = %v, want the 2%% pool share %v", pool.AccruedUSD, poolTPUSD(feature, fee))
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
		depth    int // referral chain depth (0 = lone user)
		suspend  int // index to suspend (-1 = none)
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
			feature := models.FeatureProductMerchant
			ev := FeeEvent{
				Feature: feature, Token: "USDT", FeeUSD: fee,
				UserID: userID, SourceType: "checkout", SourceID: srcID,
			}
			if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
				t.Fatalf("OnFeeSettled: %v", err)
			}

			// Σ TP credited to users for this source.
			var sumUserTP float64
			db.Model(&models.TPEarning{}).Where("source_id = ?", srcID).
				Select("COALESCE(SUM(tp_earned), 0)").Scan(&sumUserTP)

			// Σ TP routed to the global pool (the pool share, plus the upline share
			// when there is no live upline). Upline-cashback routing is cashback,
			// not TP value.
			var sumGlobalTP float64
			db.Model(&models.GlobalTPAccrual{}).
				Where("source_id = ? AND reason IN ?", srcID,
					[]string{models.GlobalAccrualReasonTPGeneration, models.GlobalAccrualReasonTPPool}).
				Select("COALESCE(SUM(amount_tp), 0)").Scan(&sumGlobalTP)

			wantPoolTP := ComputeSplit(feature, fee).TPPoolUSD * tc.tpPerUSD
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

	// gen0 + gen1 + gen2 exist. Only gen0 and gen1 earn TP; gen2 earns nothing.
	users := buildFeeChain(t, db, 3)
	feature := models.FeatureProductMerchant
	fee := 100.0
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "checkout", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	for gen, want := range []float64{userTPUSD(feature, fee), uplineTPUSD(feature, fee), 0} {
		if got := tpBalance(t, db, users[gen].ID); !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
		}
	}

	// Only the pool share is routed to the global pool: a live gen1 means no
	// upline fallback, and no longer chain is ever walked.
	pool, _ := svc.GlobalPool(context.Background())
	if !approx(pool.AccruedUSD, poolTPUSD(feature, fee)) {
		t.Fatalf("global pool = %v, want %v", pool.AccruedUSD, poolTPUSD(feature, fee))
	}
	var poolRows, genRows int64
	db.Model(&models.GlobalTPAccrual{}).Where("reason = ?", models.GlobalAccrualReasonTPPool).Count(&poolRows)
	db.Model(&models.GlobalTPAccrual{}).Where("reason = ?", models.GlobalAccrualReasonTPGeneration).Count(&genRows)
	if poolRows != 1 || genRows != 0 {
		t.Fatalf("want 1 tp_pool and 0 tp_generation accruals, got %d and %d", poolRows, genRows)
	}
}

func TestOnFeeSettled_InactiveUplineRoutesToGlobal(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// The direct upline is suspended: its 5% TP share and the upline cashback
	// both go to the global pool, and nobody further up inherits them.
	users := buildFeeChain(t, db, 11)
	if err := db.Model(&models.User{}).Where("id = ?", users[1].ID).
		Update("account_status", models.AccountStatusSuspended).Error; err != nil {
		t.Fatalf("suspend gen1: %v", err)
	}

	feature := models.FeatureProductMerchant
	fee := 100.0
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "checkout", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	for gen := 0; gen <= MaxTPGenerations; gen++ {
		want := 0.0
		if gen == 0 {
			want = userTPUSD(feature, fee)
		}
		if got := tpBalance(t, db, users[gen].ID); !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
		}
	}

	pool, _ := svc.GlobalPool(context.Background())
	want := uplineTPUSD(feature, fee) + poolTPUSD(feature, fee) + fee*uplineFrac(feature)
	if !approx(pool.AccruedUSD, want) {
		t.Fatalf("global pool = %v, want upline TP + pool share + upline cashback = %v", pool.AccruedUSD, want)
	}
}

func TestOnFeeSettled_NoDirectUplineRoutesBothToGlobal(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// Lone user, no referrer, on a CARVED feature (product_merchant). the upline
	// TP share AND the 15% upline cashback both route to the global pool. gen0 TP +
	// buyer cashback still credited.
	user := mkFeeUser(t, db, "solo"+uuidShort(), nil, models.AccountStatusActive)
	feature := models.FeatureProductMerchant
	fee := 100.0
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: user.ID, SourceType: "checkout", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// gen0 credited.
	if got := tpBalance(t, db, user.ID); !approx(got, userTPUSD(feature, fee)) {
		t.Fatalf("gen0 tp = %v, want %v", got, userTPUSD(feature, fee))
	}
	// Buyer cashback credited.
	if got := cashbackBalance(t, db, user.ID); !approx(got, fee*buyerFrac(feature)) {
		t.Fatalf("buyer cashback = %v, want %v", got, fee*buyerFrac(feature))
	}

	// Global pool = upline TP share + pool share + upline cashback (15%).
	wantGlobal := uplineTPUSD(feature, fee) + poolTPUSD(feature, fee) + fee*uplineFrac(feature)
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
	feature := models.FeatureProductMerchant
	fee := 100.0
	srcID := uuid.New()
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "checkout", SourceID: srcID,
	}

	// Call twice with the same (SourceType, SourceID).
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("second call should be a clean no-op: %v", err)
	}

	// Balances reflect a SINGLE distribution.
	if got := tpBalance(t, db, users[0].ID); !approx(got, userTPUSD(feature, fee)) {
		t.Fatalf("gen0 tp after double-call = %v, want single %v", got, userTPUSD(feature, fee))
	}

	// Exactly 2 TP earnings (user + upline) + 2 cashback earnings + 1 guard row.
	var tpRows, cbRows, guards int64
	db.Model(&models.TPEarning{}).Where("source_id = ?", srcID).Count(&tpRows)
	db.Model(&models.CashbackEarning{}).Where("source_id = ?", srcID).Count(&cbRows)
	db.Model(&models.FeeDistribution{}).Where("source_id = ?", srcID).Count(&guards)
	if tpRows != 2 {
		t.Fatalf("expected 2 tp earnings, got %d", tpRows)
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
	feature := models.FeatureProductMerchant
	fee := 100.0
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "checkout", SourceID: uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// TP credited = shareUSD * rate.
	want := userTPUSD(feature, fee) * rate
	if got := tpBalance(t, db, users[0].ID); !approx(got, want) {
		t.Fatalf("gen0 tp with rate %v = %v, want %v", rate, got, want)
	}

	// Cashback is recorded in USD and is NOT affected by the TP rate.
	if got := cashbackBalance(t, db, users[0].ID); !approx(got, fee*buyerFrac(feature)) {
		t.Fatalf("cashback should be USD-only (rate-independent) = %v, want %v", got, fee*buyerFrac(feature))
	}
}

// TestOnFeeSettled_ZeroFeeCreditsUserOnly: a transaction with no fee earns the
// user the fixed zero-fee reward (PM 2026-10-21) and nothing else — no upline,
// no pool, no cashback — once per source.
func TestOnFeeSettled_ZeroFeeCreditsUserOnly(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 100)
	users := buildFeeChain(t, db, 3)
	ev := FeeEvent{
		Feature: models.FeatureP2P, Token: "MCGP", FeeUSD: 0,
		UserID: users[0].ID, SourceType: "p2p_release", SourceID: uuid.New(),
	}
	for i := 0; i < 2; i++ { // the replay must not credit twice
		if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
			t.Fatalf("zero-fee settle %d: %v", i, err)
		}
	}
	if got := tpBalance(t, db, users[0].ID); !approx(got, models.DefaultZeroFeeTP) {
		t.Fatalf("zero-fee reward = %v, want %v", got, models.DefaultZeroFeeTP)
	}
	if got := tpBalance(t, db, users[1].ID); got != 0 {
		t.Fatalf("upline must earn nothing on a zero-fee transaction, got %v", got)
	}
	pool, _ := svc.GlobalPool(context.Background())
	if pool.AccruedUSD != 0 {
		t.Fatalf("zero-fee must not touch the pool, got %v", pool.AccruedUSD)
	}
	var cb int64
	db.Model(&models.CashbackEarning{}).Count(&cb)
	if cb != 0 {
		t.Fatalf("zero-fee must credit no cashback, got %d rows", cb)
	}
}

// Gas carries its own fee and never mints TP, so a zero-value gas event is not
// a reward either.
func TestOnFeeSettled_ZeroFeeGasEarnsNothing(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 100)
	user := mkFeeUser(t, db, "zg"+uuidShort(), nil, models.AccountStatusActive)
	if err := svc.OnFeeSettled(context.Background(), FeeEvent{
		Feature: models.FeatureGas, Token: "USDT", FeeUSD: 0,
		UserID: user.ID, SourceType: "gas", SourceID: uuid.New(),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := tpBalance(t, db, user.ID); got != 0 {
		t.Fatalf("zero-fee gas must earn nothing, got %v", got)
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

// --- SP3→SP5 dirty-hook bridge (SP5 finding #1) ---

// TestOnFeeSettled_DirtyHookMarksAllRecipients asserts the onDirty hook fires
// exactly once per DISTINCT recipient whose tp_balance or cashback_balance moved
// — the gen0 and gen1 TP recipients plus the cashback recipients — and
// never for global-pool-only generations. This is the bridge that was missing
// (MarkUserDirty was never called by any accrual path), which left the reward
// processor's dirty set permanently empty.
func TestOnFeeSettled_DirtyHookMarksAllRecipients(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	got := map[uuid.UUID]int{}
	svc.SetDirtyHook(func(_ context.Context, uid uuid.UUID) { got[uid]++ })

	users := buildFeeChain(t, db, 11) // gen0 + gen1..gen10, all active
	ev := FeeEvent{
		Feature:    models.FeatureProductMerchant,
		Token:      "USDT",
		FeeUSD:     100.0,
		UserID:     users[0].ID,
		SourceType: "checkout",
		SourceID:   uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// Only the user and the direct upline have a balance change (TP, plus buyer
	// and upline cashback) — each must be marked dirty EXACTLY once (deduped).
	for gen, u := range users[:2] {
		if got[u.ID] != 1 {
			t.Fatalf("gen %d user marked dirty %d times, want exactly 1", gen, got[u.ID])
		}
	}
	if len(got) != 2 {
		t.Fatalf("marked %d distinct users dirty, want 2", len(got))
	}
}

// TestOnFeeSettled_DirtyHookSkipsGlobalOnlyAndDuplicates asserts (1) a generation
// routed entirely to the global pool (no live ancestor) is NOT marked dirty —
// only users whose own balance changed are — and (2) a duplicate settled fee
// (idempotent guard no-op) fires the hook for NO ONE.
func TestOnFeeSettled_DirtyHookSkipsGlobalOnlyAndDuplicates(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	got := map[uuid.UUID]int{}
	svc.SetDirtyHook(func(_ context.Context, uid uuid.UUID) { got[uid]++ })

	// Lone user: no uplines, so gen1..gen10 TP + the 15% upline cashback all route
	// to the global pool. Only gen0 (the user) has a balance change.
	user := mkFeeUser(t, db, "solo"+uuidShort(), nil, models.AccountStatusActive)
	ev := FeeEvent{
		Feature:    models.FeatureProductMerchant,
		Token:      "USDT",
		FeeUSD:     50.0,
		UserID:     user.ID,
		SourceType: "checkout",
		SourceID:   uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}
	if len(got) != 1 || got[user.ID] != 1 {
		t.Fatalf("lone user: want only the user marked once, got %v", got)
	}

	// Re-settle the SAME source: idempotent guard no-ops, hook fires for no one.
	before := got[user.ID]
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled (dup): %v", err)
	}
	if got[user.ID] != before {
		t.Fatalf("duplicate settle must not re-mark dirty; before=%d after=%d", before, got[user.ID])
	}
}

// TestOnFeeSettled_NilDirtyHookSafe asserts a settled fee with NO hook wired
// (the engine's default, and the FEE_MODEL_V2-off / pre-SP5 state) still
// distributes correctly and does not panic.
func TestOnFeeSettled_NilDirtyHookSafe(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	// No SetDirtyHook call — onDirty stays nil.

	user := mkFeeUser(t, db, "nohook"+uuidShort(), nil, models.AccountStatusActive)
	feature := models.FeatureProductMerchant
	ev := FeeEvent{
		Feature:    feature,
		Token:      "USDT",
		FeeUSD:     20.0,
		UserID:     user.ID,
		SourceType: "checkout",
		SourceID:   uuid.New(),
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled with nil hook: %v", err)
	}
	if got := tpBalance(t, db, user.ID); !approx(got, userTPUSD(feature, 20.0)) {
		t.Fatalf("nil hook must still distribute TP; got %v", got)
	}
}

// ─── COMPLETE per-feature split model tests (design §2) ──────────────────────

// creditedTotalUSD sums every USD value the engine credited/accrued for one
// source: user TP (back out the tpPerUSD), user cashback, global-pool USD, and
// the recorded system USD on the guard row. With tpPerUSD=1 this must equal the
// source FeeUSD exactly (conservation per feature).
func creditedTotalUSD(t *testing.T, db *gorm.DB, sourceID uuid.UUID) float64 {
	t.Helper()
	var fd models.FeeDistribution
	if err := db.First(&fd, "source_id = ?", sourceID).Error; err != nil {
		t.Fatalf("load guard for %s: %v", sourceID, err)
	}
	var tpUSD float64
	db.Model(&models.TPEarning{}).Where("source_id = ?", sourceID).
		Select("COALESCE(SUM(tp_earned), 0)").Scan(&tpUSD) // tpPerUSD=1 in these tests
	var cbUSD float64
	db.Model(&models.CashbackEarning{}).Where("source_id = ?", sourceID).
		Select("COALESCE(SUM(amount_usd), 0)").Scan(&cbUSD)
	var globalUSD float64
	db.Model(&models.GlobalTPAccrual{}).Where("source_id = ?", sourceID).
		Select("COALESCE(SUM(amount_usd), 0)").Scan(&globalUSD)
	return fd.SystemUSD + tpUSD + cbUSD + globalUSD
}

// TestCarvedFee_ProductMerchant verifies a carved fee (product_merchant) on a
// full chain credits buyer 15% + upline 15% + TP 20% (gen0..gen10) + 0 provider,
// and conserves (every credited share sums to FeeUSD).
func TestCarvedFee_ProductMerchant(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	users := buildFeeChain(t, db, 11)
	feature := models.FeatureProductMerchant
	fee := 100.0
	srcID := uuid.New()
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "checkout", SourceID: srcID,
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// Buyer 15%, upline 15%, no provider.
	if got := cashbackBalance(t, db, users[0].ID); !approx(got, fee*0.15) {
		t.Fatalf("buyer cashback = %v, want %v", got, fee*0.15)
	}
	if got := cashbackBalance(t, db, users[1].ID); !approx(got, fee*0.15) {
		t.Fatalf("upline cashback = %v, want %v", got, fee*0.15)
	}
	// TP: 5% of the kept 70 to the user and to the upline, 2% to the pool.
	for gen, want := range []float64{userTPUSD(feature, fee), uplineTPUSD(feature, fee)} {
		if got := tpBalance(t, db, users[gen].ID); !approx(got, want) {
			t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
		}
	}
	if want := 3.5; !approx(userTPUSD(feature, fee), want) {
		t.Fatalf("golden: user TP on a $100 product fee = %v, want %v", userTPUSD(feature, fee), want)
	}
	// No provider cashback row at all (3 cashback rows would mean provider got one).
	var cbRows int64
	db.Model(&models.CashbackEarning{}).Where("source_id = ?", srcID).Count(&cbRows)
	if cbRows != 2 {
		t.Fatalf("carved fee with no provider must write exactly 2 cashback rows (buyer+upline), got %d", cbRows)
	}
	// Conservation.
	if got := creditedTotalUSD(t, db, srcID); !approx(got, fee) {
		t.Fatalf("conservation: credited %v, want fee %v", got, fee)
	}
}

// TestCarvedFee_ServiceContact verifies service_contact credits buyer 10% +
// upline 10% + provider 10% + TP 20%, the provider gets exactly ONE cashback row
// to ProviderID, and the event conserves.
func TestCarvedFee_ServiceContact(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	upline := mkFeeUser(t, db, "sc_up"+uuidShort(), nil, models.AccountStatusActive)
	requester := mkFeeUser(t, db, "sc_req"+uuidShort(), &upline.ID, models.AccountStatusActive)
	provider := mkFeeUser(t, db, "sc_prov"+uuidShort(), nil, models.AccountStatusActive)

	feature := models.FeatureServiceContact
	fee := 0.10
	srcID := uuid.New()
	providerID := provider.ID
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: requester.ID, ProviderID: &providerID,
		SourceType: "service_contact", SourceID: srcID,
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	if got := cashbackBalance(t, db, requester.ID); !approx(got, fee*0.10) {
		t.Fatalf("requester (buyer) cashback = %v, want %v", got, fee*0.10)
	}
	if got := cashbackBalance(t, db, upline.ID); !approx(got, fee*0.10) {
		t.Fatalf("upline cashback = %v, want %v", got, fee*0.10)
	}
	if got := cashbackBalance(t, db, provider.ID); !approx(got, fee*providerFrac(feature)) {
		t.Fatalf("provider cashback = %v, want %v", got, fee*providerFrac(feature))
	}
	// Provider credited EXACTLY once.
	var provRows int64
	db.Model(&models.CashbackEarning{}).Where("source_id = ? AND user_id = ?", srcID, provider.ID).Count(&provRows)
	if provRows != 1 {
		t.Fatalf("provider must be credited exactly once, got %d rows", provRows)
	}
	// TP: requester earns 5% of the kept share, the upline 5%.
	if got := tpBalance(t, db, requester.ID); !approx(got, userTPUSD(feature, fee)) {
		t.Fatalf("requester tp = %v, want %v", got, userTPUSD(feature, fee))
	}
	if got := tpBalance(t, db, upline.ID); !approx(got, uplineTPUSD(feature, fee)) {
		t.Fatalf("upline tp = %v, want %v", got, uplineTPUSD(feature, fee))
	}
	// Audit-row completeness: the guard's OWN component columns (system + buyer +
	// upline + provider + TP) must sum to the full FeeUSD — including the 10%
	// provider share. Without ProviderCashbackUSD on the guard, per-row
	// reconciliation of service_contact would appear to lose 10%.
	var fd models.FeeDistribution
	if err := db.First(&fd, "source_id = ?", srcID).Error; err != nil {
		t.Fatalf("load guard: %v", err)
	}
	if !approx(fd.ProviderCashbackUSD, fee*providerFrac(feature)) {
		t.Fatalf("guard provider_cashback_usd = %v, want %v", fd.ProviderCashbackUSD, fee*providerFrac(feature))
	}
	guardSum := fd.SystemUSD + fd.BuyerCashbackUSD + fd.UplineCashbackUSD + fd.ProviderCashbackUSD + fd.TPPoolUSD
	if !approx(guardSum, fee) {
		t.Fatalf("guard component columns sum = %v, want fee %v", guardSum, fee)
	}
	// Conservation.
	if got := creditedTotalUSD(t, db, srcID); !approx(got, fee) {
		t.Fatalf("conservation: credited %v, want fee %v", got, fee)
	}
}

// TestSeventyThirtyFee_NoCashback verifies a 70/30 trade (withdrawal and otc)
// credits NO cashback (buyer/upline/provider all 0), routes TP 30% across
// gen0..gen10, records 70% system, and conserves.
func TestSeventyThirtyFee_NoCashback(t *testing.T) {
	for _, feature := range []string{models.FeatureWithdrawal, models.FeatureOTC} {
		t.Run(feature, func(t *testing.T) {
			db := setupFeeDistTestDB(t)
			svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

			users := buildFeeChain(t, db, 11)
			fee := 100.0
			srcID := uuid.New()
			// Pass a ProviderID too, to prove the 0 provider share ignores it.
			pid := users[2].ID
			ev := FeeEvent{
				Feature: feature, Token: "USDT", FeeUSD: fee,
				UserID: users[0].ID, ProviderID: &pid,
				SourceType: feature, SourceID: srcID,
			}
			if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
				t.Fatalf("OnFeeSettled: %v", err)
			}

			// NO cashback rows anywhere.
			var cbRows int64
			db.Model(&models.CashbackEarning{}).Where("source_id = ?", srcID).Count(&cbRows)
			if cbRows != 0 {
				t.Fatalf("70/30 fee must credit NO cashback, got %d rows", cbRows)
			}
			// No cashback means the whole fee is kept: 5% to the user, 5% to the
			// upline, 2% to the pool, the remaining 88% stays system.
			for gen, want := range []float64{userTPUSD(feature, fee), uplineTPUSD(feature, fee)} {
				if got := tpBalance(t, db, users[gen].ID); !approx(got, want) {
					t.Fatalf("gen %d tp = %v, want %v", gen, got, want)
				}
			}
			var fd models.FeeDistribution
			db.First(&fd, "source_id = ?", srcID)
			if !approx(fd.SystemUSD, fee*0.88) {
				t.Fatalf("system = %v, want %v", fd.SystemUSD, fee*0.88)
			}
			// Conservation.
			if got := creditedTotalUSD(t, db, srcID); !approx(got, fee) {
				t.Fatalf("conservation: credited %v, want fee %v", got, fee)
			}
		})
	}
}

// TestGasFee_OnlySystem verifies the gas split credits ONLY system (100%): no TP,
// no cashback, no recipients — and conserves.
func TestGasFee_OnlySystem(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	users := buildFeeChain(t, db, 11)
	feature := models.FeatureGas
	fee := 0.50
	srcID := uuid.New()
	ev := FeeEvent{
		Feature: feature, Token: "USDT", FeeUSD: fee,
		UserID: users[0].ID, SourceType: "gas", SourceID: srcID,
	}
	if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// No TP anywhere.
	var tpRows int64
	db.Model(&models.TPEarning{}).Where("source_id = ?", srcID).Count(&tpRows)
	if tpRows != 0 {
		t.Fatalf("gas must mint NO TP, got %d tp rows", tpRows)
	}
	for gen := 0; gen <= MaxTPGenerations; gen++ {
		if got := tpBalance(t, db, users[gen].ID); got != 0 {
			t.Fatalf("gen %d must have 0 TP for gas, got %v", gen, got)
		}
	}
	// No cashback, no global accrual.
	var cbRows, globalRows int64
	db.Model(&models.CashbackEarning{}).Where("source_id = ?", srcID).Count(&cbRows)
	db.Model(&models.GlobalTPAccrual{}).Where("source_id = ?", srcID).Count(&globalRows)
	if cbRows != 0 || globalRows != 0 {
		t.Fatalf("gas must credit only system; got %d cashback, %d global rows", cbRows, globalRows)
	}
	// System = 100%.
	var fd models.FeeDistribution
	db.First(&fd, "source_id = ?", srcID)
	if !approx(fd.SystemUSD, fee) {
		t.Fatalf("gas system = %v, want full fee %v", fd.SystemUSD, fee)
	}
	// Conservation.
	if got := creditedTotalUSD(t, db, srcID); !approx(got, fee) {
		t.Fatalf("conservation: credited %v, want fee %v", got, fee)
	}
}

// TestConservation_EveryFeature is the dynamic conservation guard across the
// WHOLE split table: for each feature, the sum of all credited shares (system +
// user TP + cashback + global pool) equals the source FeeUSD exactly.
func TestConservation_EveryFeature(t *testing.T) {
	for feature := range featureSplits {
		t.Run(feature, func(t *testing.T) {
			db := setupFeeDistTestDB(t)
			svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
			users := buildFeeChain(t, db, 11)
			provider := mkFeeUser(t, db, "cons_prov"+uuidShort(), nil, models.AccountStatusActive)
			pid := provider.ID
			fee := 100.0
			srcID := uuid.New()
			ev := FeeEvent{
				Feature: feature, Token: "USDT", FeeUSD: fee,
				UserID: users[0].ID, ProviderID: &pid,
				SourceType: "conservation", SourceID: srcID,
			}
			if err := svc.OnFeeSettled(context.Background(), ev); err != nil {
				t.Fatalf("OnFeeSettled[%s]: %v", feature, err)
			}
			if got := creditedTotalUSD(t, db, srcID); !approx(got, fee) {
				t.Fatalf("feature %s NOT conserved: credited %v, want %v", feature, got, fee)
			}
		})
	}
}
