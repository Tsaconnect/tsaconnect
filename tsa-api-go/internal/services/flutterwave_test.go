package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ─── Webhook verifier ──────────────────────────────────────────────────────

func fwSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestFlutterwaveWebhookVerifier(t *testing.T) {
	secret := "whsec_test_123"
	v := NewFlutterwaveWebhookVerifier(secret)
	body := []byte(`{"webhook_id":"wh_1","type":"charge.completed","data":{}}`)

	if !v.Verify(body, fwSign(secret, body)) {
		t.Fatal("valid signature rejected")
	}
	if v.Verify(body, fwSign("wrong-secret", body)) {
		t.Fatal("signature under wrong secret accepted")
	}
	if v.Verify(body, "") {
		t.Fatal("empty signature accepted")
	}
	tampered := []byte(`{"webhook_id":"wh_1","type":"charge.completed","data":{"amount":1}}`)
	if v.Verify(tampered, fwSign(secret, body)) {
		t.Fatal("signature over different body accepted")
	}
}

// ─── Parsers ───────────────────────────────────────────────────────────────

func TestParseFlutterwaveInbound(t *testing.T) {
	raw := []byte(`{
		"webhook_id": "wh_evt_1",
		"timestamp": 1789000000,
		"type": "charge.completed",
		"data": {
			"id": "chg_123",
			"reference": "IT-BUY-abc123",
			"amount": 500,
			"currency": "NGN",
			"status": "succeeded",
			"customer": {"name": "Ada Obi", "email": "ada@x.com"},
			"payment_method": {"type": "bank_transfer"}
		}
	}`)
	p, err := ParseFlutterwaveInbound(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.EventID != "wh_evt_1" || p.AccountRef != "IT-BUY-abc123" {
		t.Fatalf("keys wrong: %+v", p)
	}
	if p.AmountNGN != "500.00" {
		t.Fatalf("amount not normalized to 2dp: %q", p.AmountNGN)
	}
	if p.Currency != "NGN" || p.Status != "settled" || p.SenderAccountName != "Ada Obi" {
		t.Fatalf("fields wrong: %+v", p)
	}

	// Live charge.completed payloads send customer.name as {first,last}.
	objName := []byte(`{
		"webhook_id": "wh_obj",
		"type": "charge.completed",
		"data": {
			"id": "chg_obj",
			"reference": "ITVA-abc",
			"amount": 100,
			"currency": "NGN",
			"status": "succeeded",
			"customer": {"id": "cus_1", "name": {"first": "Ada", "last": "Lovelace"}, "email": "ada@x.com"}
		}
	}`)
	p2, err := ParseFlutterwaveInbound(objName)
	if err != nil {
		t.Fatalf("parse object name: %v", err)
	}
	if p2.SenderAccountName != "Ada Lovelace" || p2.CustomerID != "cus_1" {
		t.Fatalf("object name fields wrong: %+v", p2)
	}

	// Failed charge → benign non-settling sentinel, not a hard error.
	failed := []byte(`{"webhook_id":"wh2","type":"charge.completed","data":{"reference":"r","status":"failed","amount":10,"currency":"NGN"}}`)
	if _, err := ParseFlutterwaveInbound(failed); !errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("failed charge: want ErrNonSettlingEvent, got %v", err)
	}

	// Missing attribution reference is a real error.
	noRef := []byte(`{"webhook_id":"wh3","type":"charge.completed","data":{"status":"succeeded","amount":10}}`)
	if _, err := ParseFlutterwaveInbound(noRef); err == nil || errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("missing reference: want hard error, got %v", err)
	}

	// Wrong event type refuses to parse as inbound.
	wrongType := []byte(`{"webhook_id":"wh4","type":"transfer.disburse","data":{}}`)
	if _, err := ParseFlutterwaveInbound(wrongType); err == nil {
		t.Fatal("wrong type accepted")
	}
}

func TestParseFlutterwavePayout(t *testing.T) {
	// v4 transfer ids are string references ("trf_…"), not numbers — using the
	// real shape here is what guards the regression where fwTransferData.ID was
	// json.Number and every transfer.disburse webhook failed to parse.
	mk := func(evType, status string) []byte {
		return []byte(`{"webhook_id":"wh_p","type":"` + evType + `","data":{"id":"trf_ujFmOq7yqNtsQIF1tKiGP","reference":"3f2b8c9e-0000-0000-0000-000000000000","status":"` + status + `","amount":500,"currency":"NGN"}}`)
	}
	cases := []struct {
		evType, status, want string
	}{
		{"transfer.disburse", "SUCCESSFUL", "success"},
		{"transfer.disburse", "FAILED", "failed"},
		{"transfer.disburse", "PENDING", "pending"},
		{"transfer.disburse", "NEW", "pending"},
		{"transfer.reversal", "SUCCESSFUL", "reversed"},
	}
	for _, c := range cases {
		p, err := ParseFlutterwavePayout(mk(c.evType, c.status))
		if err != nil {
			t.Fatalf("%s/%s: %v", c.evType, c.status, err)
		}
		if p.Status != c.want {
			t.Fatalf("%s/%s: status %q want %q", c.evType, c.status, p.Status, c.want)
		}
		if p.PayoutID != "trf_ujFmOq7yqNtsQIF1tKiGP" || p.MerchantTxRef != "3f2b8c9e-0000-0000-0000-000000000000" {
			t.Fatalf("%s/%s: keys wrong: %+v", c.evType, c.status, p)
		}
	}
	if _, err := ParseFlutterwavePayout([]byte(`{"type":"transfer.disburse","data":{}}`)); err == nil {
		t.Fatal("missing webhook_id accepted")
	}
}

func TestClassifyFlutterwaveEvent(t *testing.T) {
	et, id, err := ClassifyFlutterwaveEvent([]byte(`{"webhook_id":"w1","type":"Charge.Completed","data":{}}`))
	if err != nil || et != "charge.completed" || id != "w1" {
		t.Fatalf("classify: %q %q %v", et, id, err)
	}
	if _, _, err := ClassifyFlutterwaveEvent([]byte(`{"type":"x"}`)); err == nil {
		t.Fatal("missing webhook_id accepted")
	}
}

// ─── Client (httptest) ─────────────────────────────────────────────────────

// newFWTestServer stands up a mux that serves both the IdP token endpoint and
// the API endpoints, returning a client pointed at it.
func newFWTestServer(t *testing.T, mux *http.ServeMux) (*FlutterwaveClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewFlutterwaveClient(srv.URL, srv.URL, "cid", "csecret")
	if c == nil {
		t.Fatal("client nil despite creds")
	}
	return c, srv
}

func fwTokenHandler(tokenCalls *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(tokenCalls, 1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, "bad token request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok_abc", "expires_in": 600})
	}
}

func TestFlutterwaveClientBanksAndTokenCache(t *testing.T) {
	var tokenCalls, bankCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/banks", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bankCalls, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer tok_abc" {
			t.Errorf("missing bearer, got %q", got)
		}
		if r.Header.Get("X-Trace-Id") == "" {
			t.Error("missing X-Trace-Id")
		}
		if r.URL.Query().Get("country") != "NG" {
			t.Errorf("country = %q", r.URL.Query().Get("country"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": []map[string]any{
				{"id": 1, "code": "044", "name": "Access Bank"},
				{"id": 2, "code": "", "name": "Broken Row"},
				{"id": 3, "code": "058", "name": "GTBank"},
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	banks, err := c.ListBanks(context.Background(), "NG")
	if err != nil {
		t.Fatalf("banks: %v", err)
	}
	if len(banks) != 2 || banks[0].Code != "044" || banks[1].Name != "GTBank" {
		t.Fatalf("banks wrong: %+v", banks)
	}
	// Second call must hit both caches (token + banks).
	if _, err := c.ListBanks(context.Background(), "NG"); err != nil {
		t.Fatalf("banks cached: %v", err)
	}
	if atomic.LoadInt32(&bankCalls) != 1 {
		t.Fatalf("bank endpoint called %d times, want 1 (cache)", bankCalls)
	}
	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (cache)", tokenCalls)
	}
}

func TestFlutterwaveResolveAccountNotFound(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/banks/account-resolve", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status":"failed","message":"account not found"}`, http.StatusNotFound)
	})
	c, _ := newFWTestServer(t, mux)
	if _, err := c.ResolveAccount(context.Background(), "NGN", "044", "0690000031"); !errors.Is(err, ErrFWAccountNotFound) {
		t.Fatalf("want ErrFWAccountNotFound, got %v", err)
	}
}

func TestFlutterwaveDirectTransferAndConvert(t *testing.T) {
	var tokenCalls int32
	var gotBody map[string]any
	var gotIdem string
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/direct-transfers", func(w http.ResponseWriter, r *http.Request) {
		gotIdem = r.Header.Get("X-Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"id": "trf_777", "reference": gotBody["reference"], "status": "NEW"},
		})
	})
	c, _ := newFWTestServer(t, mux)

	// Bank payout via the gateway (the NombaGateway seam).
	g := NewFlutterwaveGateway(c, "TSA Connect")
	payoutID, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode:      "044",
		AccountNumber: "0690000031",
		AmountNGN:     "1500.00",
		Reference:     "trade-ref-123456",
		AccountName:   "Ada Obi",
	})
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	if payoutID != "trf_777" {
		t.Fatalf("payout id %q", payoutID)
	}
	if gotIdem != "trade-ref-123456" {
		t.Fatalf("idempotency key %q, want the trade reference", gotIdem)
	}
	pi := gotBody["payment_instruction"].(map[string]any)
	if pi["source_currency"] != "NGN" || pi["destination_currency"] != "NGN" {
		t.Fatalf("currencies wrong: %+v", pi)
	}
	rec := pi["recipient"].(map[string]any)
	if rec["bank"].(map[string]any)["code"] != "044" {
		t.Fatalf("recipient wrong: %+v", rec)
	}

	// Own-balance conversion (USDT → NGN) targets our own merchant id.
	if _, err := c.ConvertBalance(context.Background(), "100744667", "USDT", "NGN", FWAmount{Value: 10, AppliesTo: "source_currency"}, "conv-ref-000001"); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if gotBody["type"] != "wallet" {
		t.Fatalf("convert type %v, want wallet", gotBody["type"])
	}
	pi = gotBody["payment_instruction"].(map[string]any)
	if pi["recipient"].(map[string]any)["wallet"].(map[string]any)["identifier"] != "100744667" {
		t.Fatalf("convert recipient wrong: %+v", pi)
	}

	// Invalid amount is rejected before any HTTP call.
	if _, err := g.InitiatePayout(context.Background(), PayoutRequest{AmountNGN: "abc", Reference: "r-000000000001"}); err == nil {
		t.Fatal("invalid amount accepted")
	}
}

func TestFlutterwaveTransferFailedEnvelope(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/direct-transfers", func(w http.ResponseWriter, r *http.Request) {
		// 200 with a failed body — the belt-and-braces path.
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "failed", "message": "insufficient balance", "data": map[string]any{}})
	})
	c, _ := newFWTestServer(t, mux)
	_, err := c.DirectTransfer(context.Background(), FWDirectTransferRequest{Reference: "ref-000000000001"})
	if err == nil || !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("want failed-envelope error, got %v", err)
	}
}

func TestFlutterwaveTokenRefreshOn401(t *testing.T) {
	var tokenCalls int32
	first := true
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/banks", func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []map[string]any{{"id": 1, "code": "044", "name": "Access"}}})
	})
	c, _ := newFWTestServer(t, mux)
	if _, err := c.ListBanks(context.Background(), "NG"); err != nil {
		t.Fatalf("banks after 401 retry: %v", err)
	}
	if atomic.LoadInt32(&tokenCalls) != 2 {
		t.Fatalf("token calls = %d, want 2 (refresh on 401)", tokenCalls)
	}
}

func TestFWIdempotencyKeyWindow(t *testing.T) {
	if k := fwIdempotencyKey("short"); len(k) < 12 {
		t.Fatalf("short key not padded: %q", k)
	}
	long := strings.Repeat("x", 300)
	if k := fwIdempotencyKey(long); len(k) != 255 {
		t.Fatalf("long key not truncated: %d", len(k))
	}
	if k := fwIdempotencyKey("exactly-12-c"); k != "exactly-12-c" {
		t.Fatalf("in-window key changed: %q", k)
	}
}

// A 404 from /charges is Flutterwave's authoritative "this charge does not
// exist", not a failure to reach it. It must be distinguishable so the retry
// job can age it out instead of re-driving it forever; every other non-2xx
// stays an ordinary (retryable) error.
func TestGetChargeNotFoundIsDistinguishable(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/charges/chg_missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "failed",
			"error":  map[string]any{"type": "CHARGE_NOT_FOUND", "message": "Charge not found"},
		})
	})
	mux.HandleFunc("/charges/chg_boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "failed", "message": "server error"})
	})
	c, _ := newFWTestServer(t, mux)

	_, err := c.GetCharge(context.Background(), "chg_missing")
	if err == nil {
		t.Fatal("want an error for a 404 charge")
	}
	if !errors.Is(err, ErrFWChargeNotFound) {
		t.Errorf("404 did not surface ErrFWChargeNotFound: %v", err)
	}

	_, err = c.GetCharge(context.Background(), "chg_boom")
	if err == nil {
		t.Fatal("want an error for a 500")
	}
	if errors.Is(err, ErrFWChargeNotFound) {
		t.Errorf("a 500 must NOT be treated as charge-not-found: %v", err)
	}
}

// ─── FWAmount (production: /transfers/rates strings) ───────────────────────

// fwRatesHandler stands up a /transfers/rates handler that always answers
// with body, regardless of the requested pair — good enough to pin a decode
// shape without caring what QuoteRate's caller asked for.
func fwRatesHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}
}

// TestQuoteRate_DecodesStringNumbersFromProduction pins the exact shape
// production Flutterwave sends: 2026-08-31, "rate" AND both "amount" fields
// arrived as JSON strings, not numbers as the sandbox returns, and broke the
// whole decode with "cannot unmarshal string into Go struct field". Both the
// sell probe shape (source=asset, destination=fiat — QuoteStableSell's call)
// and the buy probe shape (source=fiat, destination=asset — QuoteStableBuy's
// call) must decode and derive the same rate a numeric wire would give.
func TestQuoteRate_DecodesStringNumbersFromProduction(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", fwRatesHandler(`{
		"status": "success",
		"data": {
			"rate": "0.000707",
			"source": {"currency": "USDC", "amount": "1"},
			"destination": {"currency": "NGN", "amount": "1650.25"}
		}
	}`))
	c, _ := newFWTestServer(t, mux)

	rate, err := c.QuoteRate(context.Background(), "USDC", "NGN", 1650.25)
	if err != nil {
		t.Fatalf("QuoteRate: %v", err)
	}
	if rate.Source.Amount.Float64() != 1 || rate.Destination.Amount.Float64() != 1650.25 {
		t.Fatalf("decoded amounts wrong: source=%v destination=%v",
			rate.Source.Amount.Float64(), rate.Destination.Amount.Float64())
	}

	// Sell-shaped: source is the asset, so DeriveFiatPerUnit (dst/src) applies.
	sell, err := DeriveFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("DeriveFiatPerUnit: %v", err)
	}
	if want := new(big.Rat).SetFloat64(1650.25); sell.Cmp(want) != 0 {
		t.Fatalf("DeriveFiatPerUnit = %s, want %s", sell.FloatString(6), want.FloatString(6))
	}

	// Buy-shaped: source is the fiat, destination the asset, so
	// DeriveBuyFiatPerUnit (src/dst) applies to a mirror-image response.
	buyRate := FWRate{
		Source:      FWMoney{Currency: "NGN", Amount: rate.Destination.Amount},
		Destination: FWMoney{Currency: "USDC", Amount: rate.Source.Amount},
	}
	buy, err := DeriveBuyFiatPerUnit(buyRate)
	if err != nil {
		t.Fatalf("DeriveBuyFiatPerUnit: %v", err)
	}
	if buy.Cmp(sell) != 0 {
		t.Fatalf("DeriveBuyFiatPerUnit = %s, want %s (same underlying rate, mirrored)",
			buy.FloatString(6), sell.FloatString(6))
	}
}

// TestQuoteRate_DecodesNumericWireNoRegression pins the sandbox's numeric
// wire shape still decodes, so the tolerant type doesn't regress the
// previously-working case.
func TestQuoteRate_DecodesNumericWireNoRegression(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", fwRatesHandler(`{
		"status": "success",
		"data": {
			"rate": 0.000707,
			"source": {"currency": "USDC", "amount": 1},
			"destination": {"currency": "NGN", "amount": 1650.25}
		}
	}`))
	c, _ := newFWTestServer(t, mux)

	rate, err := c.QuoteRate(context.Background(), "USDC", "NGN", 1650.25)
	if err != nil {
		t.Fatalf("QuoteRate: %v", err)
	}
	got, err := DeriveFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("DeriveFiatPerUnit: %v", err)
	}
	if want := new(big.Rat).SetFloat64(1650.25); got.Cmp(want) != 0 {
		t.Fatalf("DeriveFiatPerUnit = %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

// TestQuoteRate_DecodesMixedStringAndNumericAmounts pins the mixed shape —
// string rate, numeric amounts — which is what production appears to send
// on at least some responses; the two amounts must not need to agree on
// representation with each other or with rate.
func TestQuoteRate_DecodesMixedStringAndNumericAmounts(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", fwRatesHandler(`{
		"status": "success",
		"data": {
			"rate": "0.000707",
			"source": {"currency": "USDC", "amount": 1},
			"destination": {"currency": "NGN", "amount": 1650.25}
		}
	}`))
	c, _ := newFWTestServer(t, mux)

	rate, err := c.QuoteRate(context.Background(), "USDC", "NGN", 1650.25)
	if err != nil {
		t.Fatalf("QuoteRate: %v", err)
	}
	got, err := DeriveFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("DeriveFiatPerUnit: %v", err)
	}
	if want := new(big.Rat).SetFloat64(1650.25); got.Cmp(want) != 0 {
		t.Fatalf("DeriveFiatPerUnit = %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

// TestFWQuoteAmount_UnmarshalJSON exercises the type directly for the wire
// shapes DeriveFiatPerUnit/ErrBadFxProbe depend on: null and "" must yield a
// usable zero (never an error — a missing optional amount must not fail the
// whole response), while a non-numeric string must error rather than
// silently becoming a zero that ErrBadFxProbe's guard would then miss.
func TestFWQuoteAmount_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		wire    string
		want    float64
		wantErr bool
	}{
		{"number", `1650.25`, 1650.25, false},
		{"numeric string", `"1650.25"`, 1650.25, false},
		{"null", `null`, 0, false},
		{"empty string", `""`, 0, false},
		{"whitespace string", `"  "`, 0, false},
		{"garbage string", `"abc"`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a FWQuoteAmount
			err := json.Unmarshal([]byte(tc.wire), &a)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("wire %q: want error, got amount=%v", tc.wire, a.Float64())
				}
				return
			}
			if err != nil {
				t.Fatalf("wire %q: unexpected error: %v", tc.wire, err)
			}
			if a.Float64() != tc.want {
				t.Fatalf("wire %q: got %v, want %v", tc.wire, a.Float64(), tc.want)
			}
		})
	}
}

// TestQuoteRate_NullAmountDoesNotFailTheWholeQuote pins that a null amount on
// one side decodes to zero rather than failing the decode outright — the
// zero is then what ErrBadFxProbe's positive-amount guard catches downstream,
// not a decode error masking that guard's job.
func TestQuoteRate_NullAmountDoesNotFailTheWholeQuote(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"rate": "0.000707",
				"source": {"currency": "NGN", "amount": null},
				"destination": {"currency": "USDC", "amount": ""}
			}
		}`))
	})
	c, _ := newFWTestServer(t, mux)

	rate, err := c.QuoteRate(context.Background(), "NGN", "USDC", 1)
	if err != nil {
		t.Fatalf("QuoteRate: %v", err)
	}
	if rate.Source.Amount.Float64() != 0 || rate.Destination.Amount.Float64() != 0 {
		t.Fatalf("null/empty amounts should decode to zero, got source=%v destination=%v",
			rate.Source.Amount.Float64(), rate.Destination.Amount.Float64())
	}
	if _, err := DeriveFiatPerUnit(rate); !errors.Is(err, ErrBadFxProbe) {
		t.Fatalf("DeriveFiatPerUnit on a zero probe: want ErrBadFxProbe, got %v", err)
	}
}

// TestQuoteRate_UnparseableAmountStringIsAnError pins that a genuinely
// unparseable amount fails the decode loudly rather than being coerced to a
// zero that would then read as a merely-implausible (not corrupt) quote.
func TestQuoteRate_UnparseableAmountStringIsAnError(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"rate": "0.000707",
				"source": {"currency": "NGN", "amount": "abc"},
				"destination": {"currency": "USDC", "amount": "1"}
			}
		}`))
	})
	c, _ := newFWTestServer(t, mux)

	if _, err := c.QuoteRate(context.Background(), "NGN", "USDC", 1); err == nil {
		t.Fatal("QuoteRate: want an error for an unparseable amount string, got nil")
	}
}

// ─── FWFeeAmount / GetFees ──────────────────────────────────────────────────

// TestFWFeeAmount_UnmarshalJSON exercises the type directly for the wire
// shapes GetFees depends on: the historical scalar/numeric-string/null shape
// must keep working unchanged, the production array shape (inferred: numeric
// elements summed — see FWFeeAmount's doc comment) must decode instead of
// failing the whole /fees response, and anything genuinely unparseable
// (garbage string, or an array holding a non-numeric element such as an
// object) must error rather than silently becoming a zero fee.
func TestFWFeeAmount_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		wire    string
		want    float64
		wantErr bool
	}{
		{"scalar number (legacy shape)", `53.75`, 53.75, false},
		{"numeric string", `"53.75"`, 53.75, false},
		{"null", `null`, 0, false},
		{"empty string", `""`, 0, false},
		{"array of numbers (production shape, inferred)", `[10, 5.5]`, 15.5, false},
		{"array of numeric strings", `["10", "5.5"]`, 15.5, false},
		{"single-element array", `[53.75]`, 53.75, false},
		{"empty array", `[]`, 0, false},
		{"garbage string", `"abc"`, 0, true},
		{"array with non-numeric element", `[10, {"amount": 5}]`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a FWFeeAmount
			err := json.Unmarshal([]byte(tc.wire), &a)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("wire %q: want error, got amount=%v", tc.wire, a.Float64())
				}
				return
			}
			if err != nil {
				t.Fatalf("wire %q: unexpected error: %v", tc.wire, err)
			}
			if a.Float64() != tc.want {
				t.Fatalf("wire %q: got %v, want %v", tc.wire, a.Float64(), tc.want)
			}
		})
	}
}

// TestGetFees_ScalarFeeShape pins the historical shape (fee/vat as bare
// numbers) still decodes exactly as before — no regression from adding array
// support.
func TestGetFees_ScalarFeeShape(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/fees", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {"currency": "NGN", "fee": 53.75, "vat": 4.03}
		}`))
	})
	c, _ := newFWTestServer(t, mux)

	fees, err := c.GetFees(context.Background(), 1000, "NGN", "bank_transfer", "", "NG")
	if err != nil {
		t.Fatalf("GetFees: %v", err)
	}
	if len(fees) != 1 || fees[0].Fee.Float64() != 53.75 || fees[0].VAT.Float64() != 4.03 {
		t.Fatalf("fees wrong: %+v", fees)
	}
}

// TestGetFees_ArrayFeeShape pins the fix for the production bug: Flutterwave
// production was observed returning "fee" as a JSON array (see FWFeeAmount's
// doc comment for what was and wasn't directly observed), which previously
// failed decode outright with "cannot unmarshal array into Go struct field
// FWFee.fee of type float64" — silently falling back to the uncalibrated
// config estimate on every live payout quote. It must now decode.
func TestGetFees_ArrayFeeShape(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/fees", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {"currency": "NGN", "fee": [45, 8.75], "vat": [4.03]}
		}`))
	})
	c, _ := newFWTestServer(t, mux)

	fees, err := c.GetFees(context.Background(), 1000, "NGN", "bank_transfer", "", "NG")
	if err != nil {
		t.Fatalf("GetFees: %v", err)
	}
	if len(fees) != 1 || fees[0].Fee.Float64() != 53.75 || fees[0].VAT.Float64() != 4.03 {
		t.Fatalf("fees wrong: %+v", fees)
	}
}

// TestGetFees_NullFee pins that a null fee decodes to a usable zero rather
// than failing the response, matching FWQuoteAmount's null handling.
func TestGetFees_NullFee(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/fees", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {"currency": "NGN", "fee": null, "vat": ""}
		}`))
	})
	c, _ := newFWTestServer(t, mux)

	fees, err := c.GetFees(context.Background(), 1000, "NGN", "bank_transfer", "", "NG")
	if err != nil {
		t.Fatalf("GetFees: %v", err)
	}
	if len(fees) != 1 || fees[0].Fee.Float64() != 0 || fees[0].VAT.Float64() != 0 {
		t.Fatalf("fees wrong: %+v", fees)
	}
}

// TestGetFees_UnparseableFeeIsAnError pins that a fee value which cannot be
// interpreted as a number at all (not scalar, not numeric string, not a
// numeric array) fails the decode rather than silently becoming a zero fee —
// a zero provider fee is a plausible-looking number that would under-charge
// a live payout.
func TestGetFees_UnparseableFeeIsAnError(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/fees", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {"currency": "NGN", "fee": "abc", "vat": 0}
		}`))
	})
	c, _ := newFWTestServer(t, mux)

	if _, err := c.GetFees(context.Background(), 1000, "NGN", "bank_transfer", "", "NG"); err == nil {
		t.Fatal("GetFees: want an error for an unparseable fee, got nil")
	}
}
