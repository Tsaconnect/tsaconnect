package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupWithdrawalTestDB builds an in-memory SQLite DB with the users / assets /
// wallets / transactions tables AND the full fee-v2 ledger tables, on a single
// pinned connection (the engine's recursive-CTE upline read shares the handle).
func setupWithdrawalTestDB(t *testing.T) *gorm.DB {
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
			profile_photo TEXT, referral_code TEXT, referred_by TEXT, persona_inquiry_id TEXT,
			verification_status TEXT DEFAULT 'pending', verification_notes TEXT,
			account_status TEXT DEFAULT 'active', last_login DATETIME, login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME, wallet_address TEXT, seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0, mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0, cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER, email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME, created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE assets (
			id TEXT PRIMARY KEY, user_id TEXT, symbol TEXT, name TEXT,
			balance REAL DEFAULT 0, usd_value REAL DEFAULT 0, is_selected INTEGER DEFAULT 0,
			is_hidden INTEGER DEFAULT 0, details TEXT, metadata TEXT, performance TEXT,
			last_synced DATETIME, created_at DATETIME, updated_at DATETIME
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

// seededPriceService returns a PriceService with the symbol pre-cached at $1.00
// so getAssetPrice never hits the network in tests.
func seededPriceService(symbol string) *services.PriceService {
	ps := services.NewPriceService()
	ps.SeedPriceForTest(symbol, services.PriceData{USD: 1.0})
	return ps
}

func newWithdrawalHandler(t *testing.T, db *gorm.DB) *Handlers {
	t.Helper()
	cfg := &config.Config{TPPerUSD: 1.0}
	feeSvc := services.NewFeeDistributionService(db, services.NewReferralGraphService(db), 1.0)
	return &Handlers{
		Config:       cfg,
		EventBus:     events.NewBus(),
		PriceService: seededPriceService("USDT"),
		FeeService:   feeSvc,
	}
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

func mkAsset(t *testing.T, db *gorm.DB, userID uuid.UUID, symbol string, balance float64) {
	t.Helper()
	a := models.Asset{ID: uuid.New(), UserID: userID, Symbol: symbol, Name: symbol, Balance: balance, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("create asset: %v", err)
	}
}

func doWithdraw(t *testing.T, h *Handlers, user models.User, symbol string, amount float64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"assetSymbol": symbol, "amount": amount, "walletAddress": "0xdeadbeef",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/transactions/withdraw", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &user)
	h.CreateWithdrawal(c)
	return w
}

func feeGuardCount(db *gorm.DB, sourceType string, sourceID uuid.UUID) int64 {
	var n int64
	db.Model(&models.FeeDistribution{}).Where("source_type = ? AND source_id = ?", sourceType, sourceID).Count(&n)
	return n
}

// (1) Flag ON: the withdrawal accrues a FeeEvent in the SAME DB tx as the
// deduction, under SourceType "withdrawal", FeatureWithdrawal, with the verified
// platform-fee USD. The balance is deducted and the guard row exists.
func TestWithdrawal_AccruesInSameTx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupWithdrawalTestDB(t)
	h := newWithdrawalHandler(t, db)

	user := mkWUser(t, db, "wuser", nil)
	mkAsset(t, db, user.ID, "USDT", 1000.0)

	w := doWithdraw(t, h, user, "USDT", 100.0)
	if w.Code != http.StatusCreated {
		t.Fatalf("withdraw status=%d body=%s", w.Code, w.Body.String())
	}

	// Find the created tx.
	var tx models.Transaction
	if err := db.Where("user_id = ? AND type = ?", user.ID, models.TransactionTypeWithdrawal).First(&tx).Error; err != nil {
		t.Fatalf("no withdrawal tx: %v", err)
	}

	// FeeEvent accrued: exactly one guard row, distinct SourceType "withdrawal".
	if got := feeGuardCount(db, "withdrawal", tx.ID); got != 1 {
		t.Fatalf("want 1 withdrawal guard row, got %d", got)
	}
	var fd models.FeeDistribution
	if err := db.Where("source_type = ? AND source_id = ?", "withdrawal", tx.ID).First(&fd).Error; err != nil {
		t.Fatalf("load guard: %v", err)
	}
	if fd.Feature != models.FeatureWithdrawal {
		t.Errorf("guard feature=%q want %q", fd.Feature, models.FeatureWithdrawal)
	}
	// 0.5% of 100 USDT @ $1 = $0.50.
	if fd.FeeUSD < 0.4999 || fd.FeeUSD > 0.5001 {
		t.Errorf("guard feeUSD=%v want 0.50", fd.FeeUSD)
	}

	// Balance deducted: 1000 - (100 + 0.5% platform + network fee).
	var asset models.Asset
	db.Where("user_id = ? AND symbol = ?", user.ID, "USDT").First(&asset)
	if asset.Balance >= 1000.0 {
		t.Errorf("balance not deducted: %v", asset.Balance)
	}
}

// (2) Idempotency: the engine is keyed (withdrawal, tx.ID); a second settle for
// the same tx is a no-op (one guard row only). We re-run SettleInTx directly.
func TestWithdrawal_AccrualIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupWithdrawalTestDB(t)
	h := newWithdrawalHandler(t, db)
	user := mkWUser(t, db, "widem", nil)
	mkAsset(t, db, user.ID, "USDT", 1000.0)

	if w := doWithdraw(t, h, user, "USDT", 100.0); w.Code != http.StatusCreated {
		t.Fatalf("withdraw status=%d", w.Code)
	}
	var tx models.Transaction
	db.Where("user_id = ? AND type = ?", user.ID, models.TransactionTypeWithdrawal).First(&tx)

	// Re-settle the SAME (withdrawal, tx.ID) — must be a clean no-op.
	ev := services.FeeEvent{Feature: models.FeatureWithdrawal, Token: "USDT", FeeUSD: 0.5, UserID: user.ID, SourceType: "withdrawal", SourceID: tx.ID}
	if err := db.Transaction(func(dbtx *gorm.DB) error {
		return h.FeeService.SettleInTx(dbtx, ev, map[int]services.Ancestor{}, map[uuid.UUID]struct{}{})
	}); err != nil {
		t.Fatalf("re-settle: %v", err)
	}
	if got := feeGuardCount(db, "withdrawal", tx.ID); got != 1 {
		t.Fatalf("idempotency broken: want 1 guard row, got %d", got)
	}
}

// (3) THE LEAK FIX: a FeeEvent-write failure rolls back the WHOLE tx — the
// balance is NOT deducted and no transaction row persists. We force the in-tx
// guard insert to fail by dropping the fee_distributions table while the flag
// is ON.
func TestWithdrawal_FeeWriteFailureRollsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupWithdrawalTestDB(t)
	h := newWithdrawalHandler(t, db)
	user := mkWUser(t, db, "wrollback", nil)
	mkAsset(t, db, user.ID, "USDT", 1000.0)

	// Break the accrual write: no fee_distributions table → guard insert errors.
	if err := db.Exec(`DROP TABLE fee_distributions`).Error; err != nil {
		t.Fatalf("drop: %v", err)
	}

	w := doWithdraw(t, h, user, "USDT", 100.0)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on accrual failure, got %d body=%s", w.Code, w.Body.String())
	}

	// LEAK CHECK: balance untouched (still 1000), and NO withdrawal tx row.
	var asset models.Asset
	db.Where("user_id = ? AND symbol = ?", user.ID, "USDT").First(&asset)
	if asset.Balance != 1000.0 {
		t.Fatalf("LEAK: balance deducted despite accrual failure: %v want 1000", asset.Balance)
	}
	var n int64
	db.Model(&models.Transaction{}).Where("user_id = ? AND type = ?", user.ID, models.TransactionTypeWithdrawal).Count(&n)
	if n != 0 {
		t.Fatalf("LEAK: %d withdrawal tx rows persisted despite rollback", n)
	}
}

