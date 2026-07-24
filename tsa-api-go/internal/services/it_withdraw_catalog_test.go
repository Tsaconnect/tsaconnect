package services

import "testing"

// TestWithdrawalDestinationCatalog pins the product catalog: NGN/bank only
// until multi-currency withdrawal is re-enabled.
func TestWithdrawalDestinationCatalog(t *testing.T) {
	if len(WithdrawalDestinationCatalog) != 1 {
		t.Fatalf("catalog has %d entries, want 1: %+v", len(WithdrawalDestinationCatalog), WithdrawalDestinationCatalog)
	}
	got := WithdrawalDestinationCatalog[0]
	if got.Currency != "NGN" || got.Rail != "bank" || got.Network != "" {
		t.Fatalf("catalog entry = %+v, want NGN/bank", got)
	}
	if got.Label == "" {
		t.Fatal("catalog entry for NGN has no label")
	}
}

// TestSupportedWithdrawalQuoteCurrencies_DerivesFromCatalog confirms the FX
// quote allowlist is exactly "the catalog's currencies minus NGN" — empty
// while the product catalog is NGN-only.
func TestSupportedWithdrawalQuoteCurrencies_DerivesFromCatalog(t *testing.T) {
	want := map[string]bool{}
	for _, d := range WithdrawalDestinationCatalog {
		if d.Currency == "NGN" {
			continue
		}
		want[d.Currency] = true
	}
	if len(SupportedWithdrawalQuoteCurrencies) != len(want) {
		t.Fatalf("SupportedWithdrawalQuoteCurrencies = %+v, want %+v", SupportedWithdrawalQuoteCurrencies, want)
	}
	for k := range want {
		if !SupportedWithdrawalQuoteCurrencies[k] {
			t.Fatalf("SupportedWithdrawalQuoteCurrencies missing %q derived from catalog", k)
		}
	}
	if SupportedWithdrawalQuoteCurrencies["NGN"] {
		t.Fatal("NGN must not be in the quote allowlist — it's the source currency, no FX leg")
	}
}

// TestIsSupportedWithdrawalDestination pins the accept/reject matrix for the
// NGN-only product catalog.
func TestIsSupportedWithdrawalDestination(t *testing.T) {
	cases := []struct {
		name                    string
		currency, rail, network string
		want                    bool
	}{
		{"NGN bank accepted", "NGN", "bank", "", true},
		{"case-insensitive currency+rail", "ngn", "BANK", "", true},
		{"GHS bank rejected (not in product catalog)", "GHS", "bank", "", false},
		{"XOF mobile_money wave rejected (not in product catalog)", "XOF", "mobile_money", "wave", false},
		{"USD bank rejected", "USD", "bank", "", false},
		{"NGN mobile_money rejected", "NGN", "mobile_money", "", false},
		{"empty currency rejected", "", "bank", "", false},
		{"empty rail rejected", "NGN", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsSupportedWithdrawalDestination(tc.currency, tc.rail, tc.network)
			if got != tc.want {
				t.Fatalf("IsSupportedWithdrawalDestination(%q, %q, %q) = %v, want %v", tc.currency, tc.rail, tc.network, got, tc.want)
			}
		})
	}
}
