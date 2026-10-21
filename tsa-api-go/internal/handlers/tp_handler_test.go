package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	sqls := []string{
		`CREATE TABLE IF NOT EXISTS users (
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
		`CREATE TABLE IF NOT EXISTS tp_earnings (
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
		`CREATE TABLE IF NOT EXISTS cashback_earnings (
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
	}
	for _, sql := range sqls {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	config.DB = db
	return db
}

func createTPTestUser(t *testing.T, db *gorm.DB, name, username string, referredBy *uuid.UUID) models.User {
	t.Helper()
	id := uuid.New()
	user := models.User{
		ID:            id,
		Name:          name,
		Username:      username,
		Email:         username + "@test.com",
		Password:      "hashedpassword",
		ReferralCode:  username,
		ReferredBy:    referredBy,
		AccountStatus: models.AccountStatusActive,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create test user %s: %v", name, err)
	}
	return user
}

// ---------- HTTP handler endpoint tests ----------

func setupTPHandlers(t *testing.T) *Handlers {
	t.Helper()
	return &Handlers{}
}

// addFeeV2Tables adds the v2 engine's tables to a TP test DB so a real
// FeeDistributionService can run against it.
func addFeeV2Tables(t *testing.T, db *gorm.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS fee_distributions (
			id TEXT PRIMARY KEY, source_type TEXT NOT NULL, source_id TEXT NOT NULL,
			user_id TEXT NOT NULL, feature TEXT NOT NULL, token TEXT NOT NULL,
			fee_usd REAL NOT NULL, system_usd REAL NOT NULL, buyer_cashback_usd REAL NOT NULL,
			upline_cashback_usd REAL NOT NULL, provider_cashback_usd REAL NOT NULL DEFAULT 0,
			tp_pool_usd REAL NOT NULL,
			global_pool_usd REAL NOT NULL DEFAULT 0, tp_per_usd REAL NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL, UNIQUE(source_type, source_id)
		)`,
		`CREATE TABLE IF NOT EXISTS global_tp_pool (
			singleton INTEGER PRIMARY KEY, accrued_usd REAL NOT NULL DEFAULT 0,
			accrued_tp REAL NOT NULL DEFAULT 0,
			per_user_index_scaled INTEGER NOT NULL DEFAULT 0,
			indexed_tp REAL NOT NULL DEFAULT 0,
			last_payout_at DATETIME,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS global_tp_accruals (
			id TEXT PRIMARY KEY, source_type TEXT NOT NULL, source_id TEXT NOT NULL,
			reason TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT -1,
			amount_usd REAL NOT NULL, amount_tp REAL NOT NULL, created_at DATETIME NOT NULL
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create fee v2 table: %v", err)
		}
	}
}

// TestSettleFeeEarnings_CreditsCashbackOnce is the regression guard for SP3
// findings #1/#4: the fee engine (settleFeeEarnings → OnFeeSettled) is the SOLE
// cashback source and credits the buyer (gen0) + direct upline EXACTLY ONCE
// each. (The legacy on-chain RecordCashbackEarning blocks that used to run
// alongside it have been removed with the v1 retirement, so there is no longer a
// double-credit path — this locks the single-credit invariant.)
func TestSettleFeeEarnings_CreditsCashbackOnce(t *testing.T) {
	db := setupTPTestDB(t)
	addFeeV2Tables(t, db)
	// Pin to one connection: :memory: SQLite is per-connection and the engine's
	// upline read + write tx must see the same schema.
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)

	upline := createTPTestUser(t, db, "Upline", "settle_upline", nil)
	buyer := createTPTestUser(t, db, "Buyer", "settle_buyer", &upline.ID)

	feeSvc := services.NewFeeDistributionService(db, services.NewReferralGraphService(db), 1.0)

	srcID := uuid.New()
	// The checkout hook settles the 2% MERCHANT fee under FeatureProductMerchant
	// (the carved {0.50,0.15,0.15,0.00,0.20} split). Buyer + upline each get 15%.
	ev := services.FeeEvent{
		Feature: models.FeatureProductMerchant, Token: "USDT", FeeUSD: 10.0,
		UserID: buyer.ID, SourceType: "checkout", SourceID: srcID,
	}

	// settleFeeEarnings enqueues async; call the engine directly so the assertion
	// is deterministic (the handler's call site is identical).
	if err := feeSvc.OnFeeSettled(context.Background(), ev); err != nil {
		t.Fatalf("OnFeeSettled: %v", err)
	}

	// Exactly ONE buyer cashback row + ONE upline cashback row for this source.
	var buyerRows, uplineRows int64
	db.Model(&models.CashbackEarning{}).
		Where("source_id = ? AND user_id = ?", srcID, buyer.ID).Count(&buyerRows)
	db.Model(&models.CashbackEarning{}).
		Where("source_id = ? AND user_id = ?", srcID, upline.ID).Count(&uplineRows)
	if buyerRows != 1 {
		t.Fatalf("expected exactly 1 buyer cashback row under v2, got %d (double-count?)", buyerRows)
	}
	if uplineRows != 1 {
		t.Fatalf("expected exactly 1 upline cashback row under v2, got %d (double-count?)", uplineRows)
	}

	// And the credited balances reflect a single 15% credit each (= $1.5), not a
	// doubled/legacy-base amount (product_merchant carved split: buyer 15%, upline 15%).
	var b, u models.User
	db.First(&b, "id = ?", buyer.ID)
	db.First(&u, "id = ?", upline.ID)
	if b.CashbackBalance != 1.5 {
		t.Fatalf("buyer cashback balance = %v, want exactly 1.5 (single 15%% credit)", b.CashbackBalance)
	}
	if u.CashbackBalance != 1.5 {
		t.Fatalf("upline cashback balance = %v, want exactly 1.5 (single 15%% credit)", u.CashbackBalance)
	}
}

func TestGetTPBalance(t *testing.T) {
	db := setupTPTestDB(t)
	h := setupTPHandlers(t)

	user := createTPTestUser(t, db, "Alice", "alice_bal", nil)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("tp_balance", 5.5)
	user.TPBalance = 5.5

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request, _ = http.NewRequest("GET", "/tp-balance", nil)

	h.GetTPBalance(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}
	if data["tpBalance"] != 5.5 {
		t.Errorf("expected tpBalance=5.5, got %v", data["tpBalance"])
	}
}

func TestGetTPEarnings(t *testing.T) {
	db := setupTPTestDB(t)
	h := setupTPHandlers(t)

	user := createTPTestUser(t, db, "Bob", "bob_earn", nil)
	sourceUser := createTPTestUser(t, db, "Source", "source_earn", nil)

	for i := 0; i < 2; i++ {
		earning := models.TPEarning{
			ID:           uuid.New(),
			UserID:       user.ID,
			SourceUserID: sourceUser.ID,
			SourceType:   "checkout",
			SourceID:     uuid.New(),
			Generation:   0,
			FeeAmountUSD: 2.0,
			Percentage:   0.354,
			TPEarned:     0.00708,
			CreatedAt:    time.Now(),
		}
		if err := db.Create(&earning).Error; err != nil {
			t.Fatalf("failed to create tp_earning %d: %v", i, err)
		}
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request, _ = http.NewRequest("GET", "/tp-earnings?page=1&limit=10", nil)

	h.GetTPEarnings(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}
	earningsArr, ok := data["earnings"].([]interface{})
	if !ok {
		t.Fatalf("expected earnings to be an array, got %T", data["earnings"])
	}
	if len(earningsArr) != 2 {
		t.Errorf("expected 2 earnings, got %d", len(earningsArr))
	}
	if data["total"] != float64(2) {
		t.Errorf("expected total=2, got %v", data["total"])
	}
	if data["page"] != float64(1) {
		t.Errorf("expected page=1, got %v", data["page"])
	}
}

func TestGetReferralsWithTP(t *testing.T) {
	db := setupTPTestDB(t)
	h := setupTPHandlers(t)

	userA := createTPTestUser(t, db, "Alice", "alice_ref", nil)
	userB := createTPTestUser(t, db, "Bob", "bob_ref", &userA.ID)

	earning := models.TPEarning{
		ID:           uuid.New(),
		UserID:       userA.ID,
		SourceUserID: userB.ID,
		SourceType:   "checkout",
		SourceID:     uuid.New(),
		Generation:   1,
		FeeAmountUSD: 2.0,
		Percentage:   0.177,
		TPEarned:     0.00354,
		CreatedAt:    time.Now(),
	}
	if err := db.Create(&earning).Error; err != nil {
		t.Fatalf("failed to create tp_earning: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &userA)
	c.Request, _ = http.NewRequest("GET", "/referrals", nil)

	h.GetReferralsWithTP(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}
	if data["totalReferrals"] != float64(1) {
		t.Errorf("expected totalReferrals=1, got %v", data["totalReferrals"])
	}
	referrals, ok := data["referrals"].([]interface{})
	if !ok {
		t.Fatalf("expected referrals to be an array, got %T", data["referrals"])
	}
	if len(referrals) != 1 {
		t.Fatalf("expected 1 referral, got %d", len(referrals))
	}
	referral, ok := referrals[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected referral to be an object")
	}
	if referral["username"] != userB.Username {
		t.Errorf("expected referral username=%s, got %v", userB.Username, referral["username"])
	}
	if _, hasTP := referral["tpContributed"]; !hasTP {
		t.Error("expected referral to have tpContributed field")
	}
}

func TestGetCashbackBalance(t *testing.T) {
	db := setupTPTestDB(t)
	h := setupTPHandlers(t)

	user := createTPTestUser(t, db, "Alice", "alice_cb3", nil)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("cashback_balance", 3.25)
	user.CashbackBalance = 3.25

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request, _ = http.NewRequest("GET", "/cashback-balance", nil)

	h.GetCashbackBalance(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}
	if data["cashbackBalance"] != 3.25 {
		t.Errorf("expected cashbackBalance=3.25, got %v", data["cashbackBalance"])
	}
}

func TestGetReferralsWithTP_Empty(t *testing.T) {
	db := setupTPTestDB(t)
	h := setupTPHandlers(t)

	user := createTPTestUser(t, db, "Solo", "solo_ref", nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request, _ = http.NewRequest("GET", "/referrals", nil)

	h.GetReferralsWithTP(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}
	if data["totalReferrals"] != float64(0) {
		t.Errorf("expected totalReferrals=0, got %v", data["totalReferrals"])
	}
	referrals, ok := data["referrals"].([]interface{})
	if !ok {
		t.Fatalf("expected referrals to be an array, got %T", data["referrals"])
	}
	if len(referrals) != 0 {
		t.Errorf("expected empty referrals array, got %d items", len(referrals))
	}
}
