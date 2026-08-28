package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func lifiServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("txHash"); got == "" {
			t.Errorf("no txHash query parameter sent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestLiFiStatus_ParsesADoneTransfer(t *testing.T) {
	srv := lifiServer(t, 200, `{
		"status": "DONE",
		"receiving": {
			"txHash": "0xabc123",
			"chainId": 137,
			"amount": "99884217",
			"token": {"symbol": "USDC", "decimals": 6},
			"toAddress": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
		}
	}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "DONE" {
		t.Errorf("status = %q, want DONE", got.Status)
	}
	if got.DestTxHash != "0xabc123" {
		t.Errorf("destTxHash = %q", got.DestTxHash)
	}
	// The bridge delivers less than was sent; the received amount is what the
	// payout must be computed from, so it has to survive parsing exactly.
	if got.ReceivedBaseUnits.String() != "99884217" {
		t.Errorf("received = %s, want 99884217", got.ReceivedBaseUnits)
	}
	if got.DestChainID != 137 {
		t.Errorf("destChainID = %d, want 137", got.DestChainID)
	}
}

func TestLiFiStatus_PendingIsNotAnError(t *testing.T) {
	srv := lifiServer(t, 200, `{"status":"PENDING"}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "PENDING" {
		t.Fatalf("status = %q, want PENDING", got.Status)
	}
	if got.DestTxHash != "" {
		t.Fatalf("pending transfer reported a destination tx %q", got.DestTxHash)
	}
}

func TestLiFiStatus_SurfacesAFailedTransfer(t *testing.T) {
	srv := lifiServer(t, 200, `{"status":"FAILED"}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got.Status)
	}
}

func TestLiFiStatus_ErrorsOnUpstreamFailure(t *testing.T) {
	srv := lifiServer(t, 500, `{"message":"boom"}`)
	defer srv.Close()

	if _, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc"); err == nil {
		t.Fatal("upstream 500 swallowed")
	}
}

func TestLiFiStatus_SendsTheAPIKeyWhenConfigured(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-lifi-api-key")
		_, _ = w.Write([]byte(`{"status":"PENDING"}`))
	}))
	defer srv.Close()

	if _, err := NewLiFiStatusClient(srv.URL, "secret").Status(context.Background(), "0xsrc"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if seen != "secret" {
		t.Fatalf("x-lifi-api-key = %q, want secret", seen)
	}
}
