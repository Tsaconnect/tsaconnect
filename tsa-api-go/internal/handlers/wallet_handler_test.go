package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
			didit_session_id TEXT,
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
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			email_verified INTEGER DEFAULT 0,
			last_settled_global_index REAL DEFAULT 0,
			deleted_at DATETIME,
			submitted_for_verification_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
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
	ensureWalletLinkChallenges(t, db)

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

	wallet := newTestWallet(t)
	proof := signedProof(t, db, user.ID, wallet)
	body, _ := json.Marshal(map[string]interface{}{
		"walletAddress":   wallet.Address,
		"walletNonce":     proof.Nonce,
		"walletSignature": proof.Signature,
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

	wallet := newTestWallet(t)

	// Create first user and assign the address.
	otherUser := createTestUser(t, db)
	setUserWallet(t, db, otherUser, wallet.Address)
	mustRecordWalletProof(t, db, otherUser.ID, wallet.Address)

	// Create second user and try to register the same address — proven, and
	// in another letter case, and still refused: one account per wallet.
	user := createTestUser(t, db)
	proof := signedProof(t, db, user.ID, wallet)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"walletAddress":   strings.ToLower(wallet.Address),
		"walletNonce":     proof.Nonce,
		"walletSignature": proof.Signature,
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

// TestSeedDefaultTokens_StablecoinsCoverEveryConfiguredChain guards against
// the exact drift that caused the IT hub to show 0.2 USDC for a wallet
// holding 1.29 (split across Base/Arbitrum/Sonic): config.go grew Arbitrum
// and Base token addresses (commit 25d14842) but the USDT/USDC seed rows
// stayed at ["sonic", "bsc"], so GetWalletBalances' per-chain lookup — gated
// on SupportedToken.Chains — never ran on Base/Arbitrum for those symbols.
func TestSeedDefaultTokens_StablecoinsCoverEveryConfiguredChain(t *testing.T) {
	setupTestDB(t)

	created := seedDefaultTokens()

	want := map[string][]string{
		"USDT": {"sonic", "bsc", "arbitrum", "base"},
		"USDC": {"sonic", "bsc", "arbitrum", "base"},
	}
	found := map[string]bool{}
	for _, tok := range created {
		wantChains, ok := want[tok.Symbol]
		if !ok {
			continue
		}
		found[tok.Symbol] = true
		var chains []string
		if err := json.Unmarshal(tok.Chains, &chains); err != nil {
			t.Fatalf("failed to unmarshal chains for %s: %v", tok.Symbol, err)
		}
		have := map[string]bool{}
		for _, c := range chains {
			have[c] = true
		}
		for _, c := range wantChains {
			if !have[c] {
				t.Errorf("%s: expected chain %q in seeded Chains, got %v", tok.Symbol, c, chains)
			}
		}
	}
	for sym := range want {
		if !found[sym] {
			t.Errorf("expected seedDefaultTokens to create a %s row", sym)
		}
	}
}

// ---------- EnsureSupportedTokenChains ----------

// TestEnsureSupportedTokenChains_BackfillsMissingChains reproduces the
// production state directly: a supported_tokens row created back when the
// seed only knew about sonic/bsc, before Arbitrum/Base were added to
// config.go. EnsureSupportedTokenChains must add the missing chains so
// GetWalletBalances stops silently skipping them.
func TestEnsureSupportedTokenChains_BackfillsMissingChains(t *testing.T) {
	db := setupTestDB(t)

	staleChains, _ := json.Marshal([]string{"sonic", "bsc"})
	stale := models.SupportedToken{
		ID:        uuid.New(),
		Symbol:    "USDC",
		Name:      "USD Coin",
		Decimals:  6,
		IconColor: "#2775CA",
		Chains:    staleChains,
		IsActive:  true,
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatalf("failed to seed stale row: %v", err)
	}

	EnsureSupportedTokenChains()

	var got models.SupportedToken
	if err := db.Where("symbol = ?", "USDC").First(&got).Error; err != nil {
		t.Fatalf("failed to reload row: %v", err)
	}
	var chains []string
	if err := json.Unmarshal(got.Chains, &chains); err != nil {
		t.Fatalf("failed to unmarshal chains: %v", err)
	}
	have := map[string]bool{}
	for _, c := range chains {
		have[c] = true
	}
	for _, want := range []string{"sonic", "bsc", "arbitrum", "base"} {
		if !have[want] {
			t.Errorf("expected backfilled chains to include %q, got %v", want, chains)
		}
	}

	// The row's other fields — and its ID — must be untouched.
	if got.ID != stale.ID || got.Name != "USD Coin" || got.Decimals != 6 {
		t.Errorf("backfill must only touch the chains column, got %+v", got)
	}
}

// TestEnsureSupportedTokenChains_PreservesOperatorAddedChain checks the
// union is genuinely additive: a chain already present that ISN'T in
// defaultTokenDefs (there's no admin UI for this today, but the column is
// still just data) must survive the backfill rather than being replaced by
// the compiled-in list.
func TestEnsureSupportedTokenChains_PreservesOperatorAddedChain(t *testing.T) {
	db := setupTestDB(t)

	chainsJSON, _ := json.Marshal([]string{"sonic", "bsc", "polygon"})
	tok := models.SupportedToken{
		ID:        uuid.New(),
		Symbol:    "USDT",
		Name:      "Tether",
		Decimals:  6,
		IconColor: "#26A17B",
		Chains:    chainsJSON,
		IsActive:  true,
	}
	if err := db.Create(&tok).Error; err != nil {
		t.Fatalf("failed to seed row: %v", err)
	}

	EnsureSupportedTokenChains()

	var got models.SupportedToken
	if err := db.Where("symbol = ?", "USDT").First(&got).Error; err != nil {
		t.Fatalf("failed to reload row: %v", err)
	}
	var chains []string
	json.Unmarshal(got.Chains, &chains)
	have := map[string]bool{}
	for _, c := range chains {
		have[c] = true
	}
	if !have["polygon"] {
		t.Errorf("expected the pre-existing 'polygon' chain to survive the backfill, got %v", chains)
	}
	if !have["arbitrum"] || !have["base"] {
		t.Errorf("expected arbitrum/base to be added by the backfill, got %v", chains)
	}
}

// TestEnsureSupportedTokenChains_NoRowsIsNoOp guards against a boot-time
// panic/error when the table has no matching rows yet (fresh DB, before
// seedDefaultTokens has ever run).
func TestEnsureSupportedTokenChains_NoRowsIsNoOp(t *testing.T) {
	setupTestDB(t)

	EnsureSupportedTokenChains() // must not panic or error
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
		// Exactness: 64-bit float math made these one base unit short.
		{"0.13", 18, "130000000000000000", true},
		{"0.13", 6, "130000", true},
		{"1.5", 6, "1500000", true},
		{"0.000001", 6, "1", true},
		// More fractional digits than the token has is refused, never truncated.
		{"0.0000001", 6, "", false},
		// ".5" and "12." are normalised (older app versions send them); exponents,
		// signs and bare "." are malformed.
		{"12.", 6, "12000000", true},
		{".5", 6, "500000", true},
		{" 1 ", 6, "1000000", true},
		{".", 6, "", false},
		{"", 6, "", false},
		{"1e3", 6, "", false},
		{"+1", 6, "", false},
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

// ---------- erc20SendDecimals ----------

// BSC's USDT and USDC are 18-decimal. Scaling a send by the 6 other chains use
// transferred 10^-12 of the typed amount, so the contract must win over both
// the symbol and a caller's override.
func TestERC20SendDecimals(t *testing.T) {
	six, eighteen, outOfRange := 6, 18, 99
	rpcDown := errors.New("rpc down")
	tests := []struct {
		name     string
		override *int
		onChain  int
		readErr  error
		want     int
		wantErr  bool
	}{
		{"contract decimals are used", nil, 18, nil, 18, false},
		{"contract beats a disagreeing override", &six, 18, nil, 18, false},
		{"override covers an unreadable contract", &eighteen, 0, rpcDown, 18, false},
		{"out-of-range override is not a fallback", &outOfRange, 0, rpcDown, 0, true},
		{"unreadable contract without override is refused", nil, 0, rpcDown, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := erc20SendDecimals(tt.override, func() (int, error) { return tt.onChain, tt.readErr })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("decimals = %d, want %d", got, tt.want)
			}
		})
	}

	amount, _ := parseTokenAmount("10", 18)
	if amount.String() != "10000000000000000000" {
		t.Errorf("10 BSC-USDT scaled to %s base units, want 10e18", amount)
	}
}

// A tx signed by a wallet other than the user's registered one must be
// rejected BEFORE broadcast. The chain credits the tx to the SIGNER, so the
// prepared nonce (computed for the registered wallet) belongs to the wrong
// account — the tx is accepted as a future-nonce orphan and queues in the
// mempool forever: "Payment Sent" with no transfer ever landing. Regression
// for the 2026-07-08 Instant Pay incident.
func TestSubmitTx_SignerMismatchRejected(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	// Registered wallet ≠ the key that signs below.
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	// Sign a minimal legacy tx with a throwaway key (chainId 14601 = testnet
	// sonic, resolvable via ClientForChainID without any network I/O).
	key, _ := crypto.GenerateKey()
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &common.Address{0x02},
		Value:    big.NewInt(0),
		Gas:      21000,
		GasPrice: big.NewInt(1),
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(14601)), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    hexutil.Encode(raw),
		"txType":      "send",
		"tokenSymbol": "USDC",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "0.5",
		"chainId":     14601,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("signed by wallet")) {
		t.Errorf("expected signer-mismatch message, got: %s", w.Body.String())
	}
}

// ---------- Active-wallet sends (fromAddress) ----------

func TestPrepareTx_InvalidFromAddress(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol": "USDC",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "0.5",
		"chainId":     14601,
		"fromAddress": "not-an-address",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// Spend-from-active-wallet: when the client declares the sender (fromAddress)
// and the signature matches it, the signer guard must NOT reject — even though
// the signer differs from the user's registered wallet. (Receive stays on the
// registered wallet; sending follows the device's active wallet.)
func TestSubmitTx_SignerMatchesFromAddress_PassesGuard(t *testing.T) {
	// Point the testnet sonic client at an unreachable port so the broadcast
	// fails fast and hermetically AFTER the guard.
	t.Setenv("SONIC_TESTNET_RPC_URL", "http://127.0.0.1:1")

	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	key, _ := crypto.GenerateKey()
	signerAddr := crypto.PubkeyToAddress(key.PublicKey)
	tx := types.NewTx(&types.LegacyTx{
		Nonce: 0, To: &common.Address{0x02}, Value: big.NewInt(0), Gas: 21000, GasPrice: big.NewInt(1),
	})
	signed, _ := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(14601)), key)
	raw, _ := signed.MarshalBinary()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    hexutil.Encode(raw),
		"txType":      "send",
		"tokenSymbol": "USDC",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "0.5",
		"chainId":     14601,
		"fromAddress": signerAddr.Hex(),
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if bytes.Contains(w.Body.Bytes(), []byte("signed by wallet")) {
		t.Fatalf("guard rejected a signer that matches fromAddress: %s", w.Body.String())
	}
	// Broadcast against the dead RPC must fail — proves we got PAST the guard.
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 broadcast failure, got %d: %s", w.Code, w.Body.String())
	}
}

// fromAddress present but the signature belongs to someone else → still rejected.
func TestSubmitTx_FromAddressSignerMismatch(t *testing.T) {
	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	key, _ := crypto.GenerateKey()
	tx := types.NewTx(&types.LegacyTx{
		Nonce: 0, To: &common.Address{0x02}, Value: big.NewInt(0), Gas: 21000, GasPrice: big.NewInt(1),
	})
	signed, _ := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(14601)), key)
	raw, _ := signed.MarshalBinary()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"signedTx":    hexutil.Encode(raw),
		"txType":      "send",
		"tokenSymbol": "USDC",
		"toAddress":   "0x0000000000000000000000000000000000000002",
		"amount":      "0.5",
		"chainId":     14601,
		"fromAddress": "0x00000000000000000000000000000000000000AA",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitTransaction(c)

	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("signed by wallet")) {
		t.Errorf("expected signer-mismatch 400, got %d: %s", w.Code, w.Body.String())
	}
}

// The app reads `native` as "this is the chain's own coin" and swaps it by the
// native address; a contract row that claimed it would swap ETH for USDC.
func TestWalletBalanceRows_OnlyTheNativeRowIsNative(t *testing.T) {
	native := nativeBalanceRow("1.5", 2000)
	if !native.Native || native.ContractAddress != "" || native.Decimals != 18 || native.UsdValue != 3000 {
		t.Fatalf("native row = %+v", native)
	}
	usdt := contractBalanceRow("2.33", 1, "0x55d398326f99059fF775485246999027B3197955", 18)
	if usdt.Native || usdt.ContractAddress == "" || usdt.Decimals != 18 || usdt.UsdValue != 2.33 {
		t.Fatalf("contract row = %+v", usdt)
	}
	body, _ := json.Marshal(usdt)
	if strings.Contains(string(body), `"native"`) {
		t.Fatalf("contract row must not carry a native field: %s", body)
	}
}

// Every chain's own native coin is a native transfer, not an ERC-20 lookup:
// POL on Polygon and ETH on Base used to fail with "Token POL not configured
// on polygon" because only S/BNB were recognised. The RPC is unreachable, so
// reaching the transfer builder (500) rather than the token lookup (400)
// proves the native path was taken.
func TestPrepareTx_NativeCoinOfEveryChain(t *testing.T) {
	cases := []struct {
		symbol, rpcEnv string
		chainID        int64
	}{
		{"POL", "POLYGON_MAINNET_RPC_URL", 137},
		{"pol", "POLYGON_TESTNET_RPC_URL", 80002},
		{"ETH", "BASE_MAINNET_RPC_URL", 8453},
		{"ETH", "ETHEREUM_MAINNET_RPC_URL", 1},
		{"AVAX", "AVALANCHE_MAINNET_RPC_URL", 43114},
		{"BNB", "BSC_TESTNET_RPC_URL", 97},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_%d", tc.symbol, tc.chainID), func(t *testing.T) {
			t.Setenv(tc.rpcEnv, "http://127.0.0.1:1")

			db := setupTestDB(t)
			h := setupTestHandlers(t)
			user := createTestUser(t, db)
			setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("user", user)

			body, _ := json.Marshal(map[string]interface{}{
				"tokenSymbol": tc.symbol,
				"toAddress":   "0x0000000000000000000000000000000000000002",
				"amount":      "1.5",
				"chainId":     tc.chainID,
			})
			c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			h.PrepareSendTransaction(c)

			if bytes.Contains(w.Body.Bytes(), []byte("not configured")) {
				t.Fatalf("native %s was looked up as an ERC-20: %s", tc.symbol, w.Body.String())
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("expected the native transfer builder to fail on the dead RPC (%d), got %d: %s",
					http.StatusInternalServerError, w.Code, w.Body.String())
			}
		})
	}
}

// A token that is not the chain's native coin still goes through the
// configured-token lookup — ETH on Polygon is not native there.
func TestPrepareTx_ForeignNativeSymbolIsNotNative(t *testing.T) {
	t.Setenv("POLYGON_MAINNET_RPC_URL", "http://127.0.0.1:1")

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
		"amount":      "1",
		"chainId":     137,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("not configured")) {
		t.Errorf("expected S on Polygon to be refused as unconfigured (400), got %d: %s", w.Code, w.Body.String())
	}
}

// An explicit contract address is a token send, whatever its symbol says:
// a discovered (often airdropped spam) token on Base named "ETH" must not
// become a transfer of the user's real ETH. With the RPC dead, the token path
// fails reading decimals (503); the native path would fail building the
// transfer (500).
func TestPrepareTx_TokenAddressWinsOverNativeSymbol(t *testing.T) {
	t.Setenv("BASE_MAINNET_RPC_URL", "http://127.0.0.1:1")

	db := setupTestDB(t)
	h := setupTestHandlers(t)
	user := createTestUser(t, db)
	setUserWallet(t, db, user, "0x0000000000000000000000000000000000000001")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)

	body, _ := json.Marshal(map[string]interface{}{
		"tokenSymbol":  "ETH",
		"tokenAddress": "0x00000000000000000000000000000000000000E7",
		"toAddress":    "0x0000000000000000000000000000000000000002",
		"amount":       "1",
		"chainId":      8453,
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PrepareSendTransaction(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected the token path (decimals read fails, %d), got %d: %s",
			http.StatusServiceUnavailable, w.Code, w.Body.String())
	}
}
