package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEnrichBalancesWithUSD(t *testing.T) {
	ps := services.NewPriceService()
	ps.SeedPriceForTest("TRX", services.PriceData{USD: 0.5})

	out := enrichBalancesWithUSD(ps, []chains.Balance{
		{Symbol: "TRX", Raw: "2000000000", Decimals: 6, IsNative: true}, // 2000 TRX
	})
	if len(out) != 1 {
		t.Fatalf("want 1 balance, got %d", len(out))
	}
	if out[0].UsdPrice != 0.5 {
		t.Fatalf("usdPrice: want 0.5, got %v", out[0].UsdPrice)
	}
	if out[0].UsdValue != 1000 { // 2000 * 0.5
		t.Fatalf("usdValue: want 1000, got %v", out[0].UsdValue)
	}
}

func TestEnrichBalancesBestEffortWhenNoPriceService(t *testing.T) {
	out := enrichBalancesWithUSD(nil, []chains.Balance{
		{Symbol: "TRX", Raw: "1000000", Decimals: 6, IsNative: true},
	})
	if out[0].UsdPrice != 0 || out[0].UsdValue != 0 {
		t.Fatalf("nil price service must yield 0 USD, got %+v", out[0])
	}
}

// stubTron is a hand fake of chains.ChainClient for handler wiring tests.
type stubTron struct{ chains.ChainClient }

func (stubTron) Family() string                 { return "tron" }
func (stubTron) ValidateAddress(a string) error { return nil }
func (stubTron) GetBalances(context.Context, string) ([]chains.Balance, error) {
	return []chains.Balance{{Symbol: "TRX", Raw: "5", Decimals: 6, IsNative: true}}, nil
}

func TestRegistryResolvesFamily(t *testing.T) {
	r := chains.NewRegistry()
	r.Register("mainnet", "tron", stubTron{})
	c, ok := r.ForKey("mainnet", "tron")
	if !ok {
		t.Fatal("tron not registered")
	}
	bals, _ := c.GetBalances(context.Background(), "TXYZ")
	if len(bals) != 1 || bals[0].Symbol != "TRX" {
		t.Fatalf("unexpected balances: %+v", bals)
	}
}

// stubBalances returns a network-distinguishable symbol so a test can prove
// which client the handler routed to.
type stubBalances struct {
	chains.ChainClient
	sym string
}

func (stubBalances) Family() string               { return "tron" }
func (stubBalances) ValidateAddress(string) error { return nil }
func (s stubBalances) GetBalances(context.Context, string) ([]chains.Balance, error) {
	return []chains.Balance{{Symbol: s.sym, Raw: "1", Decimals: 6, IsNative: true}}, nil
}

// TestGetNonEVMBalances_RoutesByNetwork proves ?network= selects the matching
// per-network client (the dual-network wiring), not just the default mainnet.
func TestGetNonEVMBalances_RoutesByNetwork(t *testing.T) {
	reg := chains.NewRegistry()
	reg.Register("mainnet", "tron", stubBalances{sym: "MAINNET"})
	reg.Register("testnet", "tron", stubBalances{sym: "TESTNET"})
	h := &Handlers{ChainRegistry: reg}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/?chain=tron&address=TXYZ&network=testnet", nil)

	h.GetNonEVMBalances(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Balances []chains.Balance `json:"balances"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Balances) != 1 || resp.Data.Balances[0].Symbol != "TESTNET" {
		t.Fatalf("expected the testnet client to serve, got %+v", resp.Data.Balances)
	}
}

// TestRegisterNonEVM_OwnershipScoping proves that a caller cannot overwrite
// another user's registered non-EVM address by passing that user's walletId.
// User B owns wallet "0xB" with Tron address "ToldB". User A calls
// RegisterNonEVMAddress with walletId="0xB" and address="TevilA". The WHERE
// clause includes user_id=A which misses user B's row, so FirstOrCreate inserts
// a new row (which the unique index on (wallet_id, chain_key) rejects), and
// user B's original address must remain unchanged.
func TestRegisterNonEVM_OwnershipScoping(t *testing.T) {
	// Fresh SQLite in-memory DB — migrate only WalletAddress to avoid the
	// pre-existing fee_distributions SQLite failure unrelated to this task.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.WalletAddress{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	config.DB = db

	uuidB := uuid.New()
	uuidA := uuid.New()

	// Seed user B's row.
	seedRow := models.WalletAddress{
		UserID:   uuidB,
		WalletID: "0xB",
		ChainKey: "tron",
		Family:   "tron",
		Address:  "ToldB",
	}
	if err := db.Create(&seedRow).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Wire a registry with a stub tron client.
	reg := chains.NewRegistry()
	reg.Register("mainnet", "tron", stubTron{})

	h := &Handlers{ChainRegistry: reg}

	// Call RegisterNonEVMAddress AS user A, targeting wallet "0xB".
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &models.User{ID: uuidA})

	body, _ := json.Marshal(map[string]string{
		"walletId": "0xB",
		"chainKey": "tron",
		"address":  "TevilA",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterNonEVMAddress(c)

	// The security invariant: whatever user A's call does, user B's own row is
	// never mutated or hijacked. With the (user_id, wallet_id, chain_key) unique
	// index, A's upsert is scoped to A's user_id, so it can only ever touch A's
	// own row — B stays intact and B is not locked out.
	var got models.WalletAddress
	if err := db.Where("wallet_id = ? AND chain_key = ? AND user_id = ?", "0xB", "tron", uuidB).First(&got).Error; err != nil {
		t.Fatalf("reload B row: %v", err)
	}
	if got.Address != "ToldB" {
		t.Errorf("user B's address was overwritten: got %q, want %q", got.Address, "ToldB")
	}
	if got.UserID != uuidB {
		t.Errorf("user B's user_id was changed: got %v, want %v", got.UserID, uuidB)
	}

	// And user B can still register/update its OWN row without being blocked by
	// A's activity (no cross-user lockout).
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user", &models.User{ID: uuidB})
	body2, _ := json.Marshal(map[string]string{"walletId": "0xB", "chainKey": "tron", "address": "TnewB"})
	c2.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body2))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.RegisterNonEVMAddress(c2)
	if w2.Code != http.StatusOK {
		t.Errorf("user B blocked from updating its own row: got %d", w2.Code)
	}
	var b2 models.WalletAddress
	if err := db.Where("wallet_id = ? AND chain_key = ? AND user_id = ?", "0xB", "tron", uuidB).First(&b2).Error; err != nil {
		t.Fatalf("reload B row after self-update: %v", err)
	}
	if b2.Address != "TnewB" {
		t.Errorf("user B self-update did not apply: got %q, want %q", b2.Address, "TnewB")
	}
}
