package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// newAllSwapHandler builds a SwapLiFiHandler wired with config + the fee engine
// and an INJECTED requeryQuoteUSD so tests control LiFi reachability + the
// re-queried from-amount USD without HTTP.
func newAllSwapHandler(t *testing.T, db *gorm.DB, fromAmountUSD float64, reachable bool) *SwapLiFiHandler {
	t.Helper()
	cfg := &config.Config{
		TPPerUSD:                  1.0,
		AllSwapFeeDeviationMax:    0.10,
		AllSwapNominalFeeBps:      300, // 3%
		AllSwapConservativeFeeBps: 350, // 3.5%
	}
	h := NewSwapLiFiHandler("", "", "")
	h.Config = cfg
	h.FeeService = services.NewFeeDistributionService(db, services.NewReferralGraphService(db), 1.0)
	h.requeryQuoteUSD = func(p settleQuoteParams) (float64, bool) {
		return fromAmountUSD, reachable
	}
	return h
}

func doSettle(t *testing.T, h *SwapLiFiHandler, user models.User, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/swap/lifi/settle", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &user)
	h.SettleAllSwap(c)
	return w
}

// (a) accrues the VERIFIED fee (3% of the re-queried fromAmountUSD), idempotent.
func TestAllSwap_AccruesVerifiedFee(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true) // LiFi reachable, $1000 from-amount
	user := mkWUser(t, db, "asuser", nil)

	quoteID := "quote-abc-123"
	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": quoteID, "fromToken": "USDC", "toToken": "ETH",
		"fromAmount": "1000", "chainId": 1, "clientFeeUsd": 30.0, // client claims $30 (= verified 3%)
	})
	if w.Code != http.StatusOK {
		t.Fatalf("settle status=%d body=%s", w.Code, w.Body.String())
	}

	sourceID := deterministicUUID("allswap:" + quoteID)
	if got := waitForGuardHandler(t, db, "allswap", sourceID, 1); got != 1 {
		t.Fatalf("want 1 allswap guard row, got %d", got)
	}
	var fd models.FeeDistribution
	if err := db.Where("source_type = ? AND source_id = ?", "allswap", sourceID).First(&fd).Error; err != nil {
		t.Fatalf("load guard: %v", err)
	}
	if fd.Feature != models.FeatureAllswap {
		t.Errorf("feature=%q want allswap", fd.Feature)
	}
	// Verified fee = 3% of $1000 = $30 (NOT the client value — they happen to match here).
	if fd.FeeUSD < 29.999 || fd.FeeUSD > 30.001 {
		t.Errorf("verified feeUSD=%v want 30", fd.FeeUSD)
	}

	// Idempotent: a repeat settle for the SAME quoteId is a guard no-op.
	w2 := doSettle(t, h, user, map[string]interface{}{
		"quoteId": quoteID, "fromToken": "USDC", "toToken": "ETH", "fromAmount": "1000", "chainId": 1,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("repeat settle status=%d", w2.Code)
	}
	// Still exactly one guard row.
	if got := feeGuardCount(db, "allswap", sourceID); got != 1 {
		t.Fatalf("idempotency broken: %d guard rows", got)
	}
}

// (b) rejects (422) a FORGED client fee that deviates >10% from the verified fee.
func TestAllSwap_RejectsForgedClientFee(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true) // verified fee = $30
	user := mkWUser(t, db, "asforge", nil)

	// Client claims it only paid $5 — a 83% under-statement, far past the 10% gate.
	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "forge-1", "fromToken": "USDC", "toToken": "ETH",
		"fromAmount": "1000", "chainId": 1, "clientFeeUsd": 5.0,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for forged fee, got %d body=%s", w.Code, w.Body.String())
	}
	// Rejected → no accrual.
	sourceID := deterministicUUID("allswap:forge-1")
	if got := feeGuardCount(db, "allswap", sourceID); got != 0 {
		t.Fatalf("rejected settle must not accrue; got %d guard rows", got)
	}
}

// (c) LiFi unreachable → conservative default (3.5%), NOT 0 and NOT the client
// value; still accrues the verified (conservative) fee.
//
// The basis used to be `fromAmount` read as dollars. It is base units, so that
// is only ever right by accident — see TestAllSwap_UnreachableLiFiPricesTheBasis.
// The conservative charge now comes off a priced basis; what this test still
// pins is that the client's own number is ignored either way.
func TestAllSwap_LiFiUnreachableUsesConservativeDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 0, false) // LiFi unreachable
	h.PriceService = services.NewPriceService()
	h.PriceService.SeedPriceForTest("USDC", services.PriceData{USD: 1.0})
	user := mkWUser(t, db, "asdown", nil)

	// Client claims $1 (tiny). LiFi down → we ignore the client and charge the
	// conservative 3.5% of the priced from-amount (1,000 USDC = $1000) = $35.
	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "down-1", "fromToken": "USDC", "toToken": "ETH",
		"fromAmount": "1000000000", "fromSymbol": "USDC", "fromDecimals": 6,
		"chainId": 1, "clientFeeUsd": 1.0,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("settle status=%d body=%s", w.Code, w.Body.String())
	}
	sourceID := deterministicUUID("allswap:down-1")
	if got := waitForGuardHandler(t, db, "allswap", sourceID, 1); got != 1 {
		t.Fatalf("want 1 conservative guard row, got %d", got)
	}
	var fd models.FeeDistribution
	db.Where("source_type = ? AND source_id = ?", "allswap", sourceID).First(&fd)
	// 3.5% of $1000 = $35 — never 0, never the client's $1.
	if fd.FeeUSD < 34.999 || fd.FeeUSD > 35.001 {
		t.Errorf("conservative feeUSD=%v want 35 (3.5%% of 1000)", fd.FeeUSD)
	}
}

// (d) Solana routes settle with LiFi's oversized SVM chain id (1151111081099710
// ≈ 2^50) — it must survive the int64 JSON field and reach the re-query as the
// exact decimal string, and the verified fee must accrue like any EVM route.
func TestAllSwap_SolanaChainIDSettles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true)
	user := mkWUser(t, db, "assol", nil)

	const svmChainID int64 = 1151111081099710
	var gotFromChain, gotToChain string
	h.requeryQuoteUSD = func(p settleQuoteParams) (float64, bool) {
		gotFromChain, gotToChain = p.FromChain, p.ToChain
		return 1000.0, true
	}

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "sol-1", "fromToken": "SOL", "toToken": "USDC",
		"fromAmount": "1000000000", "chainId": svmChainID, "toChainId": 42161,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("settle status=%d body=%s", w.Code, w.Body.String())
	}
	if gotFromChain != "1151111081099710" || gotToChain != "42161" {
		t.Errorf("re-query chain ids: from=%q to=%q", gotFromChain, gotToChain)
	}
	sourceID := deterministicUUID("allswap:sol-1")
	if got := waitForGuardHandler(t, db, "allswap", sourceID, 1); got != 1 {
		t.Fatalf("want 1 allswap guard row, got %d", got)
	}
}

// FromAmount is in base units. Reading it as dollars — which the conservative
// fallback used to do — turns a 1,000 USDC swap (fromAmount "1000000000") into
// a $35,000,000 fee event, and every split, TP award and reward computed from
// it inherits that. With a price available the basis is priced properly.
func TestAllSwap_UnreachableLiFiPricesTheBasis(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 0, false) // LiFi unreachable
	h.PriceService = services.NewPriceService()
	h.PriceService.SeedPriceForTest("USDC", services.PriceData{USD: 1.0})
	user := mkWUser(t, db, "asprice", nil)

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "priced-1", "fromToken": "0xaf88d065e77c8cC2239327C5EDb3A432268e5831",
		"toToken": "ETH", "fromAmount": "1000000000", "chainId": 42161,
		"fromSymbol": "USDC", "fromDecimals": 6,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// 1,000 USDC at $1 → conservative 3.5% → $35. NOT $35,000,000.
	if got := settledFee(t, db, "priced-1").FeeUSD; got < 34.999 || got > 35.001 {
		t.Fatalf("feeUSD=%v, want 35 (3.5%% of a $1,000 basis)", got)
	}
}

// With neither LiFi nor a price, there is no honest basis. Accruing a guess
// would corrupt the ledger, so nothing is accrued and the caller is told.
func TestAllSwap_UnpricedFeeIsDeferredNotInvented(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 0, false)
	h.PriceService = services.NewPriceService() // no seeded price for WOOF
	user := mkWUser(t, db, "asunpriced", nil)

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "unpriced-1", "fromToken": "0xdead", "toToken": "ETH",
		"fromAmount": "2000000000000000000000000", "chainId": 1,
		"fromSymbol": "WOOF", "fromDecimals": 18,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"deferred":true`) {
		t.Errorf("response should say the fee was deferred: %s", w.Body.String())
	}
	sourceID := deterministicUUID("allswap:unpriced-1")
	if got := feeGuardCount(db, "allswap", sourceID); got != 0 {
		t.Fatalf("an unpriced settle must accrue nothing, got %d rows", got)
	}
}

// LiFi refuses a quote without fromAddress, so the re-query has to carry the
// route's endpoints or it fails 400 every time — silently sending every settle
// down the unreachable path.
func TestAllSwap_RequeryCarriesRouteAddresses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true)
	user := mkWUser(t, db, "asaddr", nil)

	var got settleQuoteParams
	h.requeryQuoteUSD = func(p settleQuoteParams) (float64, bool) {
		got = p
		return 1000.0, true
	}
	doSettle(t, h, user, map[string]interface{}{
		"quoteId": "addr-1", "fromToken": "bitcoin", "toToken": "USDC",
		"fromAmount": "200000", "chainId": 20000000000001, "toChainId": 42161,
		"fromAddress": "bc1qsender", "toAddress": "0xrecipient",
	})
	if got.FromAddress != "bc1qsender" || got.ToAddress != "0xrecipient" {
		t.Fatalf("re-query lost the route addresses: %+v", got)
	}
}

// settledFee loads the guard row a settle call produced.
func settledFee(t *testing.T, db *gorm.DB, quoteID string) models.FeeDistribution {
	t.Helper()
	sourceID := deterministicUUID("allswap:" + quoteID)
	if got := waitForGuardHandler(t, db, "allswap", sourceID, 1); got != 1 {
		t.Fatalf("want 1 guard row for %s, got %d", quoteID, got)
	}
	var fd models.FeeDistribution
	if err := db.Where("source_type = ? AND source_id = ?", "allswap", sourceID).First(&fd).Error; err != nil {
		t.Fatalf("load guard: %v", err)
	}
	return fd
}

// The same-chain trading desk and the cross-chain bridge settle through this
// one endpoint but are reported as separate lines, so the caller names its
// bucket. Anything unrecognised books as allswap rather than inventing a
// feature the fee config has no rate for.
func TestAllSwap_BooksTheRequestedFeature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true)
	user := mkWUser(t, db, "featureuser", nil)

	for _, tc := range []struct{ requested, want string }{
		{"spot", models.FeatureSpot},
		{"allswap", models.FeatureAllswap},
		{"", models.FeatureAllswap},
		{"product_merchant", models.FeatureAllswap}, // not this endpoint's to write
	} {
		quoteID := "feature-" + tc.requested
		w := doSettle(t, h, user, map[string]interface{}{
			"quoteId": quoteID, "fromToken": "USDC", "toToken": "ETH",
			"fromAmount": "1000", "chainId": 1, "feature": tc.requested,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("%q: status=%d body=%s", tc.requested, w.Code, w.Body.String())
		}
		if got := settledFee(t, db, quoteID).Feature; got != tc.want {
			t.Errorf("feature %q booked as %q, want %q", tc.requested, got, tc.want)
		}
	}
}

// The fee event's token is a settlement SYMBOL — SP5 resolves payouts from it,
// and the column holds 16 characters. The route is named by contract address,
// so an address here would both mean nothing downstream and overflow the
// column.
func TestAllSwap_LabelsFeeWithSymbolNotAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true)
	user := mkWUser(t, db, "symboluser", nil)

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId":    "symbol-1",
		"fromToken":  "0xaf88d065e77c8cC2239327C5EDb3A432268e5831", // USDC on Arbitrum
		"toToken":    "0x0000000000000000000000000000000000000000",
		"fromAmount": "1000000000", "chainId": 42161, "fromSymbol": "usdc",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := settledFee(t, db, "symbol-1").Token; got != "USDC" {
		t.Errorf("fee token=%q, want the uppercased symbol USDC", got)
	}
}

// waitForGuardHandler polls for the async EnqueueFeeSettled goroutine to land a
// guard row (releasing the single SQLite connection between polls).
func waitForGuardHandler(t *testing.T, db *gorm.DB, sourceType string, sourceID uuid.UUID, want int64) int64 {
	t.Helper()
	for i := 0; i < 150; i++ {
		if n := feeGuardCount(db, sourceType, sourceID); n >= want {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
	return feeGuardCount(db, sourceType, sourceID)
}
