package services

import (
	"context"
	"errors"
	"testing"
)

func TestNairaKoboHelpers(t *testing.T) {
	for _, c := range []struct{ in, wantKobo string }{
		{"500", "50000"},
		{"500.00", "50000"},
		{"500.5", "50050"},
		{"0.01", "1"},
		{"10053.02", "1005302"},
	} {
		got, err := nairaToKobo(c.in)
		if err != nil {
			t.Fatalf("nairaToKobo(%q): %v", c.in, err)
		}
		if got.String() != c.wantKobo {
			t.Fatalf("nairaToKobo(%q) = %s, want %s", c.in, got, c.wantKobo)
		}
	}
	if _, err := nairaToKobo("1.234"); err == nil {
		t.Fatal("3dp accepted")
	}
	if s, err := koboToNairaString("50050"); err != nil || s != "500.50" {
		t.Fatalf("koboToNairaString: %q %v", s, err)
	}
	if s, err := koboToNairaString("7"); err != nil || s != "0.07" {
		t.Fatalf("koboToNairaString small: %q %v", s, err)
	}
	if f, err := koboToNairaFloat("50000"); err != nil || f != 500 {
		t.Fatalf("koboToNairaFloat: %v %v", f, err)
	}
}

// fakeITHooks records routing decisions made by DispatchFlutterwave.
type fakeITHooks struct {
	credited  []string
	resolved  []string
	creditErr error
}

func (f *fakeITHooks) AutoCreditFromInbound(_ context.Context, ref, amount string) error {
	f.credited = append(f.credited, ref+"|"+amount)
	return f.creditErr
}

func (f *fakeITHooks) ResolvePayoutFromWebhook(_ context.Context, ref, status string) error {
	f.resolved = append(f.resolved, ref+"|"+status)
	return nil
}

// The dispatcher must route IT-prefixed references to the IT hooks and NEVER
// into the PM path (the zero-value service would nil-panic there — which is
// exactly what this test would surface on a routing bug).
func TestDispatchFlutterwaveRoutesITRefs(t *testing.T) {
	s := NewPMAutomationService(nil, nil, nil, nil, nil, nil, "")
	hooks := &fakeITHooks{}
	s.SetITHooks(hooks)

	inbound := []byte(`{"webhook_id":"w1","type":"charge.completed","data":{"reference":"IT-ABCDEF12345678","amount":500,"currency":"NGN","status":"succeeded"}}`)
	if err := s.DispatchFlutterwave(context.Background(), inbound); err != nil {
		t.Fatalf("inbound dispatch: %v", err)
	}
	if len(hooks.credited) != 1 || hooks.credited[0] != "IT-ABCDEF12345678|500.00" {
		t.Fatalf("credit routing wrong: %+v", hooks.credited)
	}

	payout := []byte(`{"webhook_id":"w2","type":"transfer.disburse","data":{"id":1,"reference":"IT-SELL-3f2b8c9e-1111-2222-3333-444444444444","status":"SUCCESSFUL"}}`)
	if err := s.DispatchFlutterwave(context.Background(), payout); err != nil {
		t.Fatalf("payout dispatch: %v", err)
	}
	if len(hooks.resolved) != 1 || hooks.resolved[0] != "IT-SELL-3f2b8c9e-1111-2222-3333-444444444444|success" {
		t.Fatalf("payout routing wrong: %+v", hooks.resolved)
	}

	// A failed-charge event is non-settling and must not reach either path.
	failed := []byte(`{"webhook_id":"w3","type":"charge.completed","data":{"reference":"IT-XYZ","amount":1,"currency":"NGN","status":"failed"}}`)
	if err := s.DispatchFlutterwave(context.Background(), failed); !errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("failed charge: want ErrNonSettlingEvent, got %v", err)
	}
	if len(hooks.credited) != 1 {
		t.Fatalf("failed charge reached credit hook: %+v", hooks.credited)
	}
}
