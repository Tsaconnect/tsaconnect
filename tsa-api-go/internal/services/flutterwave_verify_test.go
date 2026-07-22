package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// fakeChargeGetter is an injectable FlutterwaveChargeGetter.
type fakeChargeGetter struct {
	ch     FWCharge
	err    error
	calls  int
	lastID string
}

func (f *fakeChargeGetter) GetCharge(_ context.Context, id string) (FWCharge, error) {
	f.calls++
	f.lastID = id
	return f.ch, f.err
}

// (fakeITHooks — with a `credited []string` of "ref|amount" — is defined in
// it_automation_test.go and reused here.)

// inboundFor builds an InboundPayment mirroring what ParseFlutterwaveInbound
// produces for a charge.completed webhook.
func inboundFor(chargeID, ref, amountNGN string) *InboundPayment {
	return &InboundPayment{
		EventID:          "wbk_1",
		AccountRef:       ref,
		AmountNGN:        amountNGN,
		Currency:         "NGN",
		Status:           "settled",
		ProviderChargeID: chargeID,
	}
}

func TestVerifyFlutterwaveCharge_HappyPath(t *testing.T) {
	g := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "NGN", Status: "succeeded"}}
	amt, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "1500.00"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if amt != "1500.00" {
		t.Errorf("amount = %q, want 1500.00", amt)
	}
	if g.lastID != "chg_1" {
		t.Errorf("fetched id = %q, want chg_1", g.lastID)
	}
}

func TestVerifyFlutterwaveCharge_MissingChargeID(t *testing.T) {
	g := &fakeChargeGetter{}
	_, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("", "IT-abc", "1500.00"))
	// A charge.completed with no id is malformed and won't self-heal → terminal.
	if !errors.Is(err, ErrChargeRejected) {
		t.Fatalf("err = %v, want ErrChargeRejected", err)
	}
	if g.calls != 0 {
		t.Errorf("GetCharge called %d times with no id, want 0", g.calls)
	}
}

func TestVerifyFlutterwaveCharge_APIErrorIsRetryable(t *testing.T) {
	apiErr := errors.New("flutterwave get-charge: HTTP 503")
	g := &fakeChargeGetter{err: apiErr}
	_, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "1500.00"))
	if err == nil {
		t.Fatal("expected error")
	}
	// A transport error must NOT be classified as a terminal rejection —
	// it is retryable, so callers keep retrying rather than alarming.
	if errors.Is(err, ErrChargeRejected) {
		t.Fatalf("api error wrongly classified as terminal rejection: %v", err)
	}
	if !errors.Is(err, apiErr) {
		t.Fatalf("api error not wrapped: %v", err)
	}
}

func TestVerifyFlutterwaveCharge_TerminalStatusesRejected(t *testing.T) {
	// Known dead-end statuses will never become succeeded → terminal.
	for _, status := range []string{"failed", "cancelled", "reversed", "expired"} {
		g := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "NGN", Status: status}}
		_, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "1500.00"))
		if !errors.Is(err, ErrChargeRejected) {
			t.Errorf("status %q: err = %v, want ErrChargeRejected", status, err)
		}
	}
}

func TestVerifyFlutterwaveCharge_PendingIsRetryable(t *testing.T) {
	// Not-yet-succeeded (and unknown) statuses must be retryable, NOT terminal —
	// terminating them would strand a charge that later settles (money lost).
	for _, status := range []string{"pending", "processing", "", "some_new_status"} {
		g := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "NGN", Status: status}}
		_, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "1500.00"))
		if !errors.Is(err, ErrChargePending) {
			t.Errorf("status %q: err = %v, want ErrChargePending (retryable)", status, err)
		}
		if errors.Is(err, ErrChargeRejected) {
			t.Errorf("status %q: must not be terminal", status)
		}
	}
}

func TestVerifyFlutterwaveCharge_NonNGNRejected(t *testing.T) {
	g := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "USD", Status: "succeeded"}}
	_, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "1500.00"))
	if !errors.Is(err, ErrChargeRejected) {
		t.Fatalf("err = %v, want ErrChargeRejected", err)
	}
}

func TestVerifyFlutterwaveCharge_ReferenceMismatchRejected(t *testing.T) {
	// The API says the money belongs to a DIFFERENT reference — crediting our
	// webhook's reference would apply funds to the wrong order.
	g := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-someone-else", Amount: "1500", Currency: "NGN", Status: "succeeded"}}
	_, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "1500.00"))
	if !errors.Is(err, ErrChargeRejected) {
		t.Fatalf("err = %v, want ErrChargeRejected", err)
	}
}

func TestVerifyFlutterwaveCharge_AmountDivergenceCreditsAPIAmount(t *testing.T) {
	// Webhook claims 9999 but the API (source of truth) says 1500 — we credit
	// the API amount, not the misleading webhook amount.
	g := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "NGN", Status: "succeeded"}}
	amt, err := verifyFlutterwaveCharge(context.Background(), g, inboundFor("chg_1", "IT-abc", "9999.00"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if amt != "1500.00" {
		t.Errorf("credited amount = %q, want 1500.00 (api source of truth)", amt)
	}
}

// ─── DispatchFlutterwave integration (IT path, no DB) ──────────────────────────

const chargeBody = `{"webhook_id":"wbk_1","type":"charge.completed","data":{"id":"chg_1","reference":"IT-abc","amount":9999,"currency":"NGN","status":"succeeded"}}`

func newDispatchSvc(it ITWebhookHooks, cg FlutterwaveChargeGetter) *PMAutomationService {
	s := NewPMAutomationService(nil, nil, nil, nil, nil, nil, "")
	if it != nil {
		s.SetITHooks(it)
	}
	if cg != nil {
		s.SetFlutterwaveChargeVerifier(cg)
	}
	return s
}

func TestDispatchFlutterwave_ITCreditsVerifiedAPIAmount(t *testing.T) {
	it := &fakeITHooks{}
	// Webhook says 9999; API says 1500 → credit must be the API's 1500.
	cg := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "NGN", Status: "succeeded"}}
	svc := newDispatchSvc(it, cg)

	if err := svc.DispatchFlutterwave(context.Background(), []byte(chargeBody)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Credited exactly once, with the API amount (1500) — not the webhook's 9999.
	if len(it.credited) != 1 || it.credited[0] != "IT-abc|1500.00" {
		t.Fatalf("credited = %+v, want [IT-abc|1500.00] (API amount, not webhook's 9999)", it.credited)
	}
}

func TestDispatchFlutterwave_TerminalRejectionDoesNotCredit(t *testing.T) {
	it := &fakeITHooks{}
	// API reference contradicts the webhook → terminal rejection, never credited.
	cg := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-other", Amount: "1500", Currency: "NGN", Status: "succeeded"}}
	svc := newDispatchSvc(it, cg)

	err := svc.DispatchFlutterwave(context.Background(), []byte(chargeBody))
	if !errors.Is(err, ErrChargeRejected) {
		t.Fatalf("err = %v, want ErrChargeRejected", err)
	}
	if len(it.credited) != 0 {
		t.Errorf("credited despite terminal rejection: %+v", it.credited)
	}
}

func TestDispatchFlutterwave_PendingIsRetryableNoCredit(t *testing.T) {
	it := &fakeITHooks{}
	cg := &fakeChargeGetter{ch: FWCharge{ID: "chg_1", Reference: "IT-abc", Amount: "1500", Currency: "NGN", Status: "pending"}}
	svc := newDispatchSvc(it, cg)

	err := svc.DispatchFlutterwave(context.Background(), []byte(chargeBody))
	// Pending is retryable — NOT terminal — and must not credit yet.
	if !errors.Is(err, ErrChargePending) {
		t.Fatalf("err = %v, want ErrChargePending", err)
	}
	if errors.Is(err, ErrChargeRejected) {
		t.Errorf("pending must not be terminal")
	}
	if len(it.credited) != 0 {
		t.Errorf("credited a not-yet-succeeded charge: %+v", it.credited)
	}
}

func TestDispatchFlutterwave_FailClosedWithoutVerifier(t *testing.T) {
	it := &fakeITHooks{}
	svc := newDispatchSvc(it, nil) // no verifier wired

	err := svc.DispatchFlutterwave(context.Background(), []byte(chargeBody))
	if err == nil {
		t.Fatal("expected fail-closed error with no verifier")
	}
	if len(it.credited) != 0 {
		t.Errorf("credited with no verifier configured: %+v", it.credited)
	}
}

func TestDispatchFlutterwave_APIErrorRetryableNoCredit(t *testing.T) {
	it := &fakeITHooks{}
	cg := &fakeChargeGetter{err: errors.New("HTTP 503")}
	svc := newDispatchSvc(it, cg)

	err := svc.DispatchFlutterwave(context.Background(), []byte(chargeBody))
	if err == nil {
		t.Fatal("expected error on API failure")
	}
	if errors.Is(err, ErrChargeRejected) {
		t.Errorf("transient API error must stay retryable, not a terminal rejection: %v", err)
	}
	if len(it.credited) != 0 {
		t.Errorf("credited despite unverified charge: %+v", it.credited)
	}
}

// ─── FlutterwaveClient.GetCharge HTTP round-trip ──────────────────────────────

func TestFlutterwaveClient_GetCharge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(new(int32)))
	mux.HandleFunc("/charges/chg_zH0BLoNltt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"message": "Charge fetched",
			"data": map[string]any{
				"id":        "chg_zH0BLoNltt",
				"reference": "IT-abc",
				"amount":    1500,
				"currency":  "NGN",
				"status":    "succeeded",
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	ch, err := c.GetCharge(context.Background(), "chg_zH0BLoNltt")
	if err != nil {
		t.Fatalf("GetCharge: %v", err)
	}
	if ch.ID != "chg_zH0BLoNltt" || ch.Reference != "IT-abc" || ch.Currency != "NGN" || ch.Status != "succeeded" {
		t.Fatalf("charge decoded wrong: %+v", ch)
	}
	if ch.Amount.String() != "1500" {
		t.Errorf("amount = %q, want 1500", ch.Amount.String())
	}
}

func TestFlutterwaveClient_GetCharge_EmptyID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(new(int32)))
	c, _ := newFWTestServer(t, mux)
	if _, err := c.GetCharge(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty charge id")
	}
}
