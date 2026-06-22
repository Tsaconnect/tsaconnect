package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newNombaSubAccountServer creates a test HTTP server mocking /v1/auth/token/issue
// and the VA/balance endpoints. Each endpoint handler is caller-supplied.
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

// ─── CreateVirtualAccount (VA-only model) ──────────────────────────────────────

func TestNombaClient_CreateVirtualAccount_Success(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s; want POST", r.Method)
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &capturedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"bankAccountNumber": "9012345678",
					"bankName": "Sterling Bank",
					"bankAccountName": "TSA-Acme Corp",
					"accountRef": "pm-abc-123"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	acctNo, err := c.CreateVirtualAccount(t.Context(), "pm-abc-123", "Acme Corp")
	if err != nil {
		t.Fatalf("CreateVirtualAccount error: %v", err)
	}
	if acctNo != "9012345678" {
		t.Errorf("bankAccountNumber = %q; want %q", acctNo, "9012345678")
	}
	// The body must carry accountRef + accountName (sandbox-validated shape).
	if capturedBody["accountRef"] != "pm-abc-123" {
		t.Errorf("accountRef = %v; want pm-abc-123", capturedBody["accountRef"])
	}
	if capturedBody["accountName"] != "Acme Corp" {
		t.Errorf("accountName = %v; want Acme Corp", capturedBody["accountName"])
	}
}

func TestNombaClient_CreateVirtualAccount_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "duplicate accountRef",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.CreateVirtualAccount(t.Context(), "pm-abc-123", "Acme Corp")
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate accountRef") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_CreateVirtualAccount_StatusFalse(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "VA creation failed",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.CreateVirtualAccount(t.Context(), "pm-abc-123", "Acme Corp")
	if err == nil {
		t.Fatal("expected error for status:false, got nil")
	}
	if !strings.Contains(err.Error(), "VA creation failed") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

// Regression: Nomba (sandbox especially) returns status:false even on a
// SUCCESSFUL virtual-account create — it signals success via code "00" + a
// populated bankAccountNumber, the same quirk the token endpoint exhibits.
// The client must trust the account number we got back, not the `status` flag,
// or every PM provisioning wrongly fails after Nomba already created the VA.
func TestNombaClient_CreateVirtualAccount_SuccessWithStatusFalse(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "00",
				"description": "Successful",
				"status": false,
				"data": {
					"bankAccountNumber": "1234567890",
					"bankName": "Sandbox Bank",
					"bankAccountName": "TSA-Test PM",
					"accountRef": "pm-abc-123"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	acctNo, err := c.CreateVirtualAccount(t.Context(), "pm-abc-123", "Test PM")
	if err != nil {
		t.Fatalf("CreateVirtualAccount error: %v", err)
	}
	if acctNo != "1234567890" {
		t.Errorf("bankAccountNumber = %q; want 1234567890", acctNo)
	}
}

func TestNombaClient_CreateVirtualAccount_EmptyNumber(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/virtual": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "200",
				"description": "SUCCESS",
				"status": true,
				"data": {
					"bankAccountNumber": "",
					"accountRef": "pm-abc-123"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.CreateVirtualAccount(t.Context(), "pm-abc-123", "Acme Corp")
	if err == nil {
		t.Fatal("expected error for empty bankAccountNumber, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty bankAccountNumber; got: %v", err)
	}
}

// ─── FetchParentBalance ────────────────────────────────────────────────────────

func TestNombaClient_FetchParentBalance_Success(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/balance": func(w http.ResponseWriter, r *http.Request) {
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
					"amount": "150000.50",
					"currency": "NGN"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	ngn, err := c.FetchParentBalance(t.Context())
	if err != nil {
		t.Fatalf("FetchParentBalance error: %v", err)
	}
	if ngn != "150000.50" {
		t.Errorf("balance = %q, want 150000.50", ngn)
	}
}

func TestNombaClient_FetchParentBalance_SuccessWithStatusFalse(t *testing.T) {
	// Regression: a successful balance read can come back status:false (code
	// "00") — the reconciler must read the amount, not treat it as a failure
	// (which would raise false drift alerts).
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/balance": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "00",
				"description": "Successful",
				"status": false,
				"data": {
					"amount": "150000.50",
					"currency": "NGN"
				}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	ngn, err := c.FetchParentBalance(t.Context())
	if err != nil {
		t.Fatalf("FetchParentBalance error: %v", err)
	}
	if ngn != "150000.50" {
		t.Errorf("balance = %q, want 150000.50", ngn)
	}
}

func TestNombaClient_FetchParentBalance_NonOK(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/balance": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{
				"code": "500",
				"message": "balance unavailable",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.FetchParentBalance(t.Context())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "balance unavailable") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}

func TestNombaClient_FetchParentBalance_StatusFalse(t *testing.T) {
	srv := newNombaSubAccountServer(t, map[string]http.HandlerFunc{
		"/v1/accounts/balance": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{
				"code": "400",
				"message": "balance fetch failed",
				"status": false,
				"data": {}
			}`)
		},
	})

	c := newTestNombaClient(srv.URL)
	_, err := c.FetchParentBalance(t.Context())
	if err == nil {
		t.Fatal("expected error for status:false, got nil")
	}
	if !strings.Contains(err.Error(), "balance fetch failed") {
		t.Errorf("error should contain Nomba's message; got: %v", err)
	}
}
