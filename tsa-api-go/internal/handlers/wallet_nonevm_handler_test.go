package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/chains/tron"
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

// A discovered Sui coin names itself from metadata its creator wrote; one called
// "USDC" must not be shown as worth real USDC.
func TestEnrichBalancesPricesOnlyNativeAndListedRows(t *testing.T) {
	ps := services.NewPriceService()
	ps.SeedPriceForTest("USDC", services.PriceData{USD: 1})

	out := enrichBalancesWithUSD(ps, []chains.Balance{
		{Symbol: "USDC", Raw: "5000000", Decimals: 6, TokenAddress: "0xreal::usdc::USDC", Listed: true},
		{Symbol: "USDC", Raw: "500000000", Decimals: 6, TokenAddress: "0xfake::usdc::USDC"},
	})
	if out[0].UsdValue != 5 {
		t.Fatalf("listed USDC: want $5, got %+v", out[0])
	}
	if out[1].UsdPrice != 0 || out[1].UsdValue != 0 {
		t.Fatalf("discovered USDC must be unpriced, got %+v", out[1])
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

// stubFeeFailure fails the fee estimate with a supplied error, standing in for
// a chain client that rejects the request (bad bridge memo) or cannot reach its
// indexer.
type stubFeeFailure struct {
	chains.ChainClient
	err error
}

func (stubFeeFailure) Family() string               { return "btc" }
func (stubFeeFailure) ValidateAddress(string) error { return nil }
func (s stubFeeFailure) EstimateFee(context.Context, chains.TransferRequest) (chains.FeeQuote, error) {
	return chains.FeeQuote{}, s.err
}

func prepareNonEVMRequest(t *testing.T, h *Handlers, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/?chain=btc&network=mainnet", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PrepareNonEVMTx(c)
	return w
}

// PrepareNonEVMTx runs EstimateFee before PrepareTransfer, so a caller-fixable
// reason has to survive that branch. It used to collapse into "fee estimate
// failed", leaving a user with a stuck bridge deposit and no idea why.
func TestPrepareNonEVMTx_SurfacesInvalidRequestReason(t *testing.T) {
	reason := "memo is 81 bytes; the OP_RETURN standardness limit is 80"
	reg := chains.NewRegistry()
	reg.Register("mainnet", "btc", stubFeeFailure{err: chains.Invalid("%s", reason)})

	w := prepareNonEVMRequest(t, &Handlers{ChainRegistry: reg}, map[string]string{
		"from": "bc1qsender", "to": "bc1qdeposit", "amount": "125000",
		"symbol": "BTC", "memoHex": "ab",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a caller-fixable request, got %d (%s)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(reason)) {
		t.Fatalf("the actionable reason must reach the client, got %s", w.Body.String())
	}
}

// An upstream failure stays generic — the client learns the call failed, not
// the shape of our infrastructure.
func TestPrepareNonEVMTx_KeepsUpstreamFailuresGeneric(t *testing.T) {
	reg := chains.NewRegistry()
	reg.Register("mainnet", "btc", stubFeeFailure{err: errors.New("dial tcp 10.0.0.1:443: connect: refused")})

	w := prepareNonEVMRequest(t, &Handlers{ChainRegistry: reg}, map[string]string{
		"from": "bc1qsender", "to": "bc1qdeposit", "amount": "125000", "symbol": "BTC",
	})

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502 for an upstream failure, got %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("10.0.0.1")) {
		t.Fatalf("upstream detail leaked to the client: %s", w.Body.String())
	}
}

// The submit-tx fallback reads `amount` as if it were dollars. That is only
// sound for a stablecoin: an aggregator swap names its amount in the from-token
// (0.5 WETH would accrue $0.005, 2M SHIB would accrue $20,000), so those defer
// their fee to the server-verified settle instead of being guessed at here.
func TestShouldAccrueSubmitFee(t *testing.T) {
	cases := []struct {
		name string
		req  submitTxRequest
		want bool
	}{
		{"swap accrues", submitTxRequest{TxType: models.TxTypeSwap}, true},
		// A send charges no platform fee, so there is nothing to settle (it used to
		// record a phantom $0.01 that was never collected).
		{"send does not", submitTxRequest{TxType: models.TxTypeSend}, false},
		{"send with a claimed fee does not", submitTxRequest{TxType: models.TxTypeSend, FeeUSD: 5}, false},
		{"deferred swap does not", submitTxRequest{TxType: models.TxTypeSwap, FeeDeferred: true}, false},
		{"deferred send does not", submitTxRequest{TxType: models.TxTypeSend, FeeDeferred: true}, false},
		{"approve never did", submitTxRequest{TxType: "approve"}, false},
		// A deferred tx must not slip through on a client-supplied fee either —
		// the whole point is that the server figures the number out itself.
		{"deferred with a claimed fee still does not", submitTxRequest{TxType: models.TxTypeSwap, FeeDeferred: true, FeeUSD: 12.5}, false},
	}
	for _, tc := range cases {
		if got := shouldAccrueSubmitFee(tc.req); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A Tron approval the wallet already holds has nothing to sign. Prepare must
// still answer 200 with `prepared.payloads: []` — the app reads the empty list
// as "already approved" and goes straight to the swap without calling submit.
func TestPrepareNonEVMTx_TronApproveAlreadyApprovedReturnsEmptyPayloads(t *testing.T) {
	const (
		owner   = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
		spender = "TU3ymitEKCWQFtASkEeHaPb8NfZcJtCHLt"
		usdt    = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/" + owner:
			w.Write([]byte(`{"data":[{"balance":50000000,"trc20":[]}]}`))
		case "/wallet/triggerconstantcontract":
			w.Write([]byte(`{"result":{"result":true},"constant_result":["ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"]}`))
		default:
			t.Errorf("unexpected TronGrid call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	reg := chains.NewRegistry()
	reg.Register("mainnet", "tron", tron.NewClient(config.NonEVMChainConfig{ChainKey: "tron", Family: "tron", Routers: []string{spender}}, tron.NewTronGrid(srv.URL, "")))

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, _ := json.Marshal(map[string]any{
		"from": owner, "to": spender, "amount": "50000000", "symbol": "USDT",
		"tokenAddr": usdt, "approve": true,
	})
	c.Request, _ = http.NewRequest("POST", "/?chain=tron&network=mainnet", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	(&Handlers{ChainRegistry: reg}).PrepareNonEVMTx(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Prepared struct {
				Family   string             `json:"family"`
				Payloads *[]json.RawMessage `json:"payloads"`
			} `json:"prepared"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Prepared.Family != "tron" || resp.Data.Prepared.Payloads == nil || len(*resp.Data.Prepared.Payloads) != 0 {
		t.Fatalf("want family tron and payloads [], got %s", w.Body.String())
	}
}
