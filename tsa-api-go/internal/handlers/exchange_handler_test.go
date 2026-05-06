package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// ── mock transport ───────────────────────────────────────────

// mockExchangeTransport returns canned Open Exchange Rates API responses.
type mockExchangeTransport struct{}

func (m *mockExchangeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Return a valid OER-format response
	body, _ := json.Marshal(map[string]any{
		"disclaimer": "Test data",
		"license":    "MIT",
		"timestamp":  1700000000,
		"base":       "USD",
		"rates": map[string]float64{
			"NGN": 1550.0,
			"GHS": 15.0,
			"KES": 130.0,
		},
	})

	return &http.Response{
		StatusCode:    200,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
	}, nil
}

func newMockP2PService() *services.P2PService {
	return &services.P2PService{
		HTTPClient: &http.Client{
			Transport: &mockExchangeTransport{},
			Timeout:   5 * time.Second,
		},
		AppID:              "test_key",
		SupportedCurrencies: []string{"NGN", "GHS", "KES"},
	}
}

// ── handler tests ────────────────────────────────────────────

func TestGetRates(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewExchangeHandler(newMockP2PService())

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/api/exchange/rates", nil)

	h.GetRates(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %T", resp["data"])
	}

	if data["base"] != "USD" {
		t.Errorf("expected base=USD, got %v", data["base"])
	}

	rates, ok := data["rates"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected rates object, got %T", data["rates"])
	}

	// USD should always be present at 1:1
	usd, ok := rates["USD"].(map[string]interface{})
	if !ok {
		t.Fatal("expected USD in rates")
	}
	if usd["midRate"] != float64(1) {
		t.Errorf("expected USD midRate=1, got %v", usd["midRate"])
	}

	// NGN should be present
	ngn, ok := rates["NGN"].(map[string]interface{})
	if !ok {
		t.Fatal("expected NGN in rates")
	}
	if ngn["midRate"].(float64) <= 0 {
		t.Errorf("expected positive NGN midRate, got %v", ngn["midRate"])
	}
	if ngn["symbol"] != "₦" {
		t.Errorf("expected NGN symbol ₦, got %v", ngn["symbol"])
	}
}

func TestConvertPrice_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewExchangeHandler(newMockP2PService())

	body := toJSONBody(t, map[string]interface{}{
		"amount":   100,
		"currency": "NGN",
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h.ConvertPrice(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %T", resp["data"])
	}

	if data["usdAmount"] != float64(100) {
		t.Errorf("expected usdAmount=100, got %v", data["usdAmount"])
	}
	if data["currency"] != "NGN" {
		t.Errorf("expected currency=NGN, got %v", data["currency"])
	}
	if data["symbol"] != "₦" {
		t.Errorf("expected symbol=₦, got %v", data["symbol"])
	}
	localAmount, ok := data["localAmount"].(float64)
	if !ok || localAmount <= 0 {
		t.Errorf("expected positive localAmount, got %v", data["localAmount"])
	}
	rate, ok := data["rate"].(float64)
	if !ok || rate <= 0 {
		t.Errorf("expected positive rate, got %v", data["rate"])
	}
}

func TestConvertPrice_IncludesStaleAndSourceFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewExchangeHandler(newMockP2PService())

	w := httptest.NewRecorder()
	body := toJSONBody(t, map[string]interface{}{"amount": 100, "currency": "NGN"})
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h.ConvertPrice(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	data := resp["data"].(map[string]interface{})

	// Fresh data: stale should be false, source should be present.
	if v, ok := data["stale"].(bool); !ok || v {
		t.Errorf("expected stale=false on fresh data, got %v", data["stale"])
	}
	if _, ok := data["source"]; !ok {
		t.Error("expected source field in convert response")
	}
}

// TestConvertPrice_UnknownCurrencyDoesNotPanic verifies that the handler no
// longer nil-derefs on GetRate failure (regression: old code did
// `rate, _ := GetRate(...); rate.MidRate` which panicked on unknown codes).
func TestConvertPrice_UnknownCurrencyDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewExchangeHandler(newMockP2PService())

	w := httptest.NewRecorder()
	body := toJSONBody(t, map[string]interface{}{"amount": 100, "currency": "ZZZ"})
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ConvertPrice panicked on unknown currency: %v", r)
		}
	}()
	h.ConvertPrice(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on unknown currency, got %d: %s", w.Code, w.Body.String())
	}
}

// TestConvertPrice_UpstreamFailureReturns500 verifies that an OER outage / no
// API key configured maps to 500 (not 400 — that would mislead operators into
// blaming the client) and that the response message is sanitized (no internal
// strings like "Open Exchange Rates API key not configured" leaked to clients).
func TestConvertPrice_UpstreamFailureReturns500(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Build a service with no AppID so fetchOERates errors out.
	svc := &services.P2PService{
		HTTPClient:          &http.Client{Timeout: 1 * time.Second},
		AppID:               "",
		SupportedCurrencies: []string{"NGN"},
	}
	h := NewExchangeHandler(svc)

	w := httptest.NewRecorder()
	body := toJSONBody(t, map[string]interface{}{"amount": 100, "currency": "NGN"})
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h.ConvertPrice(c)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on upstream failure, got %d: %s", w.Code, w.Body.String())
	}
	body2 := w.Body.String()
	if strings.Contains(body2, "Open Exchange") || strings.Contains(body2, "app_id") || strings.Contains(body2, "API key") {
		t.Errorf("response leaked internal infrastructure details: %s", body2)
	}
}

func TestConvertPrice_RejectsNegativeAndNonFiniteAmounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewExchangeHandler(newMockP2PService())

	cases := []struct {
		name   string
		amount any
	}{
		{"negative", -100.0},
		// JSON doesn't natively encode NaN/Inf; clients sending strings or
		// non-standard floats hit the bind-error path which already 400s.
		// The most realistic in-band attack is a negative number.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body := toJSONBody(t, map[string]interface{}{"amount": tc.amount, "currency": "NGN"})
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
			c.Request.Header.Set("Content-Type", "application/json")
			h.ConvertPrice(c)
			if w.Code != http.StatusBadRequest {
				t.Errorf("amount=%v: expected 400, got %d: %s", tc.amount, w.Code, w.Body.String())
			}
		})
	}
}

func TestConvertPrice_MissingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewExchangeHandler(newMockP2PService())

	w := httptest.NewRecorder()
	body := toJSONBody(t, map[string]interface{}{})
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h.ConvertPrice(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestConvertPrice_MissingAmount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewExchangeHandler(newMockP2PService())

	w := httptest.NewRecorder()
	body := toJSONBody(t, map[string]interface{}{"currency": "NGN"})
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h.ConvertPrice(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestConvertPrice_MissingCurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewExchangeHandler(newMockP2PService())

	w := httptest.NewRecorder()
	body := toJSONBody(t, map[string]interface{}{"amount": 100})
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/exchange/convert", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h.ConvertPrice(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ── helpers ──────────────────────────────────────────────────

func toJSONBody(t *testing.T, v interface{}) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return bytes.NewReader(b)
}
