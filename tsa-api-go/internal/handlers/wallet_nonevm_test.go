package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func TestValidNonEVMTxHash(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	// A real-length base58 (88 chars, base58 alphabet) stands in for a Solana sig.
	solValid := strings.Repeat("A", 88)

	cases := []struct {
		family string
		hash   string
		want   bool
	}{
		{"tron", hex64, true},
		{"btc", hex64, true},
		{"tron", solValid, false},                  // base58 too long / non-hex for tron
		{"solana", hex64, false},                   // 64 hex is too short for a base58 sig gate
		{"solana", solValid, true},                 // base58, in-range length
		{"solana", "abc", false},                   // too short
		{"solana", strings.Repeat("A", 95), false}, // too long
		{"btc", "xyz", false},                      // non-hex
	}
	for _, tc := range cases {
		if got := validNonEVMTxHash(tc.family, tc.hash); got != tc.want {
			t.Errorf("validNonEVMTxHash(%q, len=%d) = %v, want %v", tc.family, len(tc.hash), got, tc.want)
		}
	}
}

// fakeChainClient is a minimal chains.ChainClient for handler tests. Only the
// methods the submit path touches are meaningful; the rest are stubs.
type fakeChainClient struct {
	family      string
	broadcasted bool
}

func (f *fakeChainClient) Family() string               { return f.family }
func (f *fakeChainClient) ValidateAddress(string) error { return nil }
func (f *fakeChainClient) GetBalances(context.Context, string) ([]chains.Balance, error) {
	return nil, nil
}
func (f *fakeChainClient) EstimateFee(context.Context, chains.TransferRequest) (chains.FeeQuote, error) {
	return chains.FeeQuote{}, nil
}
func (f *fakeChainClient) PrepareTransfer(context.Context, chains.TransferRequest) (chains.PreparedTx, error) {
	return chains.PreparedTx{}, nil
}
func (f *fakeChainClient) AssembleAndBroadcast(context.Context, chains.PreparedTx, []chains.Signature) (string, error) {
	f.broadcasted = true
	return "txhash123", nil
}
func (f *fakeChainClient) GetTxStatus(context.Context, string) (chains.TxStatus, error) {
	return chains.TxStatus{}, nil
}
func (f *fakeChainClient) History(context.Context, string, string) ([]chains.Activity, string, error) {
	return nil, "", nil
}

// nonEVMTables adds the wallet_addresses + wallet_activity tables the submit
// path needs (setupTestDB doesn't create them).
func nonEVMTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS wallet_addresses (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, wallet_id TEXT NOT NULL,
			chain_key TEXT NOT NULL, family TEXT NOT NULL, address TEXT NOT NULL,
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS wallet_activity (
			id TEXT PRIMARY KEY, chain_key TEXT NOT NULL, network TEXT NOT NULL DEFAULT 'mainnet',
			address TEXT NOT NULL, hash TEXT NOT NULL, direction TEXT NOT NULL,
			amount_raw TEXT NOT NULL, symbol TEXT NOT NULL, counterparty TEXT,
			status TEXT NOT NULL DEFAULT 'pending', confirmations INTEGER NOT NULL DEFAULT 0,
			block_time INTEGER, created_at DATETIME, updated_at DATETIME)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create nonevm table: %v", err)
		}
	}
}

func submitReqBody(from string) string {
	b, _ := json.Marshal(submitNonEVMRequest{
		Prepared: chains.PreparedTx{Family: "solana", Raw: "00"},
		Sigs:     []chains.Signature{{Index: 0, SigHex: strings.Repeat("a", 128)}},
		From:     from, Amount: "1000", Symbol: "SOL",
	})
	return string(b)
}

func TestSubmitNonEVMTx_RejectsUnregisteredSender(t *testing.T) {
	db := setupTestDB(t)
	nonEVMTables(t, db)
	user := createTestUser(t, db)

	reg := chains.NewRegistry()
	fake := &fakeChainClient{family: "solana"}
	reg.Register("mainnet", "solana", fake)
	h := setupTestHandlers(t)
	h.ChainRegistry = reg

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/wallet/nonevm/submit-tx?chain=solana&network=mainnet",
		strings.NewReader(submitReqBody("SoLnotRegistered")))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitNonEVMTx(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unregistered sender, got %d: %s", w.Code, w.Body.String())
	}
	if fake.broadcasted {
		t.Errorf("must NOT broadcast when the sender address isn't registered to the user")
	}
}

func TestSubmitNonEVMTx_AllowsRegisteredSender(t *testing.T) {
	db := setupTestDB(t)
	nonEVMTables(t, db)
	user := createTestUser(t, db)

	const from = "SoLregisteredAddr"
	if err := db.Exec(
		`INSERT INTO wallet_addresses (id, user_id, wallet_id, chain_key, family, address) VALUES (?,?,?,?,?,?)`,
		"a1", user.ID.String(), "0xwallet", "solana", "solana", from).Error; err != nil {
		t.Fatalf("seed address: %v", err)
	}

	reg := chains.NewRegistry()
	fake := &fakeChainClient{family: "solana"}
	reg.Register("mainnet", "solana", fake)
	h := setupTestHandlers(t)
	h.ChainRegistry = reg

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Request = httptest.NewRequest(http.MethodPost,
		"/wallet/nonevm/submit-tx?chain=solana&network=mainnet",
		strings.NewReader(submitReqBody(from)))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SubmitNonEVMTx(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for registered sender, got %d: %s", w.Code, w.Body.String())
	}
	if !fake.broadcasted {
		t.Errorf("expected broadcast for a registered sender")
	}
	// The audit row is recorded with the verified address.
	var count int64
	db.Model(&models.WalletActivity{}).Where("hash = ? AND address = ?", "txhash123", from).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 activity row for the verified sender, got %d", count)
	}
}
