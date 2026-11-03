package services

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// allowQuoteCurrency temporarily adds a currency to the FX-quote allowlist so
// quote-math tests can exercise QuoteWithdrawal for a currency the product
// catalog may not offer.
//
// Cleanup RESTORES the prior state rather than deleting. The delete was safe
// only while the catalog was NGN-only and the allowlist was therefore empty:
// once a currency is genuinely in the catalog, an unconditional delete removes
// it permanently from a package-level map, so every later test in the package
// sees a corrupted allowlist. That failure is order-dependent — the test passes
// in isolation and fails in a full run — which is the worst way to find it.
func allowQuoteCurrency(t *testing.T, currency string) {
	t.Helper()
	prev, existed := SupportedWithdrawalQuoteCurrencies[currency]
	SupportedWithdrawalQuoteCurrencies[currency] = true
	t.Cleanup(func() {
		if existed {
			SupportedWithdrawalQuoteCurrencies[currency] = prev
			return
		}
		delete(SupportedWithdrawalQuoteCurrencies, currency)
	})
}

// TestQuoteWithdrawal pins the direction-robust derivation (see it_quote.go):
// FW's /transfers/rates is destination-amount shaped; instead of trusting the
// ambiguous top-level "rate" field, QuoteWithdrawal derives
// ngnPerDestUnit = Source.Amount / Destination.Amount from the probed 1-unit
// destination amount, then destAmount = ngnAmount / ngnPerDestUnit.
func TestQuoteWithdrawal(t *testing.T) {
	allowQuoteCurrency(t, "GHS")
	var tokenCalls, rateCalls int32
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rateCalls, 1)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"rate":        150.0,
				"source":      map[string]any{"currency": "NGN", "amount": 150.0},
				"destination": map[string]any{"currency": "GHS", "amount": 1.0},
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	svc := NewITService(nil, nil, nil)
	svc.SetRateQuoter(c)

	destAmount, rate, err := svc.QuoteWithdrawal(context.Background(), 15000, "GHS")
	if err != nil {
		t.Fatalf("QuoteWithdrawal: %v", err)
	}
	if rate != 150 {
		t.Fatalf("rate = %v, want 150", rate)
	}
	if destAmount != 100 {
		t.Fatalf("destAmount = %v, want 100 (15000 NGN / 150)", destAmount)
	}
	if rateCalls != 1 {
		t.Fatalf("rateCalls = %d, want 1", rateCalls)
	}

	// Pin the request shape: source currency is NGN, destination is the
	// requested currency, probed with a 1-unit destination amount.
	if gotBody["source"].(map[string]any)["currency"] != "NGN" {
		t.Fatalf("source currency wrong: %+v", gotBody)
	}
	dest := gotBody["destination"].(map[string]any)
	if dest["currency"] != "GHS" {
		t.Fatalf("destination currency wrong: %+v", dest)
	}
	if dest["amount"] != float64(1) {
		t.Fatalf("destination probe amount = %v, want 1", dest["amount"])
	}
}

// TestQuoteWithdrawal_NoQuoter confirms the dormant-by-default posture: with
// no live FX source wired, QuoteWithdrawal fails clearly instead of guessing.
func TestQuoteWithdrawal_NoQuoter(t *testing.T) {
	allowQuoteCurrency(t, "GHS")
	svc := NewITService(nil, nil, nil)
	if _, _, err := svc.QuoteWithdrawal(context.Background(), 15000, "GHS"); err != ErrITQuoteUnavailable {
		t.Fatalf("err = %v, want ErrITQuoteUnavailable", err)
	}
}

// TestQuoteWithdrawal_ZeroAmounts confirms QuoteWithdrawal refuses to derive a
// rate from a degenerate FW response (zero/negative Source or Destination
// amount) instead of dividing by zero or returning a bogus quote.
func TestQuoteWithdrawal_ZeroAmounts(t *testing.T) {
	allowQuoteCurrency(t, "GHS")
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"rate":        150.0,
				"source":      map[string]any{"currency": "NGN", "amount": 0.0},
				"destination": map[string]any{"currency": "GHS", "amount": 1.0},
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	svc := NewITService(nil, nil, nil)
	svc.SetRateQuoter(c)

	if _, _, err := svc.QuoteWithdrawal(context.Background(), 15000, "GHS"); err != ErrITQuoteUnavailable {
		t.Fatalf("err = %v, want ErrITQuoteUnavailable", err)
	}
}

// TestQuoteWithdrawal_UnsupportedCurrency confirms QuoteWithdrawal rejects a
// destination currency outside the allowlist BEFORE ever calling Flutterwave
// (no wasted live probe).
//
// The currencies here are ones the catalog does NOT offer. USD and GHS moved
// out of this list when they became catalog entries — the behaviour under test
// is the gate, not any particular currency, so it has to be exercised with
// something still outside it.
func TestQuoteWithdrawal_UnsupportedCurrency(t *testing.T) {
	var tokenCalls, rateCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rateCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"rate":        1.0,
				"source":      map[string]any{"currency": "NGN", "amount": 1.0},
				"destination": map[string]any{"currency": "USD", "amount": 1.0},
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	svc := NewITService(nil, nil, nil)
	svc.SetRateQuoter(c)

	for _, currency := range []string{"XOF", "UGX", "CAD"} {
		if _, _, err := svc.QuoteWithdrawal(context.Background(), 15000, currency); err != ErrUnsupportedWithdrawalCurrency {
			t.Fatalf("%s: err = %v, want ErrUnsupportedWithdrawalCurrency", currency, err)
		}
	}
	if rateCalls != 0 {
		t.Fatalf("rateCalls = %d, want 0 (FW must not be called for an unsupported currency)", rateCalls)
	}
}

// TestQuoteWithdrawal_CacheHit confirms the per-currency TTL cache: two
// QuoteWithdrawal calls for the same destination currency within the TTL
// reuse the cached rate instead of re-probing Flutterwave, and different NGN
// amounts still derive correct destAmounts off the shared cached rate.
func TestQuoteWithdrawal_CacheHit(t *testing.T) {
	allowQuoteCurrency(t, "GHS")
	var tokenCalls, rateCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers/rates", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rateCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"rate":        150.0,
				"source":      map[string]any{"currency": "NGN", "amount": 150.0},
				"destination": map[string]any{"currency": "GHS", "amount": 1.0},
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	svc := NewITService(nil, nil, nil)
	svc.SetRateQuoter(c)

	destAmount1, rate1, err := svc.QuoteWithdrawal(context.Background(), 15000, "GHS")
	if err != nil {
		t.Fatalf("QuoteWithdrawal #1: %v", err)
	}
	destAmount2, rate2, err := svc.QuoteWithdrawal(context.Background(), 30000, "GHS")
	if err != nil {
		t.Fatalf("QuoteWithdrawal #2: %v", err)
	}

	if rateCalls != 1 {
		t.Fatalf("rateCalls = %d, want 1 (second call should hit the cache)", rateCalls)
	}
	if rate1 != 150 || rate2 != 150 {
		t.Fatalf("rate1=%v rate2=%v, want 150 both", rate1, rate2)
	}
	if destAmount1 != 100 {
		t.Fatalf("destAmount1 = %v, want 100", destAmount1)
	}
	if destAmount2 != 200 {
		t.Fatalf("destAmount2 = %v, want 200 (30000 NGN / 150, cached rate)", destAmount2)
	}
}
