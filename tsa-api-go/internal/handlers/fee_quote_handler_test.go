package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// stubFx is a canned NGN/USD feed.
type stubFx struct {
	rate  float64
	stale bool
	err   error
}

func (s stubFx) NgnPerUsd(context.Context) (float64, bool, error) { return s.rate, s.stale, s.err }

func feeQuoteTestService() *services.FeeQuoteService {
	return services.NewFeeQuoteService(config.FeeQuoteConfig{
		PlatformStableUSD:    2,
		ProviderFallbackBps:  200,
		TaxBasis:             "fee",
		VATBps:               750,
		PlatformTaxBps:       50,
		McgpSwapBps:          200,
		LifiRouteBps:         300,
		StableDepositConvBps: 200,
	}, nil)
}

func serveFeeQuote(t *testing.T, h *FeeQuoteHandler, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var err error
	c.Request, err = http.NewRequest("GET", "/api/it/quote?"+query, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	h.GetQuote(c)
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v (%s)", err, w.Body.String())
	}
	return w, resp
}

func quoteData(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object: %v", resp)
	}
	quote, ok := data["quote"].(map[string]any)
	if !ok {
		t.Fatalf("no quote object: %v", data)
	}
	return quote
}

func lineMinor(t *testing.T, container map[string]any, key string) string {
	t.Helper()
	line, ok := container[key].(map[string]any)
	if !ok {
		t.Fatalf("no %s line: %v", key, container)
	}
	minor, ok := line["minor"].(string)
	if !ok {
		t.Fatalf("%s.minor is %T, want string", key, line["minor"])
	}
	return minor
}

// The PM-receipt numbers end-to-end through the HTTP layer: ₦500 MCGP deposit
// → provider ₦10, VAT ₦0.75. No FX needed (MCGP has no USD platform fee), so
// this works fully dormant (no Flutterwave, no FX feed).
func TestFeeQuoteHandler_MCGPDepositReceipt(t *testing.T) {
	h := NewFeeQuoteHandler(feeQuoteTestService(), nil)
	w, resp := serveFeeQuote(t, h, "flow=deposit&asset=MCGP&amount=500&currency=NGN")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if resp["success"] != true {
		t.Fatalf("success = %v", resp["success"])
	}
	q := quoteData(t, resp)
	provider := q["providerFee"].(map[string]any)
	if got := lineMinor(t, provider, "fee"); got != "1000" {
		t.Fatalf("provider fee minor = %s, want 1000", got)
	}
	tax := q["tax"].(map[string]any)
	if tax["basis"] != "fee" {
		t.Fatalf("tax basis = %v", tax["basis"])
	}
	if got := lineMinor(t, tax, "vat"); got != "75" {
		t.Fatalf("VAT minor = %s, want 75", got)
	}
	if got := lineMinor(t, q, "totalFees"); got != "1080" {
		t.Fatalf("totalFees minor = %s, want 1080", got)
	}
	if got := lineMinor(t, q, "netAmount"); got != "48920" {
		t.Fatalf("netAmount minor = %s, want 48920", got)
	}
	if _, hasFx := resp["data"].(map[string]any)["fx"]; hasFx {
		t.Fatal("fx meta must be omitted when no conversion happened")
	}
}

// Stablecoin flow in NGN pulls the FX feed to price the $2 platform fee and
// surfaces the rate + staleness it used.
func TestFeeQuoteHandler_StableDepositUsesFx(t *testing.T) {
	h := NewFeeQuoteHandler(feeQuoteTestService(), stubFx{rate: 1500, stale: true})
	w, resp := serveFeeQuote(t, h, "flow=deposit&asset=USDT&amount=1000000&currency=NGN")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	q := quoteData(t, resp)
	platform := q["platformFee"].(map[string]any)
	if got := lineMinor(t, platform, "inCurrency"); got != "300000" { // $2 at ₦1500
		t.Fatalf("platform fee minor = %s, want 300000", got)
	}
	fx, ok := resp["data"].(map[string]any)["fx"].(map[string]any)
	if !ok {
		t.Fatalf("fx meta missing: %v", resp["data"])
	}
	if fx["ngnPerUsd"] != float64(1500) || fx["stale"] != true {
		t.Fatalf("fx meta = %v", fx)
	}
}

// USD stablecoin quotes need no FX feed at all — a nil provider must not 503.
func TestFeeQuoteHandler_USDStableSkipsFx(t *testing.T) {
	h := NewFeeQuoteHandler(feeQuoteTestService(), nil)
	w, resp := serveFeeQuote(t, h, "flow=withdraw&asset=USDC&chain=polygon&amount=100&currency=USD")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	q := quoteData(t, resp)
	swap := q["swapFee"].(map[string]any)
	if swap["bps"] != float64(300) {
		t.Fatalf("swap bps = %v, want 300 (polygon LiFi-only leg)", swap["bps"])
	}
	platform := q["platformFee"].(map[string]any)
	if got := lineMinor(t, platform, "inCurrency"); got != "200" {
		t.Fatalf("platform fee minor = %s, want 200", got)
	}
}

func TestFeeQuoteHandler_FxDown503(t *testing.T) {
	for name, h := range map[string]*FeeQuoteHandler{
		"feed errors":  NewFeeQuoteHandler(feeQuoteTestService(), stubFx{err: errors.New("feed down")}),
		"no fx wired":  NewFeeQuoteHandler(feeQuoteTestService(), nil),
		"rate is zero": NewFeeQuoteHandler(feeQuoteTestService(), stubFx{rate: 0}),
	} {
		w, resp := serveFeeQuote(t, h, "flow=deposit&asset=USDC&amount=500&currency=NGN")
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d, want 503: %s", name, w.Code, w.Body.String())
		}
		if resp["success"] != false {
			t.Fatalf("%s: success = %v", name, resp["success"])
		}
	}
}

func TestFeeQuoteHandler_BadInput400(t *testing.T) {
	h := NewFeeQuoteHandler(feeQuoteTestService(), stubFx{rate: 1500})
	for name, query := range map[string]string{
		"bad flow":                  "flow=teleport&asset=MCGP&amount=500",
		"bad asset":                 "flow=deposit&asset=DOGE&amount=500",
		"missing amount":            "flow=deposit&asset=MCGP",
		"zero amount":               "flow=deposit&asset=MCGP&amount=0",
		"bad currency":              "flow=deposit&asset=MCGP&amount=500&currency=GBP",
		"stable withdraw w/o chain": "flow=withdraw&asset=USDT&amount=100&currency=USD",
	} {
		w, resp := serveFeeQuote(t, h, query)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400: %s", name, w.Code, w.Body.String())
		}
		if resp["success"] != false {
			t.Fatalf("%s: success = %v", name, resp["success"])
		}
	}
}

// currency defaults to NGN when omitted — the app's fiat rail.
func TestFeeQuoteHandler_DefaultCurrencyNGN(t *testing.T) {
	h := NewFeeQuoteHandler(feeQuoteTestService(), nil)
	w, resp := serveFeeQuote(t, h, "flow=deposit&asset=MCGP&amount=500")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if q := quoteData(t, resp); q["currency"] != "NGN" {
		t.Fatalf("currency = %v, want NGN", q["currency"])
	}
}
