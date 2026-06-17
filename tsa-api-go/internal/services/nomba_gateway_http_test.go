package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newNombaTestServer creates a test HTTP server that handles both the Nomba token
// endpoint (/v1/auth/token/issue) and the transfer endpoint (/v2/transfers/bank).
// transferHandler is called for the transfer endpoint only; it receives the
// already-decoded request body for assertions.
func newNombaTestServer(t *testing.T, transferHandler func(w http.ResponseWriter, body map[string]interface{})) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Token endpoint — return a fake token so NombaClient.ensureToken succeeds.
	mux.HandleFunc("/v1/auth/token/issue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"data": {
				"access_token": "fake-test-token",
				"expires_in": 3600
			}
		}`)
	})

	// Transfer endpoint — delegate to the caller-supplied handler.
	mux.HandleFunc("/v2/transfers/bank", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		transferHandler(w, body)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTestGateway constructs a NombaHTTPGateway pointed at the supplied test server URL.
func newTestGateway(serverURL string) *NombaHTTPGateway {
	client := NewNombaClient(serverURL, "test-client-id", "test-client-secret", "test-account-id")
	return NewNombaHTTPGateway(client)
}

func TestNombaHTTPGateway_InitiatePayout_Success(t *testing.T) {
	var capturedBody map[string]interface{}

	srv := newNombaTestServer(t, func(w http.ResponseWriter, body map[string]interface{}) {
		capturedBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"message": "Success",
			"status": true,
			"data": {
				"amount": "1500.0",
				"id": "API-TRANSFER-x",
				"status": "SUCCESS",
				"type": "transfer",
				"fee": 50,
				"timeCreated": "2026-06-16T10:00:00Z",
				"meta": {}
			}
		}`)
	})

	gw := newTestGateway(srv.URL)
	payoutID, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "058",
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		AmountNGN:     "1500",
		Reference:     "trade-uuid-001",
		SenderName:    "TSA Connect",
	})
	if err != nil {
		t.Fatalf("InitiatePayout error: %v", err)
	}
	if payoutID != "API-TRANSFER-x" {
		t.Errorf("payoutID = %q; want %q", payoutID, "API-TRANSFER-x")
	}

	// Assert the request body carried the correct fields.
	if capturedBody == nil {
		t.Fatal("no request body captured")
	}
	if amt, ok := capturedBody["amount"].(float64); !ok || amt != 1500.0 {
		t.Errorf("amount = %v; want 1500.0", capturedBody["amount"])
	}
	if v := capturedBody["accountNumber"]; v != "0123456789" {
		t.Errorf("accountNumber = %v; want 0123456789", v)
	}
	if v := capturedBody["bankCode"]; v != "058" {
		t.Errorf("bankCode = %v; want 058", v)
	}
	if v := capturedBody["merchantTxRef"]; v != "trade-uuid-001" {
		t.Errorf("merchantTxRef = %v; want trade-uuid-001", v)
	}
	if v := capturedBody["accountName"]; v != "JOHN DOE" {
		t.Errorf("accountName = %v; want JOHN DOE", v)
	}
	if v := capturedBody["senderName"]; v != "TSA Connect" {
		t.Errorf("senderName = %v; want TSA Connect", v)
	}
}

func TestNombaHTTPGateway_PendingBilling(t *testing.T) {
	srv := newNombaTestServer(t, func(w http.ResponseWriter, _ map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "PENDING",
			"message": "Pending",
			"status": true,
			"data": {
				"amount": "1500.0",
				"id": "API-TRANSFER-pending",
				"status": "PENDING_BILLING",
				"type": "transfer",
				"fee": 0,
				"timeCreated": "2026-06-16T10:00:00Z",
				"meta": {}
			}
		}`)
	})

	gw := newTestGateway(srv.URL)
	payoutID, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "044",
		AccountNumber: "9876543210",
		AccountName:   "JANE DOE",
		AmountNGN:     "1500.00",
		Reference:     "trade-uuid-002",
		SenderName:    "TSA Connect",
	})
	if err != nil {
		t.Fatalf("PENDING_BILLING should be accepted, got error: %v", err)
	}
	if payoutID != "API-TRANSFER-pending" {
		t.Errorf("payoutID = %q; want %q", payoutID, "API-TRANSFER-pending")
	}
}

func TestNombaHTTPGateway_Error(t *testing.T) {
	srv := newNombaTestServer(t, func(w http.ResponseWriter, _ map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{
			"code": "400",
			"description": "Bad Request",
			"message": "invalid bank code",
			"status": false,
			"data": {}
		}`)
	})

	gw := newTestGateway(srv.URL)
	_, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "XXX",
		AccountNumber: "0000000000",
		AccountName:   "NOBODY",
		AmountNGN:     "100",
		Reference:     "trade-uuid-003",
		SenderName:    "TSA Connect",
	})
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "invalid bank code") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaHTTPGateway_EmptyId(t *testing.T) {
	// Nomba returns 200 + status:true but data.id is empty — gateway must reject this.
	srv := newNombaTestServer(t, func(w http.ResponseWriter, _ map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"message": "Success",
			"status": true,
			"data": {
				"amount": "1500.0",
				"id": "",
				"status": "SUCCESS",
				"type": "transfer",
				"fee": 50,
				"timeCreated": "2026-06-16T10:00:00Z",
				"meta": {}
			}
		}`)
	})

	gw := newTestGateway(srv.URL)
	_, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "058",
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		AmountNGN:     "1500",
		Reference:     "trade-uuid-004",
		SenderName:    "TSA Connect",
	})
	if err == nil {
		t.Fatal("expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("error should mention empty id; got: %v", err)
	}
}

func TestNombaHTTPGateway_StatusFalseBadRequest(t *testing.T) {
	// Some Nomba errors come back as 200 HTTP status but status:false in the body.
	srv := newNombaTestServer(t, func(w http.ResponseWriter, _ map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "400",
			"description": "Insufficient funds",
			"message": "insufficient balance",
			"status": false,
			"data": {}
		}`)
	})

	gw := newTestGateway(srv.URL)
	_, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "058",
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		AmountNGN:     "99999999",
		Reference:     "trade-uuid-005",
		SenderName:    "TSA Connect",
	})
	if err == nil {
		t.Fatal("expected error for status:false, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient balance") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaHTTPGateway_BadAmount(t *testing.T) {
	// Non-numeric AmountNGN should be caught before hitting the network.
	// No test server needed — the error is returned before the HTTP call.
	client := NewNombaClient("http://127.0.0.1:0", "cid", "csecret", "acct")
	gw := NewNombaHTTPGateway(client)
	_, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "058",
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		AmountNGN:     "not-a-number",
		Reference:     "trade-uuid-006",
		SenderName:    "TSA Connect",
	})
	if err == nil {
		t.Fatal("expected error for non-numeric amount, got nil")
	}
	if !strings.Contains(err.Error(), "bad payout amount") {
		t.Errorf("error should mention bad payout amount; got: %v", err)
	}
}

// TestNombaHTTPGateway_SubAccountRouting_WithSubAccountID verifies that when
// SubAccountID is set the gateway calls TransferFromSubAccount (accountId header
// = sub-account id), not the parent Transfer.
func TestNombaHTTPGateway_SubAccountRouting_WithSubAccountID(t *testing.T) {
	var capturedAccountID string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/token/issue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"data": {
				"access_token": "fake-test-token",
				"expires_in": 3600
			}
		}`)
	})
	mux.HandleFunc("/v2/transfers/bank", func(w http.ResponseWriter, r *http.Request) {
		capturedAccountID = r.Header.Get("accountId")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"message": "Success",
			"status": true,
			"data": {
				"amount": "2000.0",
				"id": "API-TRANSFER-sub-gw",
				"status": "SUCCESS",
				"type": "transfer",
				"fee": 50,
				"timeCreated": "2026-06-17T10:00:00Z",
				"meta": {}
			}
		}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	gw := newTestGateway(srv.URL)
	payoutID, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "058",
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		AmountNGN:     "2000",
		Reference:     "trade-uuid-sub-001",
		SenderName:    "PM Merchant Ltd",
		SubAccountID:  "sub-acct-pm-001",
	})
	if err != nil {
		t.Fatalf("InitiatePayout (sub-account): %v", err)
	}
	if payoutID != "API-TRANSFER-sub-gw" {
		t.Errorf("payoutID = %q; want API-TRANSFER-sub-gw", payoutID)
	}
	// The accountId header must be the sub-account id, not the parent "test-account-id".
	if capturedAccountID != "sub-acct-pm-001" {
		t.Errorf("accountId header = %q; want sub-acct-pm-001 (sub-account path)", capturedAccountID)
	}
}

// TestNombaHTTPGateway_SubAccountRouting_NoSubAccountID verifies that when
// SubAccountID is empty the gateway uses the parent Transfer (accountId header
// stays as the parent account id, not a sub-account).
func TestNombaHTTPGateway_SubAccountRouting_NoSubAccountID(t *testing.T) {
	var capturedAccountID string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/token/issue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"data": {
				"access_token": "fake-test-token",
				"expires_in": 3600
			}
		}`)
	})
	mux.HandleFunc("/v2/transfers/bank", func(w http.ResponseWriter, r *http.Request) {
		capturedAccountID = r.Header.Get("accountId")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"code": "200",
			"description": "SUCCESS",
			"message": "Success",
			"status": true,
			"data": {
				"amount": "3000.0",
				"id": "API-TRANSFER-parent",
				"status": "SUCCESS",
				"type": "transfer",
				"fee": 50,
				"timeCreated": "2026-06-17T10:00:00Z",
				"meta": {}
			}
		}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	gw := newTestGateway(srv.URL)
	payoutID, err := gw.InitiatePayout(t.Context(), PayoutRequest{
		BankCode:      "044",
		AccountNumber: "9876543210",
		AccountName:   "JANE DOE",
		AmountNGN:     "3000",
		Reference:     "trade-uuid-parent-001",
		SenderName:    "TSA Connect",
		// SubAccountID intentionally empty → parent path
	})
	if err != nil {
		t.Fatalf("InitiatePayout (parent): %v", err)
	}
	if payoutID != "API-TRANSFER-parent" {
		t.Errorf("payoutID = %q; want API-TRANSFER-parent", payoutID)
	}
	// The accountId header must be the parent account id ("test-account-id"),
	// NOT a sub-account id.
	if capturedAccountID != "test-account-id" {
		t.Errorf("accountId header = %q; want test-account-id (parent path)", capturedAccountID)
	}
}
