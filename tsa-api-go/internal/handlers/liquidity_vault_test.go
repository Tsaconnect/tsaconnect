package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The LP side's new refusals reach the app as their own status and code.
func TestVaultLPError_NewRefusals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("wrapped: %w", services.ErrLPOldWalletHasFunds), http.StatusConflict, "liquidity_old_wallet_has_funds"},
		{services.ErrLPClaimTo, http.StatusBadRequest, ""},
		{services.ErrLPNothingToClaim, http.StatusBadRequest, ""},
		{fmt.Errorf("wrapped: %w", services.ErrLPClaimUnavailable), http.StatusConflict, "claim_unavailable"},
		{fmt.Errorf("%w: more than your deposited principal", services.ErrLPAmount), http.StatusBadRequest, ""},
		{fmt.Errorf("%w: you can add at most 20 USDT", services.ErrLPPoolFull), http.StatusConflict, "pool_full"},
		{services.ErrLPLimitsUnavailable, http.StatusServiceUnavailable, "deposits_unavailable"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		if !vaultLPError(c, tc.err) {
			t.Fatalf("%v: not mapped (would be a 500)", tc.err)
		}
		var body struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != tc.status || body.Code != tc.code {
			t.Fatalf("%v: status %d code %q, want %d %q", tc.err, w.Code, body.Code, tc.status, tc.code)
		}
		if tc.code == "claim_unavailable" && body.Message != "Claims are not available right now. Try again later." {
			t.Fatalf("claim_unavailable message = %q, want the neutral copy", body.Message)
		}
	}
}
