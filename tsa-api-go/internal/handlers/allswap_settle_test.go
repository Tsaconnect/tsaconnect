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
	db := setupWithdrawalTestDB(t)
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
	db := setupWithdrawalTestDB(t)
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
func TestAllSwap_LiFiUnreachableUsesConservativeDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupWithdrawalTestDB(t)
	h := newAllSwapHandler(t, db, 0, false) // LiFi unreachable
	user := mkWUser(t, db, "asdown", nil)

	// Client claims $1 (tiny). LiFi down → we ignore the client and charge the
	// conservative 3.5% of the face from-amount ($1000) = $35.
	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "down-1", "fromToken": "USDC", "toToken": "ETH",
		"fromAmount": "1000", "chainId": 1, "clientFeeUsd": 1.0,
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
