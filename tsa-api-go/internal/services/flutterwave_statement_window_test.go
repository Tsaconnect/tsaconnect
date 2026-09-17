package services

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Flutterwave refuses an upper bound it reads as tomorrow:
//
//	HTTP 400 REQUEST_NOT_VALID: 'to' cannot be in the future.
//
// The walk used to add a day to the window's end to make it inclusive, which
// for any window ending today asks for tomorrow — so every poll failed and no
// credit was ingested. The fake refuses the same way, from its own clock, so a
// regression fails here instead of in production.
func fwWindowHandler(t *testing.T, seen *url.Values, okBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		*seen = q
		if to := q.Get("to"); to != "" {
			day, err := time.Parse("2006-01-02", to)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"status":"failed","error":{"type":"REQUEST_NOT_VALID","message":"'to' is not a date"}}`))
				return
			}
			today := time.Now().UTC().Truncate(24 * time.Hour)
			if day.After(today) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"status":"failed","error":{"type":"REQUEST_NOT_VALID","message":"'to' cannot be in the future."}}`))
				return
			}
		}
		_, _ = w.Write([]byte(okBody))
	}
}

func newWindowClient(t *testing.T, seen *url.Values) *FlutterwaveClient {
	t.Helper()
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/wallets/statement", fwWindowHandler(t, seen, `{"status":"success","data":[]}`))
	// The transfers listing answers with an object, not a bare array.
	mux.HandleFunc("/transfers", fwWindowHandler(t, seen,
		`{"status":"success","data":{"transfers":[],"cursor":{"next":"","has_more_items":false}}}`))
	c, _ := newFWTestServer(t, mux)
	return c
}

func TestFetchWalletStatement_NeverAsksForAFutureUpperBound(t *testing.T) {
	var seen url.Values
	c := newWindowClient(t, &seen)
	now := time.Now().UTC()

	if _, err := c.FetchWalletStatement(context.Background(), "NGN", now.Add(-24*time.Hour), now); err != nil {
		t.Fatalf("live window: %v", err)
	}
	if got := seen.Get("to"); got != "" {
		t.Errorf("to = %q for a window ending now; want it omitted so the provider reads up to now", got)
	}
	if seen.Get("from") == "" {
		t.Error("from must still bound the window")
	}
}

func TestFetchWalletStatement_KeepsAnInclusiveEndForAPastWindow(t *testing.T) {
	var seen url.Values
	c := newWindowClient(t, &seen)
	now := time.Now().UTC()
	end := now.AddDate(0, 0, -3)

	if _, err := c.FetchWalletStatement(context.Background(), "NGN", now.AddDate(0, 0, -5), end); err != nil {
		t.Fatalf("past window: %v", err)
	}
	// The day after the end, so the end's own day is included.
	if want := end.AddDate(0, 0, 1).Format("2006-01-02"); seen.Get("to") != want {
		t.Errorf("to = %q, want %q", seen.Get("to"), want)
	}
}

func TestFindTransferByReference_NeverAsksForAFutureUpperBound(t *testing.T) {
	var seen url.Values
	c := newWindowClient(t, &seen)
	// The search spans a window around `around`, so a recent payout reaches
	// into the future exactly as the statement walk did.
	if _, _, err := c.FindTransferByReference(context.Background(), "IT-REF-1", time.Now().UTC()); err != nil {
		t.Fatalf("transfer search: %v", err)
	}
	if got := seen.Get("to"); got != "" {
		t.Errorf("to = %q for a search window reaching now; want it omitted", got)
	}
}
