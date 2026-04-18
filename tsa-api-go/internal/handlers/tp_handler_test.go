package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
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
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
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

func TestDistributeTPEarnings_ThreeUserChain(t *testing.T) {
	db := setupTPTestDB(t)

	userA := createTPTestUser(t, db, "Alice", "alice", nil)
	userB := createTPTestUser(t, db, "Bob", "bob", &userA.ID)
	userC := createTPTestUser(t, db, "Charlie", "charlie", &userB.ID)

	sourceID := uuid.New()
	systemFee := 2.10

	err := DistributeTPEarnings(db, userC.ID, "checkout", sourceID, systemFee)
	if err != nil {
		t.Fatalf("DistributeTPEarnings failed: %v", err)
	}

	var earnings []models.TPEarning
	db.Order("generation ASC").Find(&earnings)
	if len(earnings) != 3 {
		t.Fatalf("expected 3 tp_earnings rows, got %d", len(earnings))
	}

	if earnings[0].UserID != userC.ID || earnings[0].Generation != 0 {
		t.Errorf("gen 0: expected userC, got user %s gen %d", earnings[0].UserID, earnings[0].Generation)
	}
	expectedTP0 := systemFee * 0.354
	if diff := earnings[0].TPEarned - expectedTP0; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("gen 0 TP: expected %f, got %f", expectedTP0, earnings[0].TPEarned)
	}

	if earnings[1].UserID != userB.ID || earnings[1].Generation != 1 {
		t.Errorf("gen 1: expected userB, got user %s gen %d", earnings[1].UserID, earnings[1].Generation)
	}
	expectedTP1 := systemFee * 0.177
	if diff := earnings[1].TPEarned - expectedTP1; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("gen 1 TP: expected %f, got %f", expectedTP1, earnings[1].TPEarned)
	}

	if earnings[2].UserID != userA.ID || earnings[2].Generation != 2 {
		t.Errorf("gen 2: expected userA, got user %s gen %d", earnings[2].UserID, earnings[2].Generation)
	}
	expectedTP2 := systemFee * 0.142
	if diff := earnings[2].TPEarned - expectedTP2; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("gen 2 TP: expected %f, got %f", expectedTP2, earnings[2].TPEarned)
	}

	var updatedA, updatedB, updatedC models.User
	db.First(&updatedA, "id = ?", userA.ID)
	db.First(&updatedB, "id = ?", userB.ID)
	db.First(&updatedC, "id = ?", userC.ID)

	if diff := updatedC.TPBalance - expectedTP0; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("userC tp_balance: expected %f, got %f", expectedTP0, updatedC.TPBalance)
	}
	if diff := updatedB.TPBalance - expectedTP1; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("userB tp_balance: expected %f, got %f", expectedTP1, updatedB.TPBalance)
	}
	if diff := updatedA.TPBalance - expectedTP2; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("userA tp_balance: expected %f, got %f", expectedTP2, updatedA.TPBalance)
	}
}

func TestDistributeTPEarnings_NoReferrer(t *testing.T) {
	db := setupTPTestDB(t)

	user := createTPTestUser(t, db, "Solo", "solo", nil)

	sourceID := uuid.New()
	systemFee := 0.10

	err := DistributeTPEarnings(db, user.ID, "service_contact", sourceID, systemFee)
	if err != nil {
		t.Fatalf("DistributeTPEarnings failed: %v", err)
	}

	var earnings []models.TPEarning
	db.Find(&earnings)
	if len(earnings) != 1 {
		t.Fatalf("expected 1 tp_earnings row, got %d", len(earnings))
	}
	if earnings[0].Generation != 0 {
		t.Errorf("expected generation 0, got %d", earnings[0].Generation)
	}
}

func TestDistributeTPEarnings_SuspendedUserStopsChain(t *testing.T) {
	db := setupTPTestDB(t)

	userA := createTPTestUser(t, db, "Alice", "alice2", nil)
	userB := createTPTestUser(t, db, "Bob", "bob2", &userA.ID)
	db.Model(&userB).Update("account_status", models.AccountStatusSuspended)
	userC := createTPTestUser(t, db, "Charlie", "charlie2", &userB.ID)

	sourceID := uuid.New()
	err := DistributeTPEarnings(db, userC.ID, "checkout", sourceID, 2.10)
	if err != nil {
		t.Fatalf("DistributeTPEarnings failed: %v", err)
	}

	var earnings []models.TPEarning
	db.Find(&earnings)
	if len(earnings) != 1 {
		t.Fatalf("expected 1 tp_earnings row (chain stopped at suspended), got %d", len(earnings))
	}
}

// ---------- HTTP handler endpoint tests ----------

func setupTPHandlers(t *testing.T) *Handlers {
	t.Helper()
	return &Handlers{}
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
