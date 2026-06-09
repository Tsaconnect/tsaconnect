package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newTestLiFiHandler(lifiURL string) *SwapLiFiHandler {
	return &SwapLiFiHandler{
		tokenCache:  make(map[string]tokenCacheEntry),
		lifiBaseURL: lifiURL,
	}
}

// newTestLiFiHandlerWithIntegrator returns a handler pre-configured with
// the "tsaconnect"/"0.03" fee/integrator pair used by the legacy tests
// that asserted upstream injection behavior.
func newTestLiFiHandlerWithIntegrator(lifiURL string) *SwapLiFiHandler {
	h := newTestLiFiHandler(lifiURL)
	h.integratorID = "tsaconnect"
	h.integratorFee = "0.03"
	return h
}

func setupTestRouter(h *SwapLiFiHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/swap/lifi/chains", h.GetChains)
	r.GET("/swap/lifi/tokens", h.GetTokens)
	r.GET("/swap/lifi/token", h.GetToken)
	r.GET("/swap/lifi/quote", h.GetQuote)
	return r
}

func TestGetChains_ReturnsLiFiResponse(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chains" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("chainTypes") != "EVM" {
			t.Errorf("expected chainTypes=EVM, got %s", r.URL.Query().Get("chainTypes"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"chains":[{"id":1,"name":"Ethereum","key":"ETH","coin":"ETH"}]}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/chains", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if body["chains"] == nil {
		t.Fatal("expected chains in response")
	}
}

func TestGetChains_Cache_SecondCallSkipsLiFi(t *testing.T) {
	callCount := 0
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"chains":[]}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/swap/lifi/chains", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i+1, w.Code)
		}
	}
	if callCount != 1 {
		t.Fatalf("expected 1 LiFi call (cached), got %d", callCount)
	}
}

func TestGetChains_LiFiUnreachable_Returns502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	h := newTestLiFiHandler(url)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/chains", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
}

func TestGetToken_ForwardsChainAndAddress(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("chain") != "56" || r.URL.Query().Get("token") != "0xabc" {
			t.Errorf("unexpected params: %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"address":"0xabc","symbol":"TEST","decimals":18,"name":"Test Token"}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/token?chain=56&token=0xabc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetToken_MissingParams_Returns400(t *testing.T) {
	h := newTestLiFiHandler("http://unused")
	r := setupTestRouter(h)

	for _, url := range []string{"/swap/lifi/token", "/swap/lifi/token?chain=56", "/swap/lifi/token?token=0xabc"} {
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %s, got %d", url, w.Code)
		}
	}
}

func TestGetTokens_ReturnsLiFiResponse(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("chains") != "56" {
			t.Errorf("expected chains=56, got %s", r.URL.Query().Get("chains"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{"56":[{"address":"0xabc","symbol":"USDT","decimals":6}]}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/tokens?chainId=56", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if body["tokens"] == nil {
		t.Fatal("expected tokens in response")
	}
}

func TestGetTokens_Cache_SecondCallSkipsLiFi(t *testing.T) {
	callCount := 0
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/swap/lifi/tokens?chainId=1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i+1, w.Code)
		}
	}
	if callCount != 1 {
		t.Fatalf("expected 1 LiFi call (cached), got %d", callCount)
	}
}

func TestGetTokens_Cache_RefetchesAfter24Hours(t *testing.T) {
	callCount := 0
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	h.tokenCache["137"] = tokenCacheEntry{
		data:      []byte(`{"tokens":{}}`),
		fetchedAt: time.Now().Add(-25 * time.Hour),
	}

	r := setupTestRouter(h)
	req := httptest.NewRequest("GET", "/swap/lifi/tokens?chainId=137", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 LiFi re-fetch after stale cache, got %d", callCount)
	}
}

func TestGetQuote_ForwardsParamsToLiFi(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/quote" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("fromChain") != "56" || q.Get("toChain") != "1" {
			t.Errorf("params not forwarded correctly: %v", q)
		}
		if q.Get("slippage") != "0.005" {
			t.Errorf("expected default slippage 0.005, got %s", q.Get("slippage"))
		}
		if q.Get("fee") != "0.03" {
			t.Errorf("expected fee=0.03, got %s", q.Get("fee"))
		}
		if q.Get("integrator") != "tsaconnect" {
			t.Errorf("expected integrator=tsaconnect, got %s", q.Get("integrator"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"to":"0x1","data":"0x","value":"0x0","chainId":56,"gasLimit":"200000"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandlerWithIntegrator(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain=56&toChain=1&fromToken=0xabc&toToken=0xdef&fromAmount=1000000&fromAddress=0x123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetQuote_PreservesCallerSlippage(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("slippage") != "0.01" {
			t.Errorf("expected caller slippage 0.01, got %s", r.URL.Query().Get("slippage"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"to":"0x1","data":"0x","value":"0x0","chainId":56,"gasLimit":"200000"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain=56&toChain=1&fromToken=0xabc&toToken=0xdef&fromAmount=1000000&fromAddress=0x123&slippage=0.01", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetTokens_MissingChainId_Returns400(t *testing.T) {
	h := newTestLiFiHandler("http://unused")
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/tokens", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetTokens_InvalidChainId_Returns400(t *testing.T) {
	h := newTestLiFiHandler("http://unused")
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/tokens?chainId=notanumber", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetTokens_LiFiUnreachable_Returns502(t *testing.T) {
	// Use a server that immediately closes the connection
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close before the request

	h := newTestLiFiHandler(url)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/tokens?chainId=56", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
}

func TestGetQuote_InjectsFeeAndIntegratorWhenConfigured(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("fee") != "0.03" {
			t.Errorf("expected fee=0.03, got %q", q.Get("fee"))
		}
		if q.Get("integrator") != "tsaconnect" {
			t.Errorf("expected integrator=tsaconnect, got %q", q.Get("integrator"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"to":"0x1","data":"0x","value":"0x0","chainId":56,"gasLimit":"200000"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandlerWithIntegrator(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain=56&toChain=1&fromToken=0xabc&toToken=0xdef&fromAmount=1000000&fromAddress=0x123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetQuote_OverridesClientFeeAndIntegrator(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("fee") != "0.03" {
			t.Errorf("expected fee=0.03, got %q", q.Get("fee"))
		}
		if q.Get("integrator") != "tsaconnect" {
			t.Errorf("expected integrator=tsaconnect, got %q", q.Get("integrator"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"to":"0x1","data":"0x","value":"0x0","chainId":56,"gasLimit":"200000"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandlerWithIntegrator(mockLiFi.URL)
	r := setupTestRouter(h)

	// Client tries to override fee and integrator — server must win
	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain=56&toChain=1&fromToken=0xabc&toToken=0xdef&fromAmount=1000000&fromAddress=0x123&fee=0&integrator=hacker", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// When integrator/fee are NOT configured (the default until portal.li.fi
// is set up), the handler must NOT leak either param upstream — even when
// a malicious caller tries to inject them via the query string. Sending
// fee+integrator against an unregistered integrator name makes LiFi
// return a 400.
func TestGetQuote_NoIntegratorConfigured_StripsFeeAndIntegrator(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Has("fee") {
			t.Errorf("expected fee absent, got %q", q.Get("fee"))
		}
		if q.Has("integrator") {
			t.Errorf("expected integrator absent, got %q", q.Get("integrator"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"to":"0x1","data":"0x","value":"0x0","chainId":56,"gasLimit":"200000"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL) // no integrator config
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain=56&toChain=1&fromToken=0xabc&toToken=0xdef&fromAmount=1000000&fromAddress=0x123&fee=0.03&integrator=hacker", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// When an API key is configured, the handler must forward it as
// `x-lifi-api-key`. Without it, the header should be absent so the
// upstream call lands in the anonymous tier.
func TestLiFiHandler_ForwardsAPIKeyHeader(t *testing.T) {
	const wantKey = "test-key-abc"
	var sawKey string
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("x-lifi-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"chains":[]}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	h.apiKey = wantKey
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/chains", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if sawKey != wantKey {
		t.Errorf("expected x-lifi-api-key=%q, got %q", wantKey, sawKey)
	}
}

// A LiFi 200 with a malformed/empty/zero fromAmountUSD must NOT yield (usd>0, true):
// returning reachable=true with usd=0 would silently accrue a $0 verified fee on the
// "reachable" path. The fix forces reachable=false so the settle path applies the
// conservative configured default instead (design §8, I8 — never a silent $0).
func TestLiveRequeryQuoteUSD_Malformed200_NotReachable(t *testing.T) {
	cases := map[string]string{
		"empty fromAmountUSD": `{"estimate":{"fromAmountUSD":""}}`,
		"zero fromAmountUSD":  `{"estimate":{"fromAmountUSD":"0"}}`,
		"missing estimate":    `{}`,
		"non-numeric":         `{"estimate":{"fromAmountUSD":"abc"}}`,
		"empty object":        `{"estimate":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK) // a 200 — but malformed payload
				w.Write([]byte(body))
			}))
			defer mockLiFi.Close()

			h := newTestLiFiHandler(mockLiFi.URL)
			usd, reachable := h.liveRequeryQuoteUSD(settleQuoteParams{
				FromChain: "56", ToChain: "56", FromToken: "0xabc", ToToken: "0xdef", FromAmount: "1000000",
			})
			if reachable {
				t.Fatalf("malformed 200 must yield reachable=false, got reachable=true usd=%v", usd)
			}
			if usd != 0 {
				t.Fatalf("malformed 200 must yield usd=0, got %v", usd)
			}
		})
	}
}

// A well-formed 200 with a positive fromAmountUSD stays reachable=true (the fix
// only flips the zero/malformed case, not the happy path).
func TestLiveRequeryQuoteUSD_Valid200_Reachable(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"estimate":{"fromAmountUSD":"123.45"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	usd, reachable := h.liveRequeryQuoteUSD(settleQuoteParams{
		FromChain: "56", ToChain: "56", FromToken: "0xabc", ToToken: "0xdef", FromAmount: "1000000",
	})
	if !reachable || usd != 123.45 {
		t.Fatalf("valid 200 want (123.45,true), got (%v,%v)", usd, reachable)
	}
}

func TestGetQuote_LiFiUnreachable_Returns502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	h := newTestLiFiHandler(url)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain=56&toChain=1&fromToken=0xabc&toToken=0xdef&fromAmount=1000000&fromAddress=0x123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
}
