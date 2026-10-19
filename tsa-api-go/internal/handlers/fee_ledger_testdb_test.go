package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupFeeLedgerTestDB builds an in-memory SQLite DB with the users / wallets /
// transactions tables AND the full fee-v2 ledger tables, on a single pinned
// connection (the engine's recursive-CTE upline read shares the handle).
//
// Previously lived in withdrawal_accrual_test.go alongside a custodial
// withdrawal handler that debited a DB-held `assets.balance`. That handler and
// its table are gone — balances are read from the chain — but the fee-ledger
// rig it carried is still the harness for every accrual test, so it moved here
// and lost the assets DDL.
func setupFeeLedgerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE, password TEXT NOT NULL, role TEXT DEFAULT 'user',
			phone_number TEXT, country TEXT, state TEXT, city TEXT, address TEXT,
			profile_photo TEXT, referral_code TEXT, referred_by TEXT, didit_session_id TEXT,
			verification_status TEXT DEFAULT 'pending', verification_notes TEXT,
			account_status TEXT DEFAULT 'active', last_login DATETIME, login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME, wallet_address TEXT, seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0, mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0, cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER, email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME, created_at DATETIME, updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
		)`,
		`CREATE TABLE wallets (
			id TEXT PRIMARY KEY, user_id TEXT, transaction_limit REAL DEFAULT 0,
			daily_limit REAL DEFAULT 0, created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE transactions (
			id TEXT PRIMARY KEY, user_id TEXT, type TEXT, status TEXT, asset_symbol TEXT,
			from_asset TEXT, to_asset TEXT, from_amount REAL, to_amount REAL, exchange_rate REAL,
			amount REAL, usd_value REAL, fee REAL, fee_usd REAL, platform_fee REAL, network_fee REAL,
			fees TEXT, network TEXT, wallet_address TEXT, transaction_hash TEXT, tx_hash TEXT,
			blockchain TEXT, confirmations INTEGER, required_confirms INTEGER, metadata TEXT,
			description TEXT, tags TEXT, completed_at DATETIME, confirmed_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE tp_earnings (
			id TEXT PRIMARY KEY, user_id TEXT, source_user_id TEXT, source_type TEXT,
			source_id TEXT, generation INTEGER, fee_amount_usd REAL, percentage REAL,
			tp_earned REAL, created_at DATETIME
		)`,
		`CREATE TABLE cashback_earnings (
			id TEXT PRIMARY KEY, user_id TEXT, source_user_id TEXT, source_type TEXT,
			source_id TEXT, amount_usd REAL, token TEXT, tx_hash TEXT, created_at DATETIME
		)`,
		`CREATE TABLE fee_distributions (
			id TEXT PRIMARY KEY, source_type TEXT, source_id TEXT, user_id TEXT,
			feature TEXT, token TEXT, fee_usd REAL, system_usd REAL,
			buyer_cashback_usd REAL, upline_cashback_usd REAL,
			provider_cashback_usd REAL DEFAULT 0, tp_pool_usd REAL,
			global_pool_usd REAL DEFAULT 0, tp_per_usd REAL DEFAULT 1, created_at DATETIME,
			UNIQUE(source_type, source_id)
		)`,
		`CREATE TABLE global_tp_pool (
			singleton INTEGER PRIMARY KEY, accrued_usd REAL DEFAULT 0, accrued_tp REAL DEFAULT 0,
			per_user_index_scaled INTEGER NOT NULL DEFAULT 0, indexed_tp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME
		)`,
		`CREATE TABLE global_tp_accruals (
			id TEXT PRIMARY KEY, source_type TEXT, source_id TEXT, reason TEXT,
			generation INTEGER DEFAULT -1, amount_usd REAL, amount_tp REAL, created_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	config.DB = db
	return db
}

func mkWUser(t *testing.T, db *gorm.DB, name string, referredBy *uuid.UUID) models.User {
	t.Helper()
	u := models.User{
		ID: uuid.New(), Name: name, Username: name, Email: name + "@t.co",
		Password: "x", ReferralCode: name, ReferredBy: referredBy,
		AccountStatus: models.AccountStatusActive, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func feeGuardCount(db *gorm.DB, sourceType string, sourceID uuid.UUID) int64 {
	var n int64
	db.Model(&models.FeeDistribution{}).Where("source_type = ? AND source_id = ?", sourceType, sourceID).Count(&n)
	return n
}
