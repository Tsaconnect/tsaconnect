package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// *FlutterwaveVAProvisioner must satisfy NombaProvisioner so it's a drop-in for
// the Nomba client in PMOnboardingService — the contract that lets PM onboarding
// run on Flutterwave.
var _ NombaProvisioner = (*FlutterwaveVAProvisioner)(nil)

func TestFlutterwavePMProvisioner_CreatesStaticVA(t *testing.T) {
	var tokenCalls int32
	var gotBody map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/virtual-accounts", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"account_number":   "9988776655",
				"account_bank_name": "Wema Bank",
				"reference":         gotBody["reference"],
				"status":            "active",
			},
		})
	})
	c, _ := newFWTestServer(t, mux)

	prov := NewFlutterwaveVAProvisioner(c, "cust_company_1")
	if prov == nil {
		t.Fatal("provisioner nil despite client + customer id")
	}

	acctNo, err := prov.CreateVirtualAccount(context.Background(), "pm-abc-123", "Acme Corp")
	if err != nil {
		t.Fatalf("CreateVirtualAccount: %v", err)
	}
	if acctNo != "9988776655" {
		t.Fatalf("account number = %q, want 9988776655", acctNo)
	}

	// The crux: a PM VA must be STATIC (permanent, amount 0) and carry the PM's
	// deterministic reference — that's the attribution key the inbound matcher
	// reads. A dynamic/amount-locked VA would never match a PM inbound.
	if gotBody["account_type"] != "static" {
		t.Fatalf("account_type = %v, want static", gotBody["account_type"])
	}
	if amt, _ := gotBody["amount"].(float64); amt != 0 {
		t.Fatalf("amount = %v, want 0 (static VAs are not amount-locked)", gotBody["amount"])
	}
	if gotBody["reference"] != "pm-abc-123" {
		t.Fatalf("reference = %v, want pm-abc-123 (the inbound attribution key)", gotBody["reference"])
	}
	if gotBody["customer_id"] != "cust_company_1" {
		t.Fatalf("customer_id = %v, want the company customer", gotBody["customer_id"])
	}
	if gotBody["currency"] != "NGN" {
		t.Fatalf("currency = %v, want NGN", gotBody["currency"])
	}
}

func TestFlutterwavePMProvisioner_DormantWithoutCustomerID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(new(int32)))
	c, _ := newFWTestServer(t, mux)

	// No company customer id → nil provisioner (dormant), matching the IT path.
	if p := NewFlutterwaveVAProvisioner(c, ""); p != nil {
		t.Fatal("expected nil provisioner when FLUTTERWAVE_CUSTOMER_ID is empty")
	}
}
