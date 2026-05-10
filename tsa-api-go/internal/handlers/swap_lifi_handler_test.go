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

func setupTestRouter(h *SwapLiFiHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/swap/lifi/tokens", h.GetTokens)
	r.GET("/swap/lifi/quote", h.GetQuote)
	return r
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"to":"0x1","data":"0x","value":"0x0","chainId":56,"gasLimit":"200000"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
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
