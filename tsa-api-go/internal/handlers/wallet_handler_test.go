package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// setupTestRouter creates a Gin engine in test mode with the user injected into context.
func setupTestRouter(user *models.User) (*gin.Engine, *Handlers) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &Handlers{}

	// Middleware to inject user into context.
	router.Use(func(c *gin.Context) {
		if user != nil {
			c.Set("user", *user)
		}
		c.Next()
	})

	return router, h
}

// testUser returns a test user with the given wallet address.
func testUser(walletAddress string) *models.User {
	return &models.User{
		ID:            uuid.New(),
		Name:          "Test User",
		Username:      "testuser",
		Email:         "test@example.com",
		WalletAddress: walletAddress,
	}
}

func TestIsValidEthAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		valid   bool
	}{
		{"valid lowercase", "0x1234567890abcdef1234567890abcdef12345678", true},
		{"valid uppercase", "0x1234567890ABCDEF1234567890ABCDEF12345678", true},
		{"valid mixed case", "0x1234567890AbCdEf1234567890aBcDeF12345678", true},
		{"missing 0x prefix", "1234567890abcdef1234567890abcdef12345678", false},
		{"too short", "0x1234567890abcdef", false},
		{"too long", "0x1234567890abcdef1234567890abcdef1234567890", false},
		{"invalid chars", "0x1234567890ghijkl1234567890abcdef12345678", false},
		{"empty string", "", false},
		{"just 0x", "0x", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidEthAddress(tt.address)
			if got != tt.valid {
				t.Errorf("isValidEthAddress(%q) = %v, want %v", tt.address, got, tt.valid)
			}
		})
	}
}

func TestIsPositiveAmount(t *testing.T) {
	tests := []struct {
		name   string
		amount string
		valid  bool
	}{
		{"positive integer", "10", true},
		{"positive decimal", "10.5", true},
		{"zero", "0", false},
		{"negative", "-5", false},
		{"not a number", "abc", false},
		{"empty", "", false},
		{"very small positive", "0.001", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPositiveAmount(tt.amount)
			if got != tt.valid {
				t.Errorf("isPositiveAmount(%q) = %v, want %v", tt.amount, got, tt.valid)
			}
		})
	}
}

func TestRegisterWalletAddress_InvalidFormat(t *testing.T) {
	user := testUser("")
	router, h := setupTestRouter(user)
	router.POST("/api/wallet/register", h.RegisterWalletAddress)

	tests := []struct {
		name    string
		address string
		code    int
	}{
		{"no 0x prefix", "1234567890abcdef1234567890abcdef12345678", http.StatusBadRequest},
		{"too short", "0x12345678", http.StatusBadRequest},
		{"invalid hex chars", "0xGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG", http.StatusBadRequest},
		{"empty address", "", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"walletAddress": tt.address})
			req, _ := http.NewRequest("POST", "/api/wallet/register", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.code {
				t.Errorf("expected status %d, got %d. Body: %s", tt.code, w.Code, w.Body.String())
			}

			var resp map[string]interface{}
			json.Unmarshal(w.Body.Bytes(), &resp)
			if resp["success"] != false {
				t.Errorf("expected success=false, got %v", resp["success"])
			}
		})
	}
}

func TestRegisterWalletAddress_SameAddress(t *testing.T) {
	existingAddr := "0x1234567890abcdef1234567890abcdef12345678"
	user := testUser(existingAddr)
	router, h := setupTestRouter(user)
	router.POST("/api/wallet/register", h.RegisterWalletAddress)

	body, _ := json.Marshal(map[string]string{
		"walletAddress": existingAddr,
	})
	req, _ := http.NewRequest("POST", "/api/wallet/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d. Body: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
}

// TestRegisterWalletAddress_ReplaceExisting requires a database connection
// and is covered by integration tests.

func TestRegisterWalletAddress_Unauthenticated(t *testing.T) {
	router, h := setupTestRouter(nil)
	router.POST("/api/wallet/register", h.RegisterWalletAddress)

	body, _ := json.Marshal(map[string]string{
		"walletAddress": "0x1234567890abcdef1234567890abcdef12345678",
	})
	req, _ := http.NewRequest("POST", "/api/wallet/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestGetWalletBalances_NoWalletAddress(t *testing.T) {
	user := testUser("")
	router, h := setupTestRouter(user)
	router.GET("/api/wallet/balances", h.GetWalletBalances)

	req, _ := http.NewRequest("GET", "/api/wallet/balances", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestGetWalletBalances_WithWalletAddress(t *testing.T) {
	user := testUser("0x1234567890abcdef1234567890abcdef12345678")
	router, h := setupTestRouter(user)
	router.GET("/api/wallet/balances", h.GetWalletBalances)

	req, _ := http.NewRequest("GET", "/api/wallet/balances", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d. Body: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data to be a map")
	}
	balances, ok := data["balances"].(map[string]interface{})
	if !ok {
		t.Fatal("expected balances to be a map")
	}
	for _, token := range []string{"MCGP", "USDT", "USDC", "S"} {
		if _, exists := balances[token]; !exists {
			t.Errorf("expected balance for %s", token)
		}
	}
}

func TestPrepareSendTransaction_InvalidToken(t *testing.T) {
	user := testUser("0x1234567890abcdef1234567890abcdef12345678")
	router, h := setupTestRouter(user)
	router.POST("/api/wallet/prepare-tx", h.PrepareSendTransaction)

	body, _ := json.Marshal(map[string]string{
		"tokenSymbol": "INVALID",
		"toAddress":   "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"amount":      "10",
	})
	req, _ := http.NewRequest("POST", "/api/wallet/prepare-tx", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestPrepareSendTransaction_InvalidAmount(t *testing.T) {
	user := testUser("0x1234567890abcdef1234567890abcdef12345678")
	router, h := setupTestRouter(user)
	router.POST("/api/wallet/prepare-tx", h.PrepareSendTransaction)

	body, _ := json.Marshal(map[string]string{
		"tokenSymbol": "USDT",
		"toAddress":   "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"amount":      "-5",
	})
	req, _ := http.NewRequest("POST", "/api/wallet/prepare-tx", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestPrepareSendTransaction_Valid(t *testing.T) {
	user := testUser("0x1234567890abcdef1234567890abcdef12345678")
	router, h := setupTestRouter(user)
	router.POST("/api/wallet/prepare-tx", h.PrepareSendTransaction)

	body, _ := json.Marshal(map[string]string{
		"tokenSymbol": "USDT",
		"toAddress":   "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"amount":      "10.5",
	})
	req, _ := http.NewRequest("POST", "/api/wallet/prepare-tx", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d. Body: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
}

func TestConfirmSeedPhraseBackup_Unauthenticated(t *testing.T) {
	router, h := setupTestRouter(nil)
	router.POST("/api/wallet/seed-phrase-backed-up", h.ConfirmSeedPhraseBackup)

	req, _ := http.NewRequest("POST", "/api/wallet/seed-phrase-backed-up", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestSubmitTransaction_InvalidTxType(t *testing.T) {
	user := testUser("0x1234567890abcdef1234567890abcdef12345678")
	router, h := setupTestRouter(user)
	router.POST("/api/wallet/submit-tx", h.SubmitTransaction)

	body, _ := json.Marshal(map[string]string{
		"signedTx":    "0xabcdef",
		"txType":      "invalid",
		"tokenSymbol": "USDT",
		"toAddress":   "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"amount":      "10",
	})
	req, _ := http.NewRequest("POST", "/api/wallet/submit-tx", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d. Body: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestGetTransactionHistory_Unauthenticated(t *testing.T) {
	router, h := setupTestRouter(nil)
	router.GET("/api/wallet/transactions", h.GetTransactionHistory)

	req, _ := http.NewRequest("GET", "/api/wallet/transactions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}
