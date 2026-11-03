package services

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func usdDetails() map[string]string {
	return map[string]string{
		"firstName": "Ada", "lastName": "Obi", "bank": "011304478", "accountNumber": "123456789",
		"routingNumber": "011304478", "swiftCode": "irvt us3n", "accountType": "Checking",
		"email": "ada@example.com", "phoneCountryCode": "+1", "phoneNumber": "2125550100",
		"addressLine1": "1 Main St", "city": "Boston", "state": "MA", "postalCode": "02110", "addressCountry": "us",
		"bankId": "2075", "unexpected": "dropped",
	}
}

func mustDest(t *testing.T, currency string) WithdrawalDestination {
	t.Helper()
	d, ok := LookupWithdrawalDestination(currency, "bank", "")
	if !ok {
		t.Fatalf("%s bank is not in the catalog", currency)
	}
	return d
}

// Every catalog currency must be allowed to pay out by the currency registry:
// the registry decides whether money may move at all.
func TestWithdrawalCatalog_EveryCurrencyPayoutEnabled(t *testing.T) {
	enabled := map[string]bool{}
	for _, c := range DefaultCurrencies() {
		enabled[c.Code] = c.PayoutEnabled
	}
	for _, d := range WithdrawalDestinationCatalog {
		if !enabled[d.Currency] {
			t.Errorf("%s is in the withdrawal catalog but not PayoutEnabled in DefaultCurrencies", d.Currency)
		}
	}
}

func TestValidatePayoutDetails_USDCleansAndKeepsOnlyCorridorFields(t *testing.T) {
	got, err := ValidatePayoutDetails(mustDest(t, "USD"), usdDetails())
	if err != nil {
		t.Fatalf("valid USD details refused: %v", err)
	}
	if got["swiftCode"] != "IRVTUS3N" || got["accountType"] != "checking" || got["phoneCountryCode"] != "1" || got["addressCountry"] != "US" {
		t.Fatalf("details not normalised: %+v", got)
	}
	if _, ok := got["unexpected"]; ok {
		t.Fatal("a key the corridor does not ask for was kept")
	}
	if got["bankId"] != "2075" {
		t.Fatal("bankId (branch lookup aid) should survive")
	}
}

func TestValidatePayoutDetails_NamesEveryBadField(t *testing.T) {
	d := usdDetails()
	delete(d, "lastName")
	d["routingNumber"] = "12345"
	d["swiftCode"] = "SHORT"
	d["accountType"] = "business"
	d["email"] = "not-an-email"
	d["addressCountry"] = "USA"
	_, err := ValidatePayoutDetails(mustDest(t, "USD"), d)
	var de *PayoutDetailsError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *PayoutDetailsError", err)
	}
	got := append([]string(nil), de.Fields...)
	sort.Strings(got)
	want := []string{"accountType", "addressCountry", "email", "lastName", "routingNumber", "swiftCode"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bad fields = %v, want %v", got, want)
	}
}

func TestValidatePayoutDetails_GBPSortCodeAndEURIBAN(t *testing.T) {
	gbp := map[string]string{
		"firstName": "Ada", "lastName": "Obi", "bankName": "Barclays", "accountNumber": "31926819",
		"sortCode": "20-00-00", "accountType": "individual", "email": "a@b.co", "phoneCountryCode": "44",
		"phoneNumber": "7700900000", "addressLine1": "1 High St", "city": "London", "state": "London",
		"postalCode": "SW1A 1AA", "addressCountry": "GB",
	}
	got, err := ValidatePayoutDetails(mustDest(t, "GBP"), gbp)
	if err != nil || got["sortCode"] != "200000" {
		t.Fatalf("GBP: %v %+v", err, got)
	}
	eur := map[string]string{
		"firstName": "Ada", "lastName": "Obi", "bankName": "Deutsche Bank", "accountNumber": "de89 3704 0044 0532 0130 00",
		"swiftCode": "DEUTDEFF", "email": "a@b.co", "phoneCountryCode": "49", "phoneNumber": "301234567",
		"addressLine1": "Unter den Linden 1", "city": "Berlin", "state": "Berlin", "postalCode": "10117", "addressCountry": "DE",
	}
	got, err = ValidatePayoutDetails(mustDest(t, "EUR"), eur)
	if err != nil || got["accountNumber"] != "DE89370400440532013000" {
		t.Fatalf("EUR: %v %+v", err, got)
	}
}

func TestValidatePayoutDetails_NGNTakesNone(t *testing.T) {
	got, err := ValidatePayoutDetails(mustDest(t, "NGN"), map[string]string{"firstName": "x"})
	if err != nil || got != nil {
		t.Fatalf("NGN should take no details, got %+v %v", got, err)
	}
}

// The intent keeps the full detail only in PayoutDetails (never serialised);
// the account column holds a masked tail that fits it, IBANs included.
func TestApplySellBank_StoresDetailsAndMasksAccount(t *testing.T) {
	clean, err := ValidatePayoutDetails(mustDest(t, "USD"), usdDetails())
	if err != nil {
		t.Fatal(err)
	}
	in := &models.ITIntent{}
	applySellBank(in, &ITSellBank{DestinationCurrency: "USD", RailType: "bank", Details: clean})
	if in.AccountNumber != "****6789" || in.BankCode != "011304478" || in.AccountName != "Ada Obi" {
		t.Fatalf("intent = %+v", in)
	}
	if !sellIntentHasPayoutDestination(in) {
		t.Fatal("a corridor intent with details must count as having a destination")
	}
	raw, _ := json.Marshal(in)
	if strings.Contains(string(raw), "123456789") {
		t.Fatal("full account number leaked into the intent's JSON")
	}
	back, err := SellPayoutDetails(in)
	if err != nil || back["accountNumber"] != "123456789" {
		t.Fatalf("details round trip: %v %+v", err, back)
	}
}

// The recipient sent to Flutterwave carries exactly the v4 fields the live
// API reported for bank_usd, and the legacy NGN body is unchanged.
func TestPayoutRecipientFromDetails_USDShape(t *testing.T) {
	clean, _ := ValidatePayoutDetails(mustDest(t, "USD"), usdDetails())
	raw, _ := json.Marshal(payoutRecipientFromDetails(clean))
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	want := map[string]any{
		"bank": map[string]any{
			"code": "011304478", "account_number": "123456789", "routing_number": "011304478",
			"swift_code": "IRVTUS3N", "account_type": "checking",
		},
		"name":    map[string]any{"first": "Ada", "last": "Obi"},
		"email":   "ada@example.com",
		"phone":   map[string]any{"country_code": "1", "number": "2125550100"},
		"address": map[string]any{"line1": "1 Main St", "city": "Boston", "state": "MA", "postal_code": "02110", "country": "US"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recipient =\n%s\nwant\n%v", raw, want)
	}
}

func TestPayoutRecipient_NGNLegacyBodyUnchanged(t *testing.T) {
	raw, _ := json.Marshal(FWTransferRecipient{
		Bank: &FWRecipientBank{Code: "044", AccountNumber: "0123456789"},
		Name: &FWRecipientName{Full: "ADA OBI"},
	})
	if string(raw) != `{"bank":{"code":"044","account_number":"0123456789"},"name":{"full":"ADA OBI"}}` {
		t.Fatalf("legacy NGN recipient changed: %s", raw)
	}
}

// Nomba pays naira only: a foreign corridor payout (recipient in Details,
// masked account on the request) is refused before any request is sent.
func TestNombaGateway_RefusesCorridorPayout(t *testing.T) {
	g := &NombaHTTPGateway{}
	_, err := g.InitiatePayout(context.Background(), PayoutRequest{
		AmountNGN: "1000.00", AccountNumber: "****6789",
		Details: map[string]string{"accountNumber": "123456789"},
	})
	var rej *ProviderRejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want *ProviderRejectedError", err)
	}
}
