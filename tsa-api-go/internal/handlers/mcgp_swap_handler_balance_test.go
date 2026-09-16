// Tests for GET /api/mcgp/balance's response shape.
//
// The endpoint used to return only the merged premiumBalance (bought +
// credited), which let the app price and offer for USDC sale MCGP a naira
// buyer can never sell that way — the contract's sellPremium requires
// premiumBought >= amount and reverts "Insufficient bought premium"
// otherwise. This pins the additive premiumBought/premiumCredited fields the
// app now needs to tell the two buckets apart before ever calling prepare.

package handlers

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// newBalanceTestHandler builds an MCGPSwapHandler over the fakeSwapPort
// double (already used by the relay tests) with no relayer and no DB access
// — GetBalance never touches h.db, only h.swap and the request context.
func newBalanceTestHandler(swap *fakeSwapPort) *MCGPSwapHandler {
	return &MCGPSwapHandler{swap: swap}
}

func doGetBalance(h *MCGPSwapHandler, user *models.User) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/mcgp/balance", nil)
	c.Set("user", user)
	h.GetBalance(c)
	return w
}

// TestMCGPSwapHandler_GetBalance_ReturnsBoughtAndCreditedBuckets is the core
// shape guarantee: a wallet with both buckets populated gets both back,
// alongside the pre-existing merged premiumBalance (untouched, for older app
// builds that only read that field).
func TestMCGPSwapHandler_GetBalance_ReturnsBoughtAndCreditedBuckets(t *testing.T) {
	swap := &fakeSwapPort{
		premiumBought:   big.NewInt(3_000000000000000000), // 3 MCGP, USDC-bought
		premiumCredited: big.NewInt(7_000000000000000000), // 7 MCGP, naira-bought
	}
	user := &models.User{ID: uuid.New(), WalletAddress: "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"}

	w := doGetBalance(newBalanceTestHandler(swap), user)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	resp := decodeITResponse(t, w)
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data object: %v", resp)
	}
	if got := data["premiumBought"]; got != "3000000000000000000" {
		t.Fatalf("premiumBought = %v, want 3000000000000000000", got)
	}
	if got := data["premiumCredited"]; got != "7000000000000000000" {
		t.Fatalf("premiumCredited = %v, want 7000000000000000000", got)
	}
}

// TestMCGPSwapHandler_GetBalance_ZeroBoughtIsExplicitZero pins the
// naira-only wallet case: premiumBought must read back as "0", not be
// omitted, so the app can safely gate "sellable for USDC" on it without a
// presence check.
func TestMCGPSwapHandler_GetBalance_ZeroBoughtIsExplicitZero(t *testing.T) {
	swap := &fakeSwapPort{} // zero-value: nothing bought, nothing credited
	user := &models.User{ID: uuid.New(), WalletAddress: "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"}

	w := doGetBalance(newBalanceTestHandler(swap), user)

	resp := decodeITResponse(t, w)
	data := resp["data"].(map[string]any)
	if got := data["premiumBought"]; got != "0" {
		t.Fatalf("premiumBought = %v, want \"0\"", got)
	}
	if got := data["premiumCredited"]; got != "0" {
		t.Fatalf("premiumCredited = %v, want \"0\"", got)
	}
}
