package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Fixtures are verbatim slices of live responses captured on 2026-09-03/04, so
// these tests fail if Paycrest changes a shape we depend on.

func paycrestStub(t *testing.T, status int, body string) (*PaycrestClient, *http.Request) {
	t.Helper()
	var seen *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		seen = clone
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewPaycrestClient(srv.URL, "test-key"), seen
}

func TestPaycrestClient_DormantWithoutKey(t *testing.T) {
	if c := NewPaycrestClient("https://example.test", ""); c != nil {
		t.Fatalf("expected nil client with no API key, got %#v", c)
	}
	var nilClient *PaycrestClient
	if nilClient.IsEnabled() {
		t.Fatal("IsEnabled must be false on a nil receiver")
	}
	if _, err := nilClient.GetOrder(context.Background(), "x"); !errors.Is(err, ErrPaycrestDisabled) {
		t.Fatalf("want ErrPaycrestDisabled, got %v", err)
	}
}

func TestPaycrestRates_ParsesBothSides(t *testing.T) {
	c, _ := paycrestStub(t, 200, `{"status":"success","message":"Rate fetched successfully","data":{
	  "buy":{"rate":"1381.23","providerIds":["etoMCRIY"],"orderType":"regular","refundTimeoutMinutes":2},
	  "sell":{"rate":"1366.05","providerIds":["WYjUQLBk"],"orderType":"regular","refundTimeoutMinutes":2}}}`)

	got, err := c.Rates(context.Background(), "base", "usdt", 100, "ngn")
	if err != nil {
		t.Fatalf("Rates: %v", err)
	}
	if got.Buy == nil || got.Buy.Rate != "1381.23" {
		t.Fatalf("buy rate: %#v", got.Buy)
	}
	if got.Sell == nil || got.Sell.Rate != "1366.05" {
		t.Fatalf("sell rate: %#v", got.Sell)
	}
	if len(got.Buy.ProviderIDs) != 1 || got.Buy.ProviderIDs[0] != "etoMCRIY" {
		t.Fatalf("provider ids: %#v", got.Buy.ProviderIDs)
	}
}

// A one-sided book is the normal way "onramp unavailable" presents — measured
// on 2026-09-04 when every USDT-on-Base buy provider left. It must decode
// cleanly with a nil Buy rather than erroring.
func TestPaycrestRates_OneSidedBookIsNotAnError(t *testing.T) {
	c, _ := paycrestStub(t, 200, `{"status":"success","message":"ok","data":{
	  "sell":{"rate":"1366.05","providerIds":["WYjUQLBk"],"orderType":"regular"}}}`)

	got, err := c.Rates(context.Background(), "base", "USDT", 1, "NGN")
	if err != nil {
		t.Fatalf("Rates: %v", err)
	}
	if got.Buy != nil {
		t.Fatalf("expected no buy side, got %#v", got.Buy)
	}
	if got.Sell == nil {
		t.Fatal("expected the sell side to survive")
	}
}

func TestPaycrestRates_NoProviderIsASentinel(t *testing.T) {
	c, _ := paycrestStub(t, 404, `{"status":"error","message":"no provider available for USDT to NGN conversion with amount 50000 on base","data":null}`)

	_, err := c.Rates(context.Background(), "base", "USDT", 50000, "NGN")
	if !errors.Is(err, ErrPaycrestNoProvider) {
		t.Fatalf("want ErrPaycrestNoProvider so callers can offer another venue, got %v", err)
	}
}

func TestPaycrestRates_RejectsScientificNotation(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	}))
	defer srv.Close()

	c := NewPaycrestClient(srv.URL, "k")
	if _, err := c.Rates(context.Background(), "base", "USDC", 1000000, "NGN"); err != nil {
		t.Fatalf("Rates: %v", err)
	}
	if strings.Contains(path, "e+") {
		t.Fatalf("amount rendered in scientific notation, path=%s", path)
	}
	if !strings.Contains(path, "1000000") {
		t.Fatalf("amount missing from path %s", path)
	}
}

// The API rejects a missing refund account with the opaque "Refund institution
// not supported". We refuse before spending a round trip.
func TestPaycrestOnramp_RequiresRefundAccount(t *testing.T) {
	c, _ := paycrestStub(t, 200, `{}`)

	_, err := c.CreateOnrampOrder(context.Background(), PaycrestOnrampRequest{
		AmountNGN:        "1000",
		Token:            "USDC",
		Network:          "base",
		RecipientAddress: "0xabc",
		Reference:        "ref-1",
	})
	if err == nil || !strings.Contains(err.Error(), "refund account") {
		t.Fatalf("want a refund-account validation error, got %v", err)
	}
}

func TestPaycrestOnramp_SendsExpectedPayload(t *testing.T) {
	var body []byte
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		authHeader = r.Header.Get("API-Key")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"status":"success","message":"Payment order initiated successfully","data":{
		  "id":"3365f79c","status":"pending","reference":"ref-1","amount":"0.7236","rate":"1382.02",
		  "senderFee":"0.0036","senderFeePercent":"0.5","transactionFee":"0",
		  "providerAccount":{"institution":"Nombank MFB","accountIdentifier":"6618322068",
		    "accountName":"5ran6/EMMANUEL ADEMU","validUntil":"2026-09-03T10:59:42Z",
		    "amountToTransfer":"1004.98","currency":"NGN"}}}`))
	}))
	defer srv.Close()

	c := NewPaycrestClient(srv.URL, "test-key")
	order, err := c.CreateOnrampOrder(context.Background(), PaycrestOnrampRequest{
		AmountNGN:        "1000",
		Token:            "USDC",
		Network:          "base",
		RecipientAddress: "0xabc",
		Reference:        "ref-1",
		RefundAccount: PaycrestBankAccount{
			Institution:       "OPAYNGPC",
			AccountIdentifier: "8068157741",
			AccountName:       "EMMANUEL ADEMU",
		},
	})
	if err != nil {
		t.Fatalf("CreateOnrampOrder: %v", err)
	}
	if authHeader != "test-key" {
		t.Fatalf("API-Key header not sent, got %q", authHeader)
	}
	sent := string(body)
	for _, want := range []string{`"amountIn":"fiat"`, `"refundAccount"`, `"OPAYNGPC"`, `"reference":"ref-1"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("payload missing %s\ngot: %s", want, sent)
		}
	}
	if order.ProviderAccount == nil || order.ProviderAccount.AmountToTransfer != "1004.98" {
		t.Fatalf("provider account: %#v", order.ProviderAccount)
	}
	// The payer owes MORE than the order amount — the sender fee rides on top.
	if order.Amount == order.ProviderAccount.AmountToTransfer {
		t.Error("amountToTransfer should include the sender fee, not equal the order amount")
	}
}

// A 400's useful text is nested inside data, not in message.
func TestPaycrestError_SurfacesValidationDetail(t *testing.T) {
	c, _ := paycrestStub(t, 400, `{"status":"error","message":"Failed to validate payload","data":{"field":"Source","message":"Refund institution not supported"}}`)

	_, err := c.GetOrder(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Refund institution not supported") {
		t.Fatalf("validation detail was swallowed: %v", err)
	}
}

func TestPaycrestGetOrder_NotFoundIsASentinel(t *testing.T) {
	c, _ := paycrestStub(t, 404, `{"status":"error","message":"not found","data":null}`)
	if _, err := c.GetOrder(context.Background(), "missing"); !errors.Is(err, ErrPaycrestOrderNotFound) {
		t.Fatalf("want ErrPaycrestOrderNotFound, got %v", err)
	}
}

// The live failure of 2026-09-03: paid in full, inside the window, bank
// confirmed — yet the order expired having never registered the deposit. There
// is no API recovery, so detecting it must be unambiguous.
func TestPaycrestOrder_UnmatchedDeposit(t *testing.T) {
	cases := []struct {
		name  string
		order PaycrestOrder
		want  bool
	}{
		{"expired with nothing registered", PaycrestOrder{Status: "expired", AmountPaid: "0"}, true},
		{"expired with empty amountPaid", PaycrestOrder{Status: "expired", AmountPaid: ""}, true},
		{"expired but the deposit landed", PaycrestOrder{Status: "expired", AmountPaid: "1004.98"}, false},
		{"settled", PaycrestOrder{Status: "settled", AmountPaid: "804.03"}, false},
		{"still pending", PaycrestOrder{Status: "pending", AmountPaid: "0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.order.UnmatchedDeposit(); got != tc.want {
				t.Fatalf("UnmatchedDeposit() = %v, want %v", got, tc.want)
			}
		})
	}
	var nilOrder *PaycrestOrder
	if nilOrder.UnmatchedDeposit() {
		t.Fatal("nil order must not report an unmatched deposit")
	}
}

func TestPaycrestTerminal(t *testing.T) {
	for _, s := range []string{"settled", "expired", "refunded", "cancelled", "SETTLED"} {
		if !PaycrestTerminal(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []string{"pending", "settling", ""} {
		if PaycrestTerminal(s) {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

// ─── Live smoke test ───────────────────────────────────────────────────────

// Public endpoints only — no key, no order, no money. Set PAYCREST_LIVE=1.
// Guards against the shapes drifting under us between releases.
func TestLive_PaycrestPublicEndpoints(t *testing.T) {
	if os.Getenv("PAYCREST_LIVE") == "" {
		t.Skip("set PAYCREST_LIVE=1 to hit the real Paycrest API (read-only)")
	}
	c := NewPaycrestClient("", "smoke-test-no-auth-needed")
	ctx := context.Background()

	mkt, err := c.MarketRate(ctx, "NGN", "buy")
	if err != nil {
		t.Fatalf("MarketRate: %v", err)
	}
	t.Logf("NGN market buy rate: %.2f", mkt)
	if mkt < 100 || mkt > 100000 {
		t.Errorf("NGN market rate %.2f is outside any plausible range", mkt)
	}

	insts, err := c.Institutions(ctx, "NGN")
	if err != nil {
		t.Fatalf("Institutions: %v", err)
	}
	t.Logf("NGN institutions: %d", len(insts))
	var sawOPay bool
	for _, i := range insts {
		if i.Code == "OPAYNGPC" {
			sawOPay = true
		}
	}
	if !sawOPay {
		t.Error("OPAYNGPC missing — institution codes may have changed")
	}

	// Quote at a size we know is served, and report the spread.
	r, err := c.Rates(ctx, "base", "USDC", 1, "NGN")
	switch {
	case errors.Is(err, ErrPaycrestNoProvider):
		t.Log("no provider for 1 USDC on base right now — that is a real state, not a failure")
	case err != nil:
		t.Fatalf("Rates: %v", err)
	default:
		if r.Buy != nil {
			t.Logf("buy %s (providers %v)", r.Buy.Rate, r.Buy.ProviderIDs)
		}
		if r.Sell != nil {
			t.Logf("sell %s (providers %v)", r.Sell.Rate, r.Sell.ProviderIDs)
		}
	}
}
