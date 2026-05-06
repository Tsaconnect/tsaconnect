package services

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── mock HTTP transport ──────────────────────────────────────

// mockTransport serves canned responses for OER and Bybit P2P URLs.
type mockTransport struct {
	oerResponse   func() (int, any)
	bybitResponse func(side string) (int, any) // called per side ("0"=buy, "1"=sell)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL.String()

	if strings.Contains(u, "openexchangerates.org") && m.oerResponse != nil {
		s, b := m.oerResponse()
		return mockResponse(s, b), nil
	}
	if strings.Contains(u, "api2.bybit.com") && m.bybitResponse != nil {
		// Read side from request body
		body, _ := io.ReadAll(req.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		side, _ := parsed["side"].(string)
		s, b := m.bybitResponse(side)
		return mockResponse(s, b), nil
	}
	return mockResponse(500, `{"error":"unexpected request"}`), nil
}

func mockResponse(status int, body any) *http.Response {
	var bodyReader io.ReadCloser
	if body != nil {
		switch v := body.(type) {
		case string:
			bodyReader = io.NopCloser(strings.NewReader(v))
		case []byte:
			bodyReader = io.NopCloser(strings.NewReader(string(v)))
		default:
			b, _ := json.Marshal(body)
			bodyReader = io.NopCloser(strings.NewReader(string(b)))
		}
	} else {
		bodyReader = io.NopCloser(strings.NewReader(""))
	}
	return &http.Response{
		StatusCode:    status,
		Body:          bodyReader,
		ContentLength: -1,
		Header:        make(http.Header),
	}
}

// ── helpers ──────────────────────────────────────────────────

func newTestP2PService(transport *mockTransport) *P2PService {
	if transport == nil {
		transport = &mockTransport{}
	}
	return &P2PService{
		HTTPClient:          &http.Client{Transport: transport, Timeout: 5 * time.Second},
		cacheTTL:            1 * time.Hour,
		degradedTTL:         60 * time.Second,
		maxStaleAge:         6 * time.Hour,
		AppID:               "test_key",
		SupportedCurrencies: []string{"NGN", "GHS", "KES"},
		bybitCurrencies:     map[string]bool{},
	}
}

func newTestP2PServiceWithBybit(transport *mockTransport, bybitCurrencies ...string) *P2PService {
	svc := newTestP2PService(transport)
	for _, c := range bybitCurrencies {
		svc.bybitCurrencies[c] = true
	}
	return svc
}

// oerSuccessBody returns a canned Open Exchange Rates API response.
func oerSuccessBody() map[string]any {
	return map[string]any{
		"disclaimer": "Test data",
		"license":    "MIT",
		"timestamp":  1700000000,
		"base":       "USD",
		"rates": map[string]float64{
			"NGN": 1550.0,
			"GHS": 15.0,
			"KES": 130.0,
			"ZAR": 17.9,
			"UGX": 3800.0,
			"TZS": 2600.0,
			"RWF": 1350.0,
			"XOF": 620.0,
			"XAF": 620.0,
			"EUR": 0.92,
			"GBP": 0.79,
		},
	}
}

// bybitItemBody constructs a single qualifying ad (fixed price, 95% rate, 100 finished).
func bybitItemBody(price string) map[string]any {
	return map[string]any{
		"price":             price,
		"priceType":         0,
		"finishNum":         100,
		"recentExecuteRate": 95,
	}
}

// bybitSuccessBody returns 5 ads at the given prices.
func bybitSuccessBody(prices ...string) map[string]any {
	items := make([]any, 0, len(prices))
	for _, p := range prices {
		items = append(items, bybitItemBody(p))
	}
	return map[string]any{
		"ret_code": 0,
		"ret_msg":  "SUCCESS",
		"result": map[string]any{
			"count": len(prices),
			"items": items,
		},
	}
}

// ── unit tests: pure helpers ─────────────────────────────────

func TestCurrencyMeta(t *testing.T) {
	// Known codes pull from the static table.
	if m := currencyMeta("USD"); m.Symbol != "$" || m.Name != "US Dollar" || m.Flag != "🇺🇸" {
		t.Errorf("USD metadata: got %+v", m)
	}
	if m := currencyMeta("NGN"); m.Symbol != "₦" || m.Flag != "🇳🇬" {
		t.Errorf("NGN metadata: got %+v", m)
	}
	// Unknown codes fall back to the code as both name and symbol.
	if m := currencyMeta("XYZ"); m.Code != "XYZ" || m.Symbol != "XYZ" || m.Name != "XYZ" {
		t.Errorf("unknown code fallback: got %+v", m)
	}
}

func TestIsKnownCurrency(t *testing.T) {
	if !isKnownCurrency("USD") || !isKnownCurrency("NGN") {
		t.Error("expected USD and NGN to be known")
	}
	if isKnownCurrency("XAU") || isKnownCurrency("BTC") {
		t.Error("precious metals / crypto should not be in the fiat metadata table")
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		in   []float64
		want float64
	}{
		{[]float64{1500, 1510, 1520, 1530, 1540}, 1520},
		{[]float64{1500, 1510}, 1505},
		{[]float64{1500}, 1500},
		{[]float64{1540, 1500, 1530, 1510, 1520}, 1520}, // unsorted input
	}
	for _, tc := range tests {
		got := median(tc.in)
		if got != tc.want {
			t.Errorf("median(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestStripControlChars(t *testing.T) {
	in := []byte("hello\x01world\n\tkeep\x1fme")
	got := stripControlChars(in)
	want := "helloworld\n\tkeepme"
	if string(got) != want {
		t.Errorf("stripControlChars = %q, want %q", got, want)
	}
}

func TestFilterBybitItems(t *testing.T) {
	items := []bybitItem{
		{Price: "1500", PriceType: 0, FinishNum: 100, RecentExecuteRate: 95}, // keep
		{Price: "1510", PriceType: 1, FinishNum: 100, RecentExecuteRate: 95}, // drop: floating
		{Price: "1520", PriceType: 0, FinishNum: 100, RecentExecuteRate: 80}, // drop: low rate
		{Price: "1530", PriceType: 0, FinishNum: 10, RecentExecuteRate: 95},  // drop: too few trades
		{Price: "1540", PriceType: 0, FinishNum: 100, RecentExecuteRate: 95}, // keep
	}
	got := filterBybitItems(items)
	if len(got) != 2 || got[0] != 1500 || got[1] != 1540 {
		t.Errorf("filterBybitItems = %v, want [1500 1540]", got)
	}
}

// ── OER API integration tests ────────────────────────────────

func TestGetAllRates_IncludesUSD(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	usd, ok := rates["USD"]
	if !ok {
		t.Fatal("expected USD in rates")
	}
	if usd.MidRate != 1.0 || usd.BinanceBuy != 1.0 || usd.BinanceSell != 1.0 {
		t.Errorf("USD should be 1:1, got %+v", usd)
	}
	if usd.Symbol != "$" {
		t.Errorf("USD symbol should be $, got %s", usd.Symbol)
	}
}

func TestGetAllRates_ReturnsAllSupportedCurrencies(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	for _, code := range []string{"USD", "NGN", "GHS", "KES"} {
		r, ok := rates[code]
		if !ok {
			t.Errorf("expected rate for %s", code)
			continue
		}
		if r.MidRate <= 0 {
			t.Errorf("%s mid rate should be positive, got %f", code, r.MidRate)
		}
		if code == "USD" && r.MidRate != 1.0 {
			t.Errorf("USD should be 1.0, got %f", r.MidRate)
		}
	}
}

func TestGetAllRates_ApiReturnsError(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 500, `{"error":"server error"}` },
	})

	_, err := svc.GetAllRates()
	if err == nil {
		t.Fatal("expected error when API returns 500")
	}
}

func TestGetAllRates_NoApiKey(t *testing.T) {
	svc := &P2PService{
		HTTPClient:          &http.Client{},
		cacheTTL:            time.Hour,
		degradedTTL:         time.Minute,
		maxStaleAge:         6 * time.Hour,
		AppID:               "",
		SupportedCurrencies: []string{"NGN"},
		bybitCurrencies:     map[string]bool{},
	}

	_, err := svc.GetAllRates()
	if err == nil {
		t.Fatal("expected error when API key is empty")
	}
}

func TestGetAllRates_Cache(t *testing.T) {
	callCount := 0
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) {
			callCount++
			return 200, oerSuccessBody()
		},
	})
	svc.cacheTTL = 5 * time.Second

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 API call, got %d", callCount)
	}

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 0 additional API calls (cached), got %d", callCount)
	}
}

func TestGetAllRates_CacheExpiry(t *testing.T) {
	callCount := 0
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) {
			callCount++
			return 200, oerSuccessBody()
		},
	})
	svc.cacheTTL = 1 * time.Millisecond

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls (cache expired), got %d", callCount)
	}
}

func TestConvertUSD(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	result, err := svc.ConvertUSD(100, "NGN")
	if err != nil {
		t.Fatalf("ConvertUSD: %v", err)
	}
	if result != 155000 {
		t.Errorf("expected 155000, got %f", result)
	}
}

func TestConvertUSD_UnsupportedCurrency(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	_, err := svc.ConvertUSD(100, "XYZ")
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestGetRate(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	rate, err := svc.GetRate("NGN")
	if err != nil {
		t.Fatalf("GetRate: %v", err)
	}
	if rate.Currency != "NGN" {
		t.Errorf("expected NGN, got %s", rate.Currency)
	}
	if rate.Symbol != "₦" {
		t.Errorf("expected ₦, got %s", rate.Symbol)
	}
	if rate.MidRate != 1550.0 {
		t.Errorf("expected rate 1550, got %f", rate.MidRate)
	}
}

func TestGetRate_UnsupportedCurrency(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	_, err := svc.GetRate("XYZ")
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestGetAllRates_WildcardReturnsAllKnownCurrencies(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})
	svc.SupportedCurrencies = []string{"*"}

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	// Mock body has 11 fiat codes + USD always added = 12.
	want := []string{"USD", "NGN", "GHS", "KES", "ZAR", "UGX", "TZS", "RWF", "XOF", "XAF", "EUR", "GBP"}
	for _, code := range want {
		if _, ok := rates[code]; !ok {
			t.Errorf("expected rate for %s under wildcard, missing", code)
		}
	}
	for code, r := range rates {
		if r.Name == "" || r.Flag == "" {
			t.Errorf("%s missing metadata: name=%q flag=%q", code, r.Name, r.Flag)
		}
	}
}

func TestGetAllRates_FiltersUnknownCodes(t *testing.T) {
	body := oerSuccessBody()
	body["rates"].(map[string]float64)["XAU"] = 0.0005 // gold — not in metadata table
	body["rates"].(map[string]float64)["BTC"] = 0.00002

	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, body },
	})
	svc.SupportedCurrencies = []string{"*"}

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}
	if _, ok := rates["XAU"]; ok {
		t.Error("XAU should be filtered out (not a fiat currency)")
	}
	if _, ok := rates["BTC"]; ok {
		t.Error("BTC should be filtered out (not a fiat currency)")
	}
}

func TestRate_OERBuySellMidAreEqual(t *testing.T) {
	// Currencies routed through OER have BinanceBuy == BinanceSell == MidRate.
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	for _, code := range []string{"NGN", "GHS", "KES"} {
		r := rates[code]
		if r.BinanceBuy != r.MidRate || r.BinanceSell != r.MidRate {
			t.Errorf("%s: expected buy=sell=mid, got buy=%v sell=%v mid=%v",
				code, r.BinanceBuy, r.BinanceSell, r.MidRate)
		}
		if r.Source != "oer" {
			t.Errorf("%s: expected source=oer, got %s", code, r.Source)
		}
	}
}

// ── Bybit P2P integration tests ──────────────────────────────

func TestGetAllRates_BybitOverridesOER(t *testing.T) {
	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) {
			if side == "1" { // sell ads (buy side rate)
				return 200, bybitSuccessBody("1600", "1605", "1610", "1615", "1620")
			}
			return 200, bybitSuccessBody("1580", "1585", "1590", "1595", "1600")
		},
	}, "NGN")

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	ngn := rates["NGN"]
	if ngn.Source != "bybit" {
		t.Errorf("expected source=bybit, got %s", ngn.Source)
	}
	if ngn.BinanceBuy != 1610 {
		t.Errorf("expected BinanceBuy=1610 (median of sell ads), got %v", ngn.BinanceBuy)
	}
	if ngn.BinanceSell != 1590 {
		t.Errorf("expected BinanceSell=1590 (median of buy ads), got %v", ngn.BinanceSell)
	}
	if ngn.MidRate != 1600 {
		t.Errorf("expected MidRate=1600, got %v", ngn.MidRate)
	}

	// Non-Bybit currency should still come from OER unchanged.
	ghs := rates["GHS"]
	if ghs.Source != "oer" || ghs.MidRate != 15.0 {
		t.Errorf("expected GHS unchanged from OER, got %+v", ghs)
	}
}

func TestGetAllRates_BybitFailsFallsBackToOER(t *testing.T) {
	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) {
			return 500, `{"error":"unavailable"}`
		},
	}, "NGN")

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	ngn := rates["NGN"]
	if ngn.Source != "oer" {
		t.Errorf("expected fallback to OER, got source=%s", ngn.Source)
	}
	if ngn.MidRate != 1550.0 {
		t.Errorf("expected OER fallback rate 1550, got %v", ngn.MidRate)
	}
}

func TestGetAllRates_BybitNoQualifyingAdsFallsBackToOER(t *testing.T) {
	// All ads filtered out (low completion rate)
	lowQualityAd := map[string]any{
		"price":             "1600",
		"priceType":         0,
		"finishNum":         100,
		"recentExecuteRate": 50, // below 90 threshold
	}
	body := map[string]any{
		"ret_code": 0,
		"ret_msg":  "SUCCESS",
		"result": map[string]any{
			"count": 1,
			"items": []any{lowQualityAd},
		},
	}

	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse:   func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) { return 200, body },
	}, "NGN")

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}
	if rates["NGN"].Source != "oer" {
		t.Errorf("expected fallback to OER when no ads qualify, got source=%s", rates["NGN"].Source)
	}
}

func TestFetchBybitRate_HandlesControlChars(t *testing.T) {
	// Inject a control char into a JSON response to verify sanitization.
	rawJSON := `{"ret_code":0,"ret_msg":"OK","result":{"count":1,"items":[{"price":"1600","priceType":0,"finishNum":100,"recentExecuteRate":95,"remark":"hi` + "\x01" + `bye"}]}}`

	svc := newTestP2PServiceWithBybit(&mockTransport{
		bybitResponse: func(side string) (int, any) { return 200, rawJSON },
	}, "NGN")

	rate, err := svc.fetchBybitRate("NGN")
	if err != nil {
		t.Fatalf("fetchBybitRate: %v", err)
	}
	if rate.MidRate != 1600 {
		t.Errorf("expected mid 1600, got %v", rate.MidRate)
	}
}

// ── Bybit failure modes (gaps surfaced by review) ────────────

func TestGetAllRates_BybitOneSideFailsMarksStale(t *testing.T) {
	// side=1 succeeds, side=0 returns 500. The whole NGN row should fall back
	// to OER mid-market and be marked Stale=true.
	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) {
			if side == "1" {
				return 200, bybitSuccessBody("1600", "1605", "1610", "1615", "1620")
			}
			return 500, `{"error":"thin book"}`
		},
	}, "NGN")

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}
	ngn := rates["NGN"]
	if ngn.Source != SourceOER {
		t.Errorf("expected OER fallback source, got %s", ngn.Source)
	}
	if !ngn.Stale {
		t.Error("expected NGN row to be marked Stale=true on partial Bybit failure")
	}
	if ngn.MidRate != 1550.0 {
		t.Errorf("expected OER fallback mid 1550, got %v", ngn.MidRate)
	}
}

func TestGetAllRates_BybitRetCodeNonZeroMarksStale(t *testing.T) {
	// Bybit returns HTTP 200 with ret_code != 0 (rate-limit, geo-block, etc.).
	body := map[string]any{
		"ret_code": 10001,
		"ret_msg":  "Too many requests",
		"result":   map[string]any{"count": 0, "items": []any{}},
	}
	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse:   func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) { return 200, body },
	}, "NGN")

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}
	if !rates["NGN"].Stale {
		t.Error("expected NGN to be marked stale when Bybit returns ret_code != 0")
	}
}

func TestGetAllRates_DegradedResponseHasShortTTL(t *testing.T) {
	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) {
			return 500, `{"error":"down"}`
		},
	}, "NGN")

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	entry, ok := svc.cache.Load("all")
	if !ok {
		t.Fatal("expected cache entry after degraded fetch")
	}
	ce := entry.(*p2pCacheEntry)
	ttl := time.Until(ce.expiresAt)
	if ttl > 90*time.Second {
		t.Errorf("degraded responses should use short TTL (≤90s), got %s", ttl)
	}
}

func TestGetAllRates_HealthyResponseUsesLongTTL(t *testing.T) {
	svc := newTestP2PServiceWithBybit(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
		bybitResponse: func(side string) (int, any) {
			return 200, bybitSuccessBody("1600", "1605", "1610", "1615", "1620")
		},
	}, "NGN")

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("GetAllRates: %v", err)
	}

	entry, ok := svc.cache.Load("all")
	if !ok {
		t.Fatal("expected cache entry")
	}
	ce := entry.(*p2pCacheEntry)
	ttl := time.Until(ce.expiresAt)
	if ttl < 30*time.Minute {
		t.Errorf("healthy responses should use long TTL (≈1h), got %s", ttl)
	}
}

// ── Stale-OER cache fallback (the gap that surfaced the dead-code bug) ──

func TestGetAllRates_OERFailureServesStaleCacheMarkedStale(t *testing.T) {
	// First call succeeds, populates cache. Second call (after expiry) sees
	// OER fail; should serve the prior snapshot with every row marked Stale.
	callCount := 0
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) {
			callCount++
			if callCount == 1 {
				return 200, oerSuccessBody()
			}
			return 500, `{"error":"down"}`
		},
	})
	svc.cacheTTL = 1 * time.Millisecond // expire quickly

	if _, err := svc.GetAllRates(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	rates, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("expected stale fallback to succeed; got error: %v", err)
	}
	for code, r := range rates {
		if !r.Stale {
			t.Errorf("%s should be marked Stale on OER fallback, got %+v", code, r)
		}
	}
}

func TestGetAllRates_OERFailureRefusesAncientCache(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 500, `{"error":"down"}` },
	})
	// Pre-populate cache with an entry that is older than maxStaleAge.
	svc.maxStaleAge = 1 * time.Hour
	svc.cache.Store("all", &p2pCacheEntry{
		rates:     map[string]P2PRate{"USD": {Currency: "USD", MidRate: 1.0}},
		cachedAt:  time.Now().Add(-2 * time.Hour),
		expiresAt: time.Now().Add(-1 * time.Hour),
	})

	if _, err := svc.GetAllRates(); err == nil {
		t.Fatal("expected error when cache exceeds maxStaleAge")
	}
}

// ── Singleflight ────────────────────────────────────────────

func TestGetAllRates_SingleflightCoalescesConcurrentCalls(t *testing.T) {
	var oerCalls int32
	var mu sync.Mutex
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) {
			mu.Lock()
			oerCalls++
			mu.Unlock()
			time.Sleep(20 * time.Millisecond) // simulate latency
			return 200, oerSuccessBody()
		},
	})

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := svc.GetAllRates(); err != nil {
				t.Errorf("GetAllRates: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if oerCalls != 1 {
		t.Errorf("expected 1 OER call under singleflight, got %d", oerCalls)
	}
}

// ── Constructor validation (Bybit + casing) ────────────────

func TestNewP2PService_DropsUnknownBybitCurrencies(t *testing.T) {
	svc := NewP2PService("key", []string{"NGN"}, []string{"NGN", "ZZZ", "ngn"})
	if !svc.bybitCurrencies["NGN"] {
		t.Error("NGN should be in bybitCurrencies")
	}
	if svc.bybitCurrencies["ZZZ"] {
		t.Error("unknown ZZZ should have been dropped")
	}
	if len(svc.bybitCurrencies) != 1 {
		t.Errorf("expected 1 valid Bybit currency (deduplicated), got %d", len(svc.bybitCurrencies))
	}
}

func TestNewP2PService_UppercasesSupportedCurrencies(t *testing.T) {
	svc := NewP2PService("key", []string{"ngn", "GHS", " kes "}, nil)
	want := []string{"NGN", "GHS", "KES"}
	for _, code := range want {
		if !svc.isExplicitlySupported(code) {
			t.Errorf("expected %s to be supported after uppercase normalization, got list %v", code, svc.SupportedCurrencies)
		}
	}
}

// ── parsePrice strict behavior ─────────────────────────────

func TestParsePrice(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		want float64
	}{
		{"1500.50", true, 1500.50},
		{"  1500  ", true, 1500},
		{"1,500.00", false, 0}, // thousands separator → reject
		{"abc", false, 0},
		{"0", false, 0},   // zero → reject
		{"-10", false, 0}, // negative → reject
		{"", false, 0},
		{"1e400", false, 0}, // overflow → +Inf → reject
	}
	for _, tc := range tests {
		got, ok := parsePrice(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parsePrice(%q) = (%v, %v); want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// ── roundRate conditional precision ────────────────────────

func TestRoundRate(t *testing.T) {
	// Rates >= 10 round to 2 decimals.
	if got := roundRate(1550.789); got != 1550.79 {
		t.Errorf("roundRate(1550.789) = %v, want 1550.79", got)
	}
	// Rates < 10 round to 4 decimals (otherwise 0.305 → 0.31, ~1.5% drift).
	if got := roundRate(0.30764); got != 0.3076 {
		t.Errorf("roundRate(0.30764) = %v, want 0.3076", got)
	}
	if got := roundRate(0.92); got != 0.92 {
		t.Errorf("roundRate(0.92) = %v, want 0.92", got)
	}
}

// ── stripControlChars edge cases (was a single-case test) ──

func TestStripControlChars_NoChangeReturnsOriginal(t *testing.T) {
	in := []byte(`{"clean":"json","value":42}`)
	out := stripControlChars(in)
	if &in[0] != &out[0] {
		t.Error("clean input should be returned without copying")
	}
}

func TestStripControlChars_PreservesUTF8(t *testing.T) {
	// "昵称" (Chinese for "nickname") plus an embedded \x01.
	in := []byte("\xe6\x98\xb5\xe7\xa7\xb0" + "\x01" + "tail")
	out := stripControlChars(in)
	if string(out) != "昵称tail" {
		t.Errorf("UTF-8 lost during stripping: got %q", string(out))
	}
}

func TestStripControlChars_StripsNULPrefix(t *testing.T) {
	in := []byte("\x00\x00data")
	if got := string(stripControlChars(in)); got != "data" {
		t.Errorf("got %q, want %q", got, "data")
	}
}

// ── Snapshot + StaleSeconds plumbing ────────────────────────

func TestGetSnapshot_FreshHasZeroStaleSeconds(t *testing.T) {
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) { return 200, oerSuccessBody() },
	})
	snap, err := svc.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.StaleSeconds != 0 {
		t.Errorf("fresh snapshot should have StaleSeconds=0, got %d", snap.StaleSeconds)
	}
	if snap.IsStale() {
		t.Error("fresh snapshot should not report IsStale")
	}
}

func TestGetSnapshot_OERFailureSurfacesAge(t *testing.T) {
	callCount := 0
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) {
			callCount++
			if callCount == 1 {
				return 200, oerSuccessBody()
			}
			return 500, `{"error":"down"}`
		},
	})
	svc.cacheTTL = 1 * time.Millisecond

	if _, err := svc.GetSnapshot(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	snap, err := svc.GetSnapshot()
	if err != nil {
		t.Fatalf("stale serve failed: %v", err)
	}
	if !snap.IsStale() {
		t.Error("expected snapshot served from stale cache to report IsStale")
	}
	if snap.StaleSeconds < 0 {
		t.Errorf("StaleSeconds should be >= 0, got %d", snap.StaleSeconds)
	}
}

func TestFetchAndCache_StaleServeWritesBackShortTTL(t *testing.T) {
	// Without writeback, every request during an OER outage would re-trigger
	// fetchOERates and burn the monthly budget. Verify the stale-served map
	// is cached so the next call short-circuits on loadFreshCache.
	callCount := 0
	svc := newTestP2PService(&mockTransport{
		oerResponse: func() (int, any) {
			callCount++
			if callCount == 1 {
				return 200, oerSuccessBody()
			}
			return 500, `{"error":"down"}`
		},
	})
	svc.cacheTTL = 1 * time.Millisecond
	svc.degradedTTL = 60 * time.Second

	if _, err := svc.GetSnapshot(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	preStaleCalls := callCount
	if _, err := svc.GetSnapshot(); err != nil {
		t.Fatalf("stale serve: %v", err)
	}
	staleCalls := callCount - preStaleCalls

	// Subsequent call within degradedTTL should hit cache, not OER.
	if _, err := svc.GetSnapshot(); err != nil {
		t.Fatalf("post-stale call: %v", err)
	}
	if delta := callCount - preStaleCalls - staleCalls; delta != 0 {
		t.Errorf("expected post-stale call to short-circuit on cache, but OER was called %d more time(s)", delta)
	}
}
