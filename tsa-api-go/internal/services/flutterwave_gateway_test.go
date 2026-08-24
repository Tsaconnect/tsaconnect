package services

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// fwCaptureTransferServer stands up an FW test server that captures the last
// posted /direct-transfers body so InitiatePayout tests can assert on it.
func fwCaptureTransferServer(t *testing.T) (*FlutterwaveGateway, *map[string]any) {
	t.Helper()
	var tokenCalls int32
	gotBody := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/direct-transfers", func(w http.ResponseWriter, r *http.Request) {
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"id": "trf_777", "reference": gotBody["reference"], "status": "NEW"},
		})
	})
	c, _ := newFWTestServer(t, mux)
	g := NewFlutterwaveGateway(c, "TSA Connect")
	return g, &gotBody
}

// TestFlutterwaveGateway_NGNBankUnchanged pins today's behavior: a
// PayoutRequest with none of the new multi-currency fields set must still
// produce an NGN→NGN bank transfer.
func TestFlutterwaveGateway_NGNBankUnchanged(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	payoutID, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode:      "044",
		AccountNumber: "0690000031",
		AmountNGN:     "1500.00",
		Reference:     "trade-ref-123456",
		AccountName:   "Ada Obi",
	})
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	if payoutID != "trf_777" {
		t.Fatalf("payout id %q", payoutID)
	}

	body := *gotBody
	if body["type"] != "bank" {
		t.Fatalf("type = %v, want bank", body["type"])
	}
	pi := body["payment_instruction"].(map[string]any)
	if pi["source_currency"] != "NGN" || pi["destination_currency"] != "NGN" {
		t.Fatalf("currencies wrong: %+v", pi)
	}
	amt := pi["amount"].(map[string]any)
	if amt["value"] != 1500.0 {
		t.Fatalf("amount value = %v, want 1500", amt["value"])
	}
	if amt["applies_to"] != "destination_currency" {
		t.Fatalf("applies_to = %v, want destination_currency", amt["applies_to"])
	}
	rec := pi["recipient"].(map[string]any)
	bank, ok := rec["bank"].(map[string]any)
	if !ok {
		t.Fatalf("recipient.bank missing: %+v", rec)
	}
	if bank["code"] != "044" || bank["account_number"] != "0690000031" {
		t.Fatalf("bank recipient wrong: %+v", bank)
	}
	if _, hasMM := rec["mobile_money"]; hasMM {
		t.Fatalf("recipient.mobile_money should be absent for bank rail: %+v", rec)
	}
}

// TestFlutterwaveGateway_CrossCurrencyBank exercises the new cross-currency
// path: our NGN converts to a destination African currency via FW's FX, over
// the bank rail.
func TestFlutterwaveGateway_CrossCurrencyBank(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	_, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode:            "044",
		AccountNumber:       "1234567890",
		AmountNGN:           "1500.00",
		Reference:           "trade-ref-cc-000001",
		AccountName:         "Wanjiku Kamau",
		DestinationCurrency: "KES",
		RailType:            "bank",
	})
	if err != nil {
		t.Fatalf("payout: %v", err)
	}

	body := *gotBody
	if body["type"] != "bank" {
		t.Fatalf("type = %v, want bank", body["type"])
	}
	pi := body["payment_instruction"].(map[string]any)
	if pi["source_currency"] != "NGN" {
		t.Fatalf("source_currency = %v, want NGN", pi["source_currency"])
	}
	if pi["destination_currency"] != "KES" {
		t.Fatalf("destination_currency = %v, want KES", pi["destination_currency"])
	}
	amt := pi["amount"].(map[string]any)
	if amt["applies_to"] != "source_currency" {
		t.Fatalf("applies_to = %v, want source_currency", amt["applies_to"])
	}
	rec := pi["recipient"].(map[string]any)
	bank, ok := rec["bank"].(map[string]any)
	if !ok {
		t.Fatalf("recipient.bank missing: %+v", rec)
	}
	if bank["code"] != "044" || bank["account_number"] != "1234567890" {
		t.Fatalf("bank recipient wrong: %+v", bank)
	}
}

// TestFlutterwaveGateway_MobileMoney exercises the mobile-money rail: the
// recipient carries MobileMoney{Network, Country, Msisdn} and NO bank block.
func TestFlutterwaveGateway_MobileMoney(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	_, err := g.InitiatePayout(context.Background(), PayoutRequest{
		AmountNGN:           "1500.00",
		Reference:           "trade-ref-mm-000001",
		AccountName:         "Aminata Diallo",
		DestinationCurrency: "XOF",
		RailType:            "mobile_money",
		MobileNetwork:       "wave",
		MobileCountry:       "SN",
		MobileMsisdn:        "221771234567",
	})
	if err != nil {
		t.Fatalf("payout: %v", err)
	}

	body := *gotBody
	if body["type"] != "mobile_money" {
		t.Fatalf("type = %v, want mobile_money", body["type"])
	}
	pi := body["payment_instruction"].(map[string]any)
	if pi["source_currency"] != "NGN" || pi["destination_currency"] != "XOF" {
		t.Fatalf("currencies wrong: %+v", pi)
	}
	amt := pi["amount"].(map[string]any)
	if amt["applies_to"] != "source_currency" {
		t.Fatalf("applies_to = %v, want source_currency", amt["applies_to"])
	}
	rec := pi["recipient"].(map[string]any)
	mm, ok := rec["mobile_money"].(map[string]any)
	if !ok {
		t.Fatalf("recipient.mobile_money missing: %+v", rec)
	}
	if mm["network"] != "wave" || mm["country"] != "SN" || mm["msisdn"] != "221771234567" {
		t.Fatalf("mobile_money recipient wrong: %+v", mm)
	}
	if _, hasBank := rec["bank"]; hasBank {
		t.Fatalf("recipient.bank should be absent for mobile_money rail: %+v", rec)
	}
}

// TestFlutterwaveGateway_MixedCaseInputsNormalized hardens InitiatePayout
// against a future caller that doesn't pre-normalize: a mixed-case rail and
// lower-case destination currency must still route to the mobile_money rail
// (not silently fall through to bank) and send an upper-case currency code.
func TestFlutterwaveGateway_MixedCaseInputsNormalized(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	_, err := g.InitiatePayout(context.Background(), PayoutRequest{
		AmountNGN:           "1500.00",
		Reference:           "trade-ref-mc-000001",
		AccountName:         "Kwame Mensah",
		DestinationCurrency: "ghs",
		RailType:            "Mobile_Money",
		MobileNetwork:       "mtn",
		MobileCountry:       "GH",
		MobileMsisdn:        "233241234567",
	})
	if err != nil {
		t.Fatalf("payout: %v", err)
	}

	body := *gotBody
	if body["type"] != "mobile_money" {
		t.Fatalf("type = %v, want mobile_money", body["type"])
	}
	pi := body["payment_instruction"].(map[string]any)
	if pi["destination_currency"] != "GHS" {
		t.Fatalf("destination_currency = %v, want GHS", pi["destination_currency"])
	}
	amt := pi["amount"].(map[string]any)
	if amt["applies_to"] != "source_currency" {
		t.Fatalf("applies_to = %v, want source_currency", amt["applies_to"])
	}
	rec := pi["recipient"].(map[string]any)
	mm, ok := rec["mobile_money"].(map[string]any)
	if !ok {
		t.Fatalf("recipient.mobile_money missing: %+v", rec)
	}
	if mm["network"] != "mtn" || mm["country"] != "GH" || mm["msisdn"] != "233241234567" {
		t.Fatalf("mobile_money recipient wrong: %+v", mm)
	}
	if _, hasBank := rec["bank"]; hasBank {
		t.Fatalf("recipient.bank should be absent for mobile_money rail: %+v", rec)
	}
}

// TestFlutterwaveGateway_LowercaseNGNAppliesTo confirms a lower-case "ngn"
// destination currency still maps to the legacy applies_to=destination_currency
// branch (case-insensitive NGN comparison).
func TestFlutterwaveGateway_LowercaseNGNAppliesTo(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	_, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode:            "044",
		AccountNumber:       "0690000031",
		AmountNGN:           "1500.00",
		Reference:           "trade-ref-lc-000001",
		AccountName:         "Ada Obi",
		DestinationCurrency: "ngn",
	})
	if err != nil {
		t.Fatalf("payout: %v", err)
	}

	body := *gotBody
	pi := body["payment_instruction"].(map[string]any)
	if pi["destination_currency"] != "NGN" {
		t.Fatalf("destination_currency = %v, want NGN", pi["destination_currency"])
	}
	amt := pi["amount"].(map[string]any)
	if amt["applies_to"] != "destination_currency" {
		t.Fatalf("applies_to = %v, want destination_currency", amt["applies_to"])
	}
}

// ── Source currency + fee incidence ──────────────────────────────────────────

// TestFlutterwaveGateway_AppliesToFollowsSourceVsDestination pins that fee
// incidence is decided by comparing the two currencies, not by testing for
// naira.
//
// Those coincided while every payout was funded from an NGN balance. Once a
// payout can draw a cedi balance they diverge, and a GHS→GHS transfer must get
// the same exact-amount treatment NGN→NGN gets — the recipient receives exactly
// `value`. A naira literal would silently deny it that, quietly making every
// same-currency Ghanaian payout arrive short by the fee.
func TestFlutterwaveGateway_AppliesToFollowsSourceVsDestination(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		dest       string
		wantSource string
		wantApply  string
	}{
		{"legacy: unset source defaults to NGN", "", "NGN", "NGN", "destination_currency"},
		{"same currency NGN", "NGN", "NGN", "NGN", "destination_currency"},
		{"same currency GHS", "GHS", "GHS", "GHS", "destination_currency"},
		{"cross-currency from NGN", "NGN", "GBP", "NGN", "source_currency"},
		{"cross-currency from GHS", "GHS", "NGN", "GHS", "source_currency"},
		{"case-insensitive match is still same-currency", "ghs", "GHS", "GHS", "destination_currency"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, gotBody := fwCaptureTransferServer(t)

			if _, err := g.InitiatePayout(context.Background(), PayoutRequest{
				BankCode:            "044",
				AccountNumber:       "0690000031",
				Amount:              "1500.00",
				SourceCurrency:      c.source,
				DestinationCurrency: c.dest,
				Reference:           "trade-ref-" + c.name,
				AccountName:         "Ada Obi",
			}); err != nil {
				t.Fatalf("payout: %v", err)
			}

			pi := (*gotBody)["payment_instruction"].(map[string]any)
			if pi["source_currency"] != c.wantSource {
				t.Errorf("source_currency = %v, want %v", pi["source_currency"], c.wantSource)
			}
			amt := pi["amount"].(map[string]any)
			if amt["applies_to"] != c.wantApply {
				t.Errorf("applies_to = %v, want %v — %s", amt["applies_to"], c.wantApply, c.name)
			}
		})
	}
}

// TestFlutterwaveGateway_LegacyAmountNGNStillHonoured confirms the additive
// change did not strand callers that have not migrated to Amount.
func TestFlutterwaveGateway_LegacyAmountNGNStillHonoured(t *testing.T) {
	g, gotBody := fwCaptureTransferServer(t)

	if _, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode:      "044",
		AccountNumber: "0690000031",
		AmountNGN:     "1500.00", // legacy field only
		Reference:     "trade-ref-legacy",
		AccountName:   "Ada Obi",
	}); err != nil {
		t.Fatalf("payout: %v", err)
	}

	pi := (*gotBody)["payment_instruction"].(map[string]any)
	amt := pi["amount"].(map[string]any)
	if amt["value"] != 1500.0 {
		t.Errorf("value = %v, want 1500 from the legacy AmountNGN field", amt["value"])
	}
	if pi["source_currency"] != "NGN" {
		t.Errorf("source_currency = %v, want NGN", pi["source_currency"])
	}
}
