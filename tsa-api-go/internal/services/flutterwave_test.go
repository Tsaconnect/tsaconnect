package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	mk := func(evType, status string) []byte {
		return []byte(`{"webhook_id":"wh_p","type":"` + evType + `","data":{"id":98765,"reference":"3f2b8c9e-0000-0000-0000-000000000000","status":"` + status + `","amount":500,"currency":"NGN"}}`)
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
		if p.PayoutID != "98765" || p.MerchantTxRef != "3f2b8c9e-0000-0000-0000-000000000000" {
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
