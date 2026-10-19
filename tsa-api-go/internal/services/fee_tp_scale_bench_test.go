package services

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── SP9 — LIGHTWEIGHT scale validation ──────────────────────────────────────
//
// These benchmarks prove the COMPLEXITY CLAIMS the fee/TP design (§3, §4, §8 SP9)
// makes about its hot paths, using the same in-memory SQLite setup as the unit
// tests. They are deliberately NOT a load-test harness: the platform is early
// stage with few users. The goal is to confirm the asymptotic shape of each hot
// path in TOTAL USER COUNT — that the per-fee and per-epoch paths stay ~flat as
// the user table grows, and that the only O(eligible) path (merkle build) is
// linear and acceptably fast at admin-publish cadence.
//
// Run (capped so it never runs forever):
//
//	go test -bench '^Benchmark(OnFeeSettled|SettleGlobalPoolEpoch|SettleUserGlobalShare|BuildDistribution)$' \
//	    -benchmem -benchtime=200x -run '^$' ./internal/services/
//
// -benchtime=200x fixes the iteration count (so seeding 10k users isn't repeated
// to satisfy a wall-clock target). The per-op numbers are what matter; compare
// the SAME bench across the user-count sub-benchmarks (/users=1000 vs 10000).
//
// NOTE on the SQLite caveat: the test DB pins MaxOpenConns(1) (in-memory SQLite
// is per-connection). Absolute ns/op is therefore higher than production
// PostgreSQL would be — but the SHAPE (flat vs growing in user count) is what the
// complexity claim is about, and that shape is DB-independent. Read these as
// relative curves, not production latencies.

// setupScaleBenchDB builds the full superset of tables every SP9 bench touches:
// the SP3/SP4 accrual+settlement tables, the SP5 payout tables, and the SP6
// distribution tables. Same raw-DDL approach as the unit-test helpers (no
// PostgreSQL gen_random_uuid() defaults; all IDs set in Go). Pinned to one
// connection because in-memory SQLite is per-connection.
func setupScaleBenchDB(b *testing.B) *gorm.DB {
	b.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		b.Fatalf("db handle: %v", err)
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
		`CREATE INDEX idx_users_deleted_at ON users(deleted_at)`,
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
			payout_token TEXT NOT NULL DEFAULT 'MCGP',
			payout_chain TEXT NOT NULL DEFAULT 'sonic',
			payout_amount REAL NOT NULL DEFAULT 0,
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
		`CREATE TABLE reward_distributions (
			id TEXT PRIMARY KEY,
			epoch INTEGER NOT NULL,
			merkle_root TEXT NOT NULL,
			distributor_address TEXT NOT NULL,
			chain_id INTEGER NOT NULL DEFAULT 0,
			leaf_count INTEGER NOT NULL DEFAULT 0,
			total_base_units TEXT NOT NULL DEFAULT '0',
			status TEXT NOT NULL DEFAULT 'pending',
			published_by TEXT,
			tx_hash TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE reward_distribution_entries (
			id TEXT PRIMARY KEY,
			distribution_id TEXT NOT NULL,
			epoch INTEGER NOT NULL,
			user_id TEXT NOT NULL,
			account TEXT NOT NULL,
			cumulative_base_units TEXT NOT NULL,
			leaf TEXT NOT NULL,
			proof TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			b.Fatalf("ddl: %v", err)
		}
	}
	return db
}

// seedFillerUsers bulk-inserts n unreferred "filler" users so the users table
// reflects a given total population WITHOUT touching the referral chain under
// test. These are the users whose presence must NOT change the per-op cost of
// OnFeeSettled / SettleGlobalPoolEpoch / SettleUserGlobalShare (the whole point
// of the cumulative-index / bounded-walk design). Inserted in batches for speed
// — seeding is not measured.
func seedFillerUsers(b *testing.B, db *gorm.DB, n int) {
	b.Helper()
	const batch = 1000
	now := time.Now()
	buf := make([]models.User, 0, batch)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := db.Create(&buf).Error; err != nil {
			b.Fatalf("seed filler users: %v", err)
		}
		buf = buf[:0]
	}
	for i := 0; i < n; i++ {
		id := uuid.New()
		short := id.String()[:12]
		buf = append(buf, models.User{
			ID:            id,
			Name:          "filler",
			Username:      "f" + short,
			Email:         "f" + short + "@bench.test",
			Password:      "x",
			AccountStatus: models.AccountStatusActive,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		if len(buf) == batch {
			flush()
		}
	}
	flush()
}

// buildBenchChain inserts a referral chain of `depth` ACTIVE users (index 0 is
// the transacting user gen0; index i is the gen-i upline) and returns them.
// Mirrors buildFeeChain but takes *testing.B.
func buildBenchChain(b *testing.B, db *gorm.DB, depth int) []models.User {
	b.Helper()
	users := make([]models.User, depth)
	var prev *uuid.UUID
	now := time.Now()
	for i := depth - 1; i >= 0; i-- {
		id := uuid.New()
		short := id.String()[:12]
		u := models.User{
			ID:            id,
			Name:          "chain",
			Username:      "c" + short,
			Email:         "c" + short + "@bench.test",
			Password:      "x",
			ReferralCode:  "c" + short,
			ReferredBy:    prev,
			AccountStatus: models.AccountStatusActive,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := db.Create(&u).Error; err != nil {
			b.Fatalf("create chain user: %v", err)
		}
		users[i] = u
		pid := id
		prev = &pid
	}
	return users
}

// ─── 1. OnFeeSettled — runs on EVERY fee ─────────────────────────────────────
//
// CLAIM (§3, §8): ~constant in TOTAL user count. Each call resolves ≤10 upline
// generations (one bounded recursive-CTE walk) and does a bounded write (≤11 TP
// rows + ≤2 cashback rows + 1 guard, plus any global-pool accrual). None of that
// depends on how many OTHER users exist. We hold the referral chain fixed (full
// 11-deep) and vary only the filler population, so any growth in ns/op would be
// the users-table size leaking into the hot path. It must NOT grow.
func BenchmarkOnFeeSettled(b *testing.B) {
	for _, total := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("users=%d", total), func(b *testing.B) {
			db := setupScaleBenchDB(b)
			// Fixed full 11-deep active chain (gen0 + gen1..gen10).
			chain := buildBenchChain(b, db, MaxTPGenerations+1)
			// Pad the population with unrelated users up to `total`.
			if rem := total - len(chain); rem > 0 {
				seedFillerUsers(b, db, rem)
			}
			svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
			ctx := context.Background()
			gen0 := chain[0].ID

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Fresh SourceID each op so the idempotency guard lets it distribute
				// (a duplicate would no-op and not measure the real write path).
				if err := svc.OnFeeSettled(ctx, FeeEvent{
					Feature:    models.FeatureP2P,
					Token:      "USDT",
					FeeUSD:     100.0,
					UserID:     gen0,
					SourceType: "p2p_release",
					SourceID:   uuid.New(),
				}); err != nil {
					b.Fatalf("OnFeeSettled: %v", err)
				}
			}
		})
	}
}

// ─── 2. SettleGlobalPoolEpoch — once per epoch (default daily) ────────────────
//
// CLAIM (§4, §8): O(1) writes + one COUNT(*), INDEPENDENT of user count. The
// MasterChef accRewardPerShare fold advances ONE scalar index + writes ONE epoch
// row regardless of population — the only user-count-dependent term is the
// COUNT(*) of registered users (one indexed aggregate, sub-linear in practice).
// We seed accrued pool TP and fold one epoch per op; the only thing that varies
// across sub-benches is the user population the COUNT scans.
func BenchmarkSettleGlobalPoolEpoch(b *testing.B) {
	for _, total := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("users=%d", total), func(b *testing.B) {
			db := setupScaleBenchDB(b)
			seedFillerUsers(b, db, total)
			svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
			ctx := context.Background()

			// Seed the singleton pool with a large accrued balance so every fold
			// has a positive delta to consume (we bump accrued_tp between ops in
			// stopped time so the measured call always does real work, never a
			// no-op).
			now := time.Now()
			if err := db.Exec(
				`INSERT INTO global_tp_pool (singleton, accrued_usd, accrued_tp, per_user_index_scaled, indexed_tp, updated_at)
				 VALUES (1, 0, 0, 0, 0, ?)`, now).Error; err != nil {
				b.Fatalf("seed pool: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Add fresh accrual outside the measured window so the fold has a
				// non-zero delta (otherwise it idempotently no-ops). The bump is a
				// single O(1) write and is NOT what we are measuring.
				b.StopTimer()
				if err := db.Exec(
					`UPDATE global_tp_pool SET accrued_tp = accrued_tp + ? WHERE singleton = 1`,
					float64(total), // > userCount so perUserScaled > 0 every epoch
				).Error; err != nil {
					b.Fatalf("bump accrual: %v", err)
				}
				b.StartTimer()

				if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
					b.Fatalf("SettleGlobalPoolEpoch: %v", err)
				}
			}
		})
	}
}

// ─── 3. SettleUserGlobalShare — O(1) per user (lazy credit on read/claim) ─────
//
// CLAIM (§4, §8): O(1) per user — read the global index, lock ONE user row,
// write ONE TPEarning + one balance update, advance the checkpoint. No scan of
// other users. Population should not matter; we still bench at 1k/10k to show it
// is flat. Each op rotates to a fresh user and bumps the index (stopped time) so
// every measured call has a positive owed amount (a second call on the same user
// with no intervening epoch would idempotently no-op).
func BenchmarkSettleUserGlobalShare(b *testing.B) {
	for _, total := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("users=%d", total), func(b *testing.B) {
			db := setupScaleBenchDB(b)
			chain := buildBenchChain(b, db, 1) // not used for referral, just a known id pool
			_ = chain
			seedFillerUsers(b, db, total)
			svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
			ctx := context.Background()

			// Pull a rotating set of real user ids to settle against.
			var ids []uuid.UUID
			if err := db.Model(&models.User{}).Limit(4096).Pluck("id", &ids).Error; err != nil {
				b.Fatalf("load ids: %v", err)
			}
			if len(ids) == 0 {
				b.Fatal("no users seeded")
			}

			// Seed the pool with an already-advanced index so users have a positive
			// owed share immediately; we keep advancing it each op.
			now := time.Now()
			if err := db.Exec(
				`INSERT INTO global_tp_pool (singleton, accrued_usd, accrued_tp, per_user_index_scaled, indexed_tp, updated_at)
				 VALUES (1, 0, 0, ?, 0, ?)`, int64(models.GlobalTPIndexScale), now).Error; err != nil {
				b.Fatalf("seed pool index: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Advance the index (stopped time) so the rotated user is owed > 0.
				b.StopTimer()
				if err := db.Exec(
					`UPDATE global_tp_pool SET per_user_index_scaled = per_user_index_scaled + ? WHERE singleton = 1`,
					int64(models.GlobalTPIndexScale),
				).Error; err != nil {
					b.Fatalf("advance index: %v", err)
				}
				uid := ids[i%len(ids)]
				b.StartTimer()

				if _, err := svc.SettleUserGlobalShare(ctx, uid); err != nil {
					b.Fatalf("SettleUserGlobalShare: %v", err)
				}
			}
		})
	}
}

// ─── 4. BuildDistribution — O(eligible) at publish time (admin-triggered) ─────
//
// CLAIM (§8 SP9): O(eligible users), acceptable because publishing is infrequent
// and admin-triggered. Each build scans every positive claimable, runs the SP7
// eligibility check per user, floors + monotonic-clamps the amount, builds the
// whole merkle tree in memory, and persists one entry row per leaf. This SHOULD
// scale ~linearly in the eligible-user count; we bench 100 / 1k / 10k and report
// the absolute time so the doc can judge whether 10k is a few hundred ms (fine)
// or multiple seconds (flag it). Each op builds a NEW cumulative epoch (epoch
// number auto-increments), so prior epochs accumulate — we keep b.N modest via
// -benchtime to bound that.
func BenchmarkBuildDistribution(b *testing.B) {
	// benchEligibility marks everyone eligible (the worst case for tree size —
	// nobody is filtered out, so all N leaves are built).
	allEligible := &fakeEligibility{eligible: nil}

	for _, eligible := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("eligible=%d", eligible), func(b *testing.B) {
			db := setupScaleBenchDB(b)
			seedClaimants(b, db, eligible)
			// Chunk batch inserts so the entries INSERT does not exceed SQLite's
			// bind-variable cap (9 cols × 10k rows = 90k > the SQLite limit). This
			// is a TEST-HARNESS constraint, not a production one — PostgreSQL has no
			// such low cap — but production should still set CreateBatchSize (see the
			// SP9 scale notes) so the publish insert is chunked rather than one giant
			// statement. Using a batched session keeps the bench measuring the real
			// BuildDistribution path at 10k eligible.
			batchedDB := db.Session(&gorm.Session{CreateBatchSize: 200})
			svc := NewRewardMerkleService(batchedDB, allEligible,
				"0xdistributor000000000000000000000000000001", 146)
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := svc.BuildDistribution(ctx, nil); err != nil {
					b.Fatalf("BuildDistribution: %v", err)
				}
			}
		})
	}
}

// seedClaimants inserts n users each with a real hex wallet address and a
// positive claimable balance, so BuildDistribution includes all of them.
// Wallet addresses are derived deterministically from a counter so every user
// has a distinct, valid 20-byte address (distinct leaves).
func seedClaimants(b *testing.B, db *gorm.DB, n int) {
	b.Helper()
	const batch = 1000
	now := time.Now()
	users := make([]models.User, 0, batch)
	claims := make([]models.RewardClaimable, 0, batch)
	flush := func() {
		if len(users) == 0 {
			return
		}
		if err := db.Create(&users).Error; err != nil {
			b.Fatalf("seed claimant users: %v", err)
		}
		if err := db.Create(&claims).Error; err != nil {
			b.Fatalf("seed claimables: %v", err)
		}
		users = users[:0]
		claims = claims[:0]
	}
	for i := 0; i < n; i++ {
		id := uuid.New()
		short := id.String()[:12]
		// Distinct valid 20-byte hex address from the index.
		addr := fmt.Sprintf("0x%040x", big.NewInt(int64(i+1)))
		users = append(users, models.User{
			ID:            id,
			Name:          "claimant",
			Username:      "k" + short,
			Email:         "k" + short + "@bench.test",
			Password:      "x",
			AccountStatus: models.AccountStatusActive,
			WalletAddress: addr,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		claims = append(claims, models.RewardClaimable{
			UserID:        id,
			ClaimableMCGP: 1.0 + float64(i)*0.001, // distinct positive claimable
			UpdatedAt:     now,
		})
		if len(users) == batch {
			flush()
		}
	}
	flush()
}
