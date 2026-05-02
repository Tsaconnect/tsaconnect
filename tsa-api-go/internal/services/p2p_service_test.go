package services

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── mock HTTP transport ──────────────────────────────────────

// mockTransport serves canned Open Exchange Rates API responses.
type mockTransport struct {
	oerResponse func() (int, any) // returns (statusCode, body) — body is JSON-marshalled
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL.String()

	var status int
	var body any

	if strings.Contains(u, "openexchangerates.org") && m.oerResponse != nil {
		status, body = m.oerResponse()
	} else {
		status = 500
		body = `{"error":"unexpected request"}`
	}

	return mockResponse(status, body), nil
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
		AppID:              "test_key",
		SupportedCurrencies: []string{"NGN", "GHS", "KES"},
	}
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

// ── unit tests: pure helpers ─────────────────────────────────

func TestParseFloatSafe(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"1540.50", 1540.50},
		{"0", 0},
		{"", 0},
		{"abc", 0},
		{"  1550  ", 1550},
		{"-10.5", -10.5},
	}
	for _, tc := range tests {
		got := parseFloatSafe(tc.input)
		if got != tc.want {
			t.Errorf("parseFloatSafe(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestCurrencySymbol(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"USD", "$"},
		{"NGN", "₦"},
		{"GHS", "GH₵"},
		{"KES", "KSh"},
		{"ZAR", "R"},
		{"EUR", "€"},
		{"GBP", "£"},
		{"XYZ", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := currencySymbol(tc.code)
		if got != tc.want {
			t.Errorf("currencySymbol(%q) = %q, want %q", tc.code, got, tc.want)
		}
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
		AppID:              "",
		SupportedCurrencies: []string{"NGN"},
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
	// Override TTL to be short for testing
	svc.cacheTTL = 5 * time.Second

	// First call hits the API
	_, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 API call, got %d", callCount)
	}

	// Second call should use cache
	_, err = svc.GetAllRates()
	if err != nil {
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

	// First call
	_, err := svc.GetAllRates()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Wait for cache to expire
	time.Sleep(5 * time.Millisecond)

	// Second call should hit API again
	_, err = svc.GetAllRates()
	if err != nil {
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
	if result != 155000 { // 100 * 1550
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

func TestRate_BuySellMidAreEqual(t *testing.T) {
	// With OER, BinanceBuy == BinanceSell == MidRate (mid-market rate)
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
			t.Errorf("%s: expected BinanceBuy=BinanceSell=MidRate, got buy=%v sell=%v mid=%v",
				code, r.BinanceBuy, r.BinanceSell, r.MidRate)
		}
	}
}
