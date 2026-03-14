package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	// Create tables with SQLite-compatible SQL (no gen_random_uuid()).
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
			documents TEXT,
			facial_verification TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT UNIQUE,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			deleted_at DATETIME,
			submitted_for_verification_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS wallets (
			id TEXT PRIMARY KEY,
			user_id TEXT UNIQUE,
			total_balance REAL,
			total_usd_value REAL,
			selected_asset TEXT,
			addresses TEXT,
			security TEXT,
			settings TEXT,
			transaction_limit REAL,
			daily_limit REAL,
			last_synced DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS wallet_transactions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			tx_hash TEXT NOT NULL UNIQUE,
			token_symbol TEXT NOT NULL,
			tx_type TEXT NOT NULL,
			from_address TEXT NOT NULL,
			to_address TEXT NOT NULL,
			amount TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			chain TEXT NOT NULL DEFAULT 'sonic',
			chain_id INTEGER NOT NULL DEFAULT 14601,
			block_number INTEGER,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS supported_tokens (
			id TEXT PRIMARY KEY,
			symbol TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			decimals INTEGER NOT NULL DEFAULT 18,
			icon_color TEXT NOT NULL DEFAULT '#888888',
			chains TEXT NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	config.DB = db
	return db
}

func setupTestHandlers(t *testing.T) *Handlers {
	t.Helper()
	cfg := config.Load()
	return &Handlers{
		BlockchainService: services.NewBlockchainService(cfg),
		Config:            cfg,
	}
}

func createTestUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	user := &models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Username: "testuser_" + uuid.New().String()[:8],
		Email:    "test_" + uuid.New().String()[:8] + "@example.com",
		Password: "$2a$10$fakehash",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}

func setUserWallet(t *testing.T, db *gorm.DB, user *models.User, addr string) {
	t.Helper()
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("wallet_address", addr)
	user.WalletAddress = addr
}

// ---------- RegisterWalletAddress tests ----------

func TestRegisterWalletAddress_Valid(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"walletAddress": "0x0000000000000000000000000000000000000001",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterWalletAddress(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestRegisterWalletAddress_InvalidFormat(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"walletAddress": "not-an-address",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterWalletAddress(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestRegisterWalletAddress_Duplicate(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)

	addr := "0x0000000000000000000000000000000000000001"

	// Create first user and assign the address.
	otherUser := createTestUser(t, db)
	setUserWallet(t, db, otherUser, addr)

	// Create second user and try to register the same address.
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"walletAddress": addr,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterWalletAddress(c)

	if w.Code != http.StatusConflict {
		t.Errorf("expected %d, got %d: %s", http.StatusConflict, w.Code, w.Body.String())
	}
}

// ---------- PrepareSendTransaction validation tests ----------

func TestPrepareTx_InvalidAddress(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "S",
		"toAddress":   "not-valid",
		"amount":      "1.0",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestPrepareTx_InvalidAmount(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "S",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "-1",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestPrepareTx_NoWallet(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "S",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "1.0",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestPrepareTx_UnsupportedChain(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "S",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "1.0",
		"chainId":     99999,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- SubmitTransaction validation tests ----------

func TestSubmitTx_InvalidSignedTx(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    "not-valid-hex",
		"txType":      "send",
		"tokenSymbol": "S",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "1.0",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestSubmitTx_InvalidTxType(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    "0xdeadbeef",
		"txType":      "invalid_type",
		"tokenSymbol": "S",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "1.0",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestSubmitTx_UnsupportedChain(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    "0xdeadbeef",
		"txType":      "send",
		"tokenSymbol": "S",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "1.0",
		"chainId":     99999,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- ConfirmSeedPhraseBackup ----------

func TestConfirmSeedPhraseBackup(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	c.Request, _ = http.NewRequest("POST", "/", nil)

	h.ConfirmSeedPhraseBackup(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// Verify DB was updated.
	var updated models.User
	if err := db.First(&updated, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("failed to reload user: %v", err)
	}
	if !updated.SeedPhraseBackedUp {
		t.Error("expected SeedPhraseBackedUp to be true after confirmation")
	}
}

// ---------- GetTransactionHistory ----------

func TestGetTransactionHistory(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)

	// Create 3 transaction records.
	for i := 1; i <= 3; i++ {
		tx := models.WalletTransaction{
			ID:          uuid.New(),
			UserID:      user.ID,
			TxHash:      fmt.Sprintf("0x%064x", i),
			TokenSymbol: "S",
			TxType:      models.TxTypeSend,
			FromAddress: "0x0000000000000000000000000000000000000001",
			ToAddress:   "0x0000000000000000000000000000000000000002",
			Amount:      "1.0",
			Status:      models.TxStatusPending,
			Chain:       "sonic",
			ChainID:     14601,
		}
		if err := db.Create(&tx).Error; err != nil {
			t.Fatalf("failed to create test tx %d: %v", i, err)
		}
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	c.Request, _ = http.NewRequest("GET", "/?page=1&limit=20", nil)

	h.GetTransactionHistory(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}

	txs, ok := data["transactions"].([]interface{})
	if !ok {
		t.Fatalf("expected transactions to be an array, got %T", data["transactions"])
	}

	if len(txs) != 3 {
		t.Errorf("expected 3 transactions, got %d", len(txs))
	}
}

// ---------- GetSupportedTokens ----------

func TestGetSupportedTokens_AutoSeed(t *testing.T) {
	setupTestDB(t)
	h := setupTestHandlers(t)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request, _ = http.NewRequest("GET", "/", nil)

	h.GetSupportedTokens(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("expected data to be an array, got %T", resp["data"])
	}

	if len(data) < 3 {
		t.Errorf("expected at least 3 seeded tokens, got %d", len(data))
	}
}

// ---------- parseTokenAmount ----------

func TestParseTokenAmount(t *testing.T) {
	tests := []struct {
		amount   string
		decimals int
		wantStr  string
		wantOK   bool
	}{
		{"1", 18, "1000000000000000000", true},
		{"0.5", 18, "500000000000000000", true},
		{"10.5", 6, "10500000", true},
		{"0", 18, "", false},
		{"-1", 18, "", false},
		{"abc", 18, "", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%d", tt.amount, tt.decimals), func(t *testing.T) {
			result, ok := parseTokenAmount(tt.amount, tt.decimals)
			if ok != tt.wantOK {
				t.Errorf("parseTokenAmount(%q, %d): got ok=%v, want ok=%v", tt.amount, tt.decimals, ok, tt.wantOK)
				return
			}
			if ok && result.String() != tt.wantStr {
				t.Errorf("parseTokenAmount(%q, %d) = %s, want %s", tt.amount, tt.decimals, result.String(), tt.wantStr)
			}
		})
	}
}
