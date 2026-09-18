package handlers

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// newSellQuoteHandler is the IT test handler with a live rate ($0.02/MCGP, 5%
// sell discount at ₦1500/$ ⇒ 2850 kobo per MCGP) and the live fee config.
func newSellQuoteHandler(t *testing.T, fx services.FxProvider) *ITHandler {
	t.Helper()
	h, db := newITTestHandler(t)
	it := services.NewITService(db, h.it.Swap(), fx)
	it.SetFeeQuoter(feeQuoteTestService())
	if _, err := it.SetRate(context.Background(), uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	return NewITHandler(db, it, nil)
}

func serveSellQuote(t *testing.T, h *ITHandler, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/sell/quote?"+query, nil)
	h.SellQuote(c)
	return w, decodeITResponse(t, w)
}

// TestSellQuote_ReceiveMode_NetCoversRequest: a naira amount is what lands in
// the bank; the MCGP is derived and the exact resulting net is returned.
func TestSellQuote_ReceiveMode_NetCoversRequest(t *testing.T) {
	h := newSellQuoteHandler(t, stubITFx{})
	w, resp := serveSellQuote(t, h, "receiveKobo=5000000")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	data := resp["data"].(map[string]any)
	net, _ := new(big.Int).SetString(data["netKobo"].(string), 10)
	if net == nil || net.Cmp(big.NewInt(5000000)) < 0 {
		t.Fatalf("netKobo = %v, want ≥ 5000000", data["netKobo"])
	}
	if data["mode"] != "receive" || data["mcgpAmount"] == "" || data["feeQuote"] == nil {
		t.Fatalf("unexpected payload: %v", data)
	}

	// The forward quote for the returned MCGP agrees to the kobo.
	w2, resp2 := serveSellQuote(t, h, "mcgpAmount="+data["mcgpAmount"].(string))
	if w2.Code != http.StatusOK {
		t.Fatalf("forward status = %d body=%s", w2.Code, w2.Body.String())
	}
	if got := resp2["data"].(map[string]any)["netKobo"]; got != data["netKobo"] {
		t.Fatalf("forward net %v != inverse net %v", got, data["netKobo"])
	}
}

func TestSellQuote_ParamValidation(t *testing.T) {
	h := newSellQuoteHandler(t, stubITFx{})
	for _, q := range []string{"", "mcgpAmount=1&receiveKobo=1", "mcgpAmount=abc", "receiveKobo=-5", "mcgpAmount=0"} {
		if w, _ := serveSellQuote(t, h, q); w.Code != http.StatusBadRequest {
			t.Errorf("%q → %d, want 400", q, w.Code)
		}
	}
	// Under the payout floor is the user's to fix: 400 with the floor message.
	w, resp := serveSellQuote(t, h, "receiveKobo=5000")
	if w.Code != http.StatusBadRequest || resp["code"] != nil {
		t.Fatalf("below floor → %d %v, want plain 400", w.Code, resp)
	}
}

// staleITFx serves a stale rate — the state that used to end on "Sale failed".
type staleITFx struct{}

func (staleITFx) NgnPerUsd(context.Context) (float64, bool, error) { return 1500, true, nil }

// TestSellQuote_PricingUnavailableIsRetryable: a stale rate is a 503 with the
// machine code the app turns into "Rates are refreshing" + Retry.
func TestSellQuote_PricingUnavailableIsRetryable(t *testing.T) {
	h := newSellQuoteHandler(t, staleITFx{})
	for _, q := range []string{"receiveKobo=5000000", "mcgpAmount=1000000000000000000000"} {
		w, resp := serveSellQuote(t, h, q)
		if w.Code != http.StatusServiceUnavailable || resp["code"] != itPricingUnavailableCode {
			t.Fatalf("%s → %d code=%v, want 503 %s", q, w.Code, resp["code"], itPricingUnavailableCode)
		}
	}
}

// TestSellFailureResponse_PricingUnavailable: the submit path maps the same
// transient errors to the same retryable code — never the dead-end 400.
func TestSellFailureResponse_PricingUnavailable(t *testing.T) {
	for _, err := range []error{
		services.ErrITFxUnavailable,
		services.ErrITNoRate,
		fmt.Errorf("wrapped: %w", services.ErrITFeeQuoteUnavailable),
	} {
		r := sellFailureResponse(err, nil)
		if r.status != http.StatusServiceUnavailable || r.code != itPricingUnavailableCode || r.accepted {
			t.Errorf("%v → %+v, want 503 %s", err, r, itPricingUnavailableCode)
		}
	}
}
