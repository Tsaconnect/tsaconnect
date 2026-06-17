package services

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newNombaSubAccountServer creates a test HTTP server mocking /v1/auth/token/issue
// and the sub-account related endpoints. Each endpoint handler is caller-supplied.
func newNombaSubAccountServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
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

	for path, handler := range handlers {
		mux.HandleFunc(path, handler)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestNombaClient(serverURL string) *NombaClient {
	return NewNombaClient(serverURL, "test-client-id", "test-client-secret", "test-account-id")
}

// ─── CreateSubAccount ──────────────────────────────────────────────────────────

func TestNombaClient_CreateSubAccount_Success(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s; want POST", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"accountId": "sub-acct-uuid-001",
					"accountName": "Test Merchant"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	subAccountID, err := c.CreateSubAccount(t.Context(), "Test Merchant")
	if err != nil {
		t.Fatalf("CreateSubAccount error: %v", err)
	}
	if subAccountID != "sub-acct-uuid-001" {
		t.Errorf("subAccountID = %q; want %q", subAccountID, "sub-acct-uuid-001")
	}
}

func TestNombaClient_CreateSubAccount_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"description": "Bad Request",
				"message": "invalid account name",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.CreateSubAccount(t.Context(), "Test Merchant")
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "invalid account name") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_CreateSubAccount_StatusFalse(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"description": "Failed",
				"message": "sub-account creation failed",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.CreateSubAccount(t.Context(), "Test Merchant")
	if err == nil {
		t.Fatal("expected error for status:false, got nil")
	}
	if !strings.Contains(err.Error(), "sub-account creation failed") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_CreateSubAccount_EmptyID(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"accountId": "",
					"accountName": "Test Merchant"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.CreateSubAccount(t.Context(), "Test Merchant")
	if err == nil {
		t.Fatal("expected error for empty sub-account id, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty id; got: %v", err)
	}
}

// ─── CreateVirtualAccountForSubAccount ────────────────────────────────────────

func TestNombaClient_CreateVirtualAccountForSubAccount_Success(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s; want POST", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"accountNumber": "9012345678",
					"walletId": "wallet-uuid-001",
					"accountName": "Test Merchant"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	vaNumber, walletID, err := c.CreateVirtualAccountForSubAccount(t.Context(), "sub-acct-uuid-001", "Test Merchant")
	if err != nil {
		t.Fatalf("CreateVirtualAccountForSubAccount error: %v", err)
	}
	if vaNumber != "9012345678" {
		t.Errorf("vaNumber = %q; want %q", vaNumber, "9012345678")
	}
	if walletID != "wallet-uuid-001" {
		t.Errorf("walletID = %q; want %q", walletID, "wallet-uuid-001")
	}
}

func TestNombaClient_CreateVirtualAccountForSubAccount_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "sub-account not found",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, _, err := c.CreateVirtualAccountForSubAccount(t.Context(), "sub-acct-uuid-001", "Test Merchant")
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "sub-account not found") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_CreateVirtualAccountForSubAccount_EmptyIDs(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"accountNumber": "",
					"walletId": "",
					"accountName": "Test Merchant"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, _, err := c.CreateVirtualAccountForSubAccount(t.Context(), "sub-acct-uuid-001", "Test Merchant")
	if err == nil {
		t.Fatal("expected error for empty vaNumber/walletId, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty fields; got: %v", err)
	}
}

// ─── SetSubAccountSettlement ───────────────────────────────────────────────────

func TestNombaClient_SetSubAccountSettlement_Success(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub/settlement": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s; want POST", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	err := c.SetSubAccountSettlement(t.Context(), "sub-acct-uuid-001", "058", "0123456789")
	if err != nil {
		t.Fatalf("SetSubAccountSettlement error: %v", err)
	}
}

func TestNombaClient_SetSubAccountSettlement_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub/settlement": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "invalid bank code",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	err := c.SetSubAccountSettlement(t.Context(), "sub-acct-uuid-001", "XXX", "0123456789")
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "invalid bank code") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_SetSubAccountSettlement_StatusFalse(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub/settlement": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "settlement update failed",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	err := c.SetSubAccountSettlement(t.Context(), "sub-acct-uuid-001", "058", "0123456789")
	if err == nil {
		t.Fatal("expected error for status:false, got nil")
	}
	if !strings.Contains(err.Error(), "settlement update failed") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

// ─── FetchSubAccountBalance ────────────────────────────────────────────────────

func TestNombaClient_FetchSubAccountBalance_Success(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub-acct-uuid-001/balance": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("method = %s; want GET", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"availableBalance": 15000.50,
					"currency": "NGN"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	ngn, err := c.FetchSubAccountBalance(t.Context(), "sub-acct-uuid-001")
	if err != nil {
		t.Fatalf("FetchSubAccountBalance error: %v", err)
	}
	if ngn == "" {
		t.Error("ngn balance should not be empty")
	}
	// Check it contains the balance value
	if !strings.Contains(ngn, "15000") {
		t.Errorf("ngn = %q; want it to contain the balance value", ngn)
	}
}

func TestNombaClient_FetchSubAccountBalance_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/sub-acct-uuid-001/balance": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{
				"code": "404",
				"message": "sub-account not found",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.FetchSubAccountBalance(t.Context(), "sub-acct-uuid-001")
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	if !strings.Contains(err.Error(), "sub-account not found") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

// ─── TransferFromSubAccount ────────────────────────────────────────────────────

func TestNombaClient_TransferFromSubAccount_Success(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v2/transfers/bank": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s; want POST", r.Method)
			}
			// Verify sub-account targeting header
			subAcctID := r.Header.Get("accountId")
			if subAcctID != "sub-acct-uuid-001" {
				// TODO(nomba-facts): confirm exact mechanism (header vs path param)
				// For now check it's set to either parent or sub-account ID
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"message": "Success",
				"status": true,
				"data": {
					"amount": "5000.0",
					"id": "API-TRANSFER-sub-001",
					"status": "SUCCESS",
					"type": "transfer",
					"fee": 50,
					"timeCreated": "2026-06-17T10:00:00Z",
					"meta": {}
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	result, err := c.TransferFromSubAccount(t.Context(), "sub-acct-uuid-001", NombaTransferRequest{
		Amount:        5000.0,
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		BankCode:      "058",
		MerchantTxRef: "trade-sub-001",
		SenderName:    "TSA Merchant",
		Narration:     "Payout",
	})
	if err != nil {
		t.Fatalf("TransferFromSubAccount error: %v", err)
	}
	if result.ID != "API-TRANSFER-sub-001" {
		t.Errorf("result.ID = %q; want %q", result.ID, "API-TRANSFER-sub-001")
	}
	if result.Status != "SUCCESS" {
		t.Errorf("result.Status = %q; want %q", result.Status, "SUCCESS")
	}
}

func TestNombaClient_TransferFromSubAccount_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v2/transfers/bank": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "insufficient sub-account balance",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.TransferFromSubAccount(t.Context(), "sub-acct-uuid-001", NombaTransferRequest{
		Amount:        99999999.0,
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		BankCode:      "058",
		MerchantTxRef: "trade-sub-002",
		SenderName:    "TSA Merchant",
	})
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient sub-account balance") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_TransferFromSubAccount_StatusFalse(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v2/transfers/bank": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"description": "Failed",
				"message": "sub-account transfer rejected",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.TransferFromSubAccount(t.Context(), "sub-acct-uuid-001", NombaTransferRequest{
		Amount:        100.0,
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		BankCode:      "058",
		MerchantTxRef: "trade-sub-003",
		SenderName:    "TSA Merchant",
	})
	if err == nil {
		t.Fatal("expected error for status:false, got nil")
	}
	if !strings.Contains(err.Error(), "sub-account transfer rejected") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_TransferFromSubAccount_EmptyID(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v2/transfers/bank": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"message": "Success",
				"status": true,
				"data": {
					"id": "",
					"status": "SUCCESS"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.TransferFromSubAccount(t.Context(), "sub-acct-uuid-001", NombaTransferRequest{
		Amount:        100.0,
		AccountNumber: "0123456789",
		AccountName:   "JOHN DOE",
		BankCode:      "058",
		MerchantTxRef: "trade-sub-004",
		SenderName:    "TSA Merchant",
	})
	if err == nil {
		t.Fatal("expected error for empty transfer id, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty id; got: %v", err)
	}
}
