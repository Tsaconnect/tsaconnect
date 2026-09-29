package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		if r.URL.Query().Get("chainTypes") != lifiChainTypes {
			t.Errorf("expected chainTypes=%s, got %s", lifiChainTypes, r.URL.Query().Get("chainTypes"))
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

// Cross-VM routes (Solana↔EVM) carry the destination-chain address in
// toAddress and use LiFi's non-numeric SVM chain id — both must pass through
// the proxy untouched.
func TestGetQuote_ForwardsSolanaChainAndToAddress(t *testing.T) {
	const svmChainID = "1151111081099710"
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("fromChain") != svmChainID {
			t.Errorf("expected fromChain=%s, got %s", svmChainID, q.Get("fromChain"))
		}
		if q.Get("toAddress") != "0x123" {
			t.Errorf("expected toAddress=0x123, got %s", q.Get("toAddress"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"toAmount":"1000","transactionRequest":{"data":"AQAA"}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	req := httptest.NewRequest("GET", "/swap/lifi/quote?fromChain="+svmChainID+"&toChain=42161&fromToken=SOL&toToken=0xdef&fromAmount=1000000000&fromAddress=EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v&toAddress=0x123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The all-chains search blob must request SVM explicitly — LiFi's /tokens
// defaults to EVM only, which silently drops every SPL token from search.
func TestGetAllChainTokens_RequestsNonEVMTypesAndFlattensSolana(t *testing.T) {
	const svmChainID = "1151111081099710"
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("chainTypes") != lifiChainTypes {
			t.Errorf("expected chainTypes=%s, got %s", lifiChainTypes, r.URL.Query().Get("chainTypes"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{
			"56":[{"address":"0xabc","symbol":"USDT","decimals":18,"name":"Tether","chainId":56}],
			"` + svmChainID + `":[{"address":"So11111111111111111111111111111111111111112","symbol":"SOL","decimals":9,"name":"Solana"}]
		}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)
	r.GET("/swap/lifi/tokens/all", h.GetAllChainTokens)

	req := httptest.NewRequest("GET", "/swap/lifi/tokens/all?q=sol", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Tokens []struct {
			Symbol  string `json:"symbol"`
			ChainID int64  `json:"chainId"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	found := false
	for _, tok := range body.Tokens {
		if tok.Symbol == "SOL" && tok.ChainID == 1151111081099710 {
			found = true
		}
	}
	if !found {
		t.Fatalf("SOL on the SVM chain id missing from flattened tokens: %s", w.Body.String())
	}
}

// The picker's default list asks for major coins by coinKey. It must get only
// verified tokens with those keys, carry coinKey/verificationStatus through,
// and never see a security-flagged token — not even in free-text search.
func TestGetAllChainTokens_CoinKeysVerifiedOnlyAndDropsFlagged(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{
			"8453":[
				{"address":"0x833589fc","symbol":"USDC","decimals":6,"name":"USD Coin","chainId":8453,"coinKey":"USDC","verificationStatus":"verified"},
				{"address":"0xfake","symbol":"USDC","decimals":6,"name":"USDC Rewards","chainId":8453,"coinKey":"USDC","verificationStatus":"unverified"},
				{"address":"0xbad","symbol":"USDT","decimals":6,"name":"Tether Drainer","chainId":8453,"coinKey":"USDT","verificationStatus":"flagged"},
				{"address":"0xnest","symbol":"NEST","decimals":18,"name":"Nest","chainId":8453,"verificationStatus":"unverified"}
			],
			"56":[{"address":"0x55d3","symbol":"USDT","decimals":18,"name":"Tether","chainId":56,"coinKey":"USDT","verificationStatus":"verified"}]
		}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)
	r.GET("/swap/lifi/tokens/all", h.GetAllChainTokens)

	get := func(query string) []tokenWithChain {
		t.Helper()
		req := httptest.NewRequest("GET", "/swap/lifi/tokens/all?"+query, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", query, w.Code, w.Body.String())
		}
		var body struct {
			Tokens []tokenWithChain `json:"tokens"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("response not valid JSON: %v", err)
		}
		return body.Tokens
	}

	majors := get("coinKeys=usdc,USDT")
	if len(majors) != 2 {
		t.Fatalf("coinKeys: got %d tokens, want the 2 verified ones: %+v", len(majors), majors)
	}
	for _, tok := range majors {
		if tok.VerificationStatus != "verified" || tok.CoinKey == "" {
			t.Errorf("coinKeys returned %+v", tok)
		}
	}

	for _, tok := range get("q=usdt") {
		if tok.Address == "0xbad" {
			t.Fatal("a security-flagged token reached search results")
		}
	}
	// Unverified tokens stay searchable, labelled.
	found := false
	for _, tok := range get("q=nest") {
		if tok.Address == "0xnest" && tok.VerificationStatus == "unverified" {
			found = true
		}
	}
	if !found {
		t.Fatal("unverified token missing from search, or its status was not carried through")
	}
}

// A broad search must put the real coin first even when many lookalikes sort
// ahead of it alphabetically, because the app only takes the first page.
func TestGetAllChainTokens_SearchRanksExactAndVerifiedFirst(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{"8453":[
			{"address":"0xa","symbol":"AUSDC","decimals":6,"name":"a usdc","chainId":8453,"verificationStatus":"unverified"},
			{"address":"0xb","symbol":"BUSDC","decimals":6,"name":"b usdc","chainId":8453,"verificationStatus":"unverified"},
			{"address":"0xc","symbol":"USDC","decimals":6,"name":"USDC fake","chainId":8453,"verificationStatus":"unverified"},
			{"address":"0x833589fc","symbol":"USDC","decimals":6,"name":"USD Coin","chainId":8453,"coinKey":"USDC","verificationStatus":"verified"}
		]}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)
	r.GET("/swap/lifi/tokens/all", h.GetAllChainTokens)

	req := httptest.NewRequest("GET", "/swap/lifi/tokens/all?q=usdc&limit=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body struct {
		Tokens []tokenWithChain `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Tokens) != 1 {
		t.Fatalf("bad response: %v %s", err, w.Body.String())
	}
	if body.Tokens[0].Address != "0x833589fc" {
		t.Fatalf("first result = %+v, want the verified USDC", body.Tokens[0])
	}
}

// The picker asks, in one request, which of the tokens a wallet holds LiFi can
// swap: exact chain+address pairs, case-insensitive, never a flagged token.
func TestGetAllChainTokens_TokensExactPairs(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"tokens":{
			"8453":[
				{"address":"0xNest","symbol":"NEST","decimals":18,"name":"Nest","chainId":8453,"verificationStatus":"unverified"},
				{"address":"0xnest2","symbol":"NEST2","decimals":18,"name":"Nest two","chainId":8453,"verificationStatus":"unverified"},
				{"address":"0xbad","symbol":"USDT","decimals":6,"name":"Tether Drainer","chainId":8453,"verificationStatus":"flagged"}
			],
			"56":[{"address":"0x55d3","symbol":"USDT","decimals":18,"name":"Tether","chainId":56,"coinKey":"USDT","verificationStatus":"verified"}]
		}}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)
	r.GET("/swap/lifi/tokens/all", h.GetAllChainTokens)

	// 0x55d3 is asked for on the wrong chain too: a pair matches on both halves.
	req := httptest.NewRequest("GET", "/swap/lifi/tokens/all?tokens=8453:0xNEST,56:0x55D3,8453:0xbad,8453:0x55d3,junk", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Tokens []tokenWithChain `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	got := map[string]bool{}
	for _, tok := range body.Tokens {
		got[fmt.Sprintf("%d:%s", tok.ChainID, tok.Address)] = true
	}
	if len(got) != 2 || !got["8453:0xNest"] || !got["56:0x55d3"] {
		t.Fatalf("tokens = %+v, want exactly 8453:0xNest and 56:0x55d3", body.Tokens)
	}
}

func TestParseTokenPairs(t *testing.T) {
	if parseTokenPairs("  ") != nil {
		t.Fatal("an absent param must be nil, not an empty filter that matches nothing")
	}
	got := parseTokenPairs(":0xabc,0:0xabc,-1:0xabc,x:0xabc,56:,56,56:0xAbC, 8453:0xdef ")
	if len(got) != 2 {
		t.Fatalf("got %v, want only 56:0xabc and 8453:0xdef", got)
	}
	for _, k := range []string{"56:0xabc", "8453:0xdef"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing %s in %v", k, got)
		}
	}

	var many []string
	for i := 0; i < maxTokenPairs+50; i++ {
		many = append(many, fmt.Sprintf("1:0x%x", i))
	}
	if n := len(parseTokenPairs(strings.Join(many, ","))); n != maxTokenPairs {
		t.Fatalf("read %d pairs, want the cap of %d", n, maxTokenPairs)
	}
}

// A pasted contract address must not return a security-flagged token.
func TestGetToken_RefusesFlagged(t *testing.T) {
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Query().Get("token") == "0xbad" {
			w.Write([]byte(`{"address":"0xbad","symbol":"USDT","verificationStatus":"flagged"}`))
			return
		}
		w.Write([]byte(`{"address":"0xok","symbol":"USDT","verificationStatus":"verified"}`))
	}))
	defer mockLiFi.Close()

	h := newTestLiFiHandler(mockLiFi.URL)
	r := setupTestRouter(h)

	for token, want := range map[string]int{"0xbad": http.StatusNotFound, "0xok": http.StatusOK} {
		req := httptest.NewRequest("GET", "/swap/lifi/token?chain=8453&token="+token, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d (%s)", token, w.Code, want, w.Body.String())
		}
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
			got := h.liveRequeryQuoteUSD(settleQuoteParams{
				FromChain: "56", ToChain: "56", FromToken: "0xabc", ToToken: "0xdef", FromAmount: "1000000",
			})
			if got.Reachable {
				t.Fatalf("malformed 200 must not count as reachable, got %+v", got)
			}
			if got.FromAmountUSD != 0 {
				t.Fatalf("malformed 200 must yield usd=0, got %v", got.FromAmountUSD)
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
	got := h.liveRequeryQuoteUSD(settleQuoteParams{
		FromChain: "56", ToChain: "56", FromToken: "0xabc", ToToken: "0xdef", FromAmount: "1000000",
	})
	if !got.Reachable || !got.FeeCollectable || got.FromAmountUSD != 123.45 {
		t.Fatalf("valid 200 want usd=123.45 reachable+collectable, got %+v", got)
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

// Bitcoin and Solana are the two non-EVM families the app can sign for, and
// LiFi omits both unless the chain type is named. A regression here empties
// them out of the swap picker with no error anywhere.
func TestLiFiChainTypes_CoverSignableNonEVMFamilies(t *testing.T) {
	for _, want := range []string{"EVM", "SVM", "UTXO"} {
		if !strings.Contains(lifiChainTypes, want) {
			t.Errorf("lifiChainTypes %q is missing %s", lifiChainTypes, want)
		}
	}
}

// Values from the client are encoded into our LiFi requests, never spliced: a
// token field carrying "&fromAmount=1" must arrive as one literal value, not
// as a second fromAmount that re-prices the settle.
func TestLiFiRequests_EncodeClientValues(t *testing.T) {
	var seen []url.Values
	mockLiFi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"address":"0xabc","estimate":{"fromAmountUSD":"1"}}`))
	}))
	defer mockLiFi.Close()
	h := newTestLiFiHandler(mockLiFi.URL)

	h.liveRequeryQuoteUSD(settleQuoteParams{
		FromChain: "56", ToChain: "56", FromToken: "0xabc&fromAmount=1", ToToken: "0xdef", FromAmount: "1000000",
	})
	req := httptest.NewRequest("GET", "/swap/lifi/token?chain=56&token="+url.QueryEscape("0xabc&chain=1"), nil)
	setupTestRouter(h).ServeHTTP(httptest.NewRecorder(), req)

	if len(seen) != 2 {
		t.Fatalf("want 2 LiFi calls, got %d", len(seen))
	}
	if got := seen[0]["fromAmount"]; len(got) != 1 || got[0] != "1000000" {
		t.Errorf("quote fromAmount = %v, want only 1000000", got)
	}
	if got := seen[0].Get("fromToken"); got != "0xabc&fromAmount=1" {
		t.Errorf("quote fromToken = %q", got)
	}
	if got := seen[1]["chain"]; len(got) != 1 || got[0] != "56" {
		t.Errorf("token chain = %v, want only 56", got)
	}
}
