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

// Every field the orchestrator and the sell-side reconciler read off one
// status, parsed from one body: a token or a sending amount that silently
// drops to its zero value reads as "no evidence", which on the delivery path
// is indistinguishable from evidence of the wrong thing.
func TestLiFiStatus_ParsesBothSidesOfTheTransfer(t *testing.T) {
	srv := lifiServer(t, 200, `{
		"status": "DONE",
		"substatus": "COMPLETED",
		"transactionId": "0xtxid",
		"fromAddress": "0x1111111111111111111111111111111111111111",
		"sending": {
			"chainId": 8453,
			"amount": "100000000",
			"token": {"address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"}
		},
		"receiving": {
			"txHash": "0xabc123",
			"chainId": 56,
			"amount": "990",
			"token": {"address": "0x0000000000000000000000000000000000000000"},
			"toAddress": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
		}
	}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.TransactionID != "0xtxid" {
		t.Errorf("transactionID = %q", got.TransactionID)
	}
	if got.FromAddress != "0x1111111111111111111111111111111111111111" {
		t.Errorf("fromAddress = %q", got.FromAddress)
	}
	if got.SendingChainID != 8453 {
		t.Errorf("sendingChainID = %d", got.SendingChainID)
	}
	if got.SendingToken != "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" {
		t.Errorf("sendingToken = %q", got.SendingToken)
	}
	if got.SendingAmount == nil || got.SendingAmount.String() != "100000000" {
		t.Errorf("sendingAmount = %v", got.SendingAmount)
	}
	if got.ReceivingToken != "0x0000000000000000000000000000000000000000" {
		t.Errorf("receivingToken = %q", got.ReceivingToken)
	}
	if got.DestChainID != 56 {
		t.Errorf("destChainID = %d", got.DestChainID)
	}
}

// A sending amount LiFi omits (or writes unparseably) must stay nil, not
// become zero: "nothing was sent" is a claim, and this parser has no evidence
// for it.
func TestLiFiStatus_LeavesAnUnparseableSendingAmountNil(t *testing.T) {
	srv := lifiServer(t, 200, `{"status":"PENDING","sending":{"amount":"not-a-number"}}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.SendingAmount != nil {
		t.Fatalf("sendingAmount = %v, want nil", got.SendingAmount)
	}
}
