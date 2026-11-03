package services

import "testing"

// TestWithdrawalDestinationCatalog pins the product catalog: the seven payout
// currencies on bank rails.
//
// Mobile money is deliberately absent even though the gateway supports it —
// it needs per-network verification of the provider's `network` and `country`
// values, and an unverified network fails at payout time, AFTER the on-chain
// debit has already left the user's premium in limbo.
func TestWithdrawalDestinationCatalog(t *testing.T) {
	want := map[string]string{
		"NGN": "bank", "GHS": "bank", "KES": "bank", "ZAR": "bank", "USD": "bank", "EUR": "bank", "GBP": "bank",
	}
	if len(WithdrawalDestinationCatalog) != len(want) {
		t.Fatalf("catalog has %d entries, want %d: %+v", len(WithdrawalDestinationCatalog), len(want), WithdrawalDestinationCatalog)
	}
	for _, got := range WithdrawalDestinationCatalog {
		rail, ok := want[got.Currency]
		if !ok {
			t.Errorf("unexpected catalog currency %q", got.Currency)
			continue
		}
		if got.Rail != rail || got.Network != "" {
			t.Errorf("catalog entry %+v: want rail %q with no network", got, rail)
		}
		if got.Label == "" {
			t.Errorf("catalog entry %q has no label — the app renders this", got.Currency)
		}
	}
}

// TestSupportedWithdrawalQuoteCurrencies_DerivesFromCatalog confirms the FX
// quote allowlist is exactly "the catalog's currencies minus NGN".
//
// NGN is excluded because it is the source currency: a naira payout funded from
// naira has no FX leg and is paid out 1:1, so quoting it would invent a
// conversion that does not happen. The derivation keeps the allowlist from
// drifting away from what Sell will actually accept.
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
		{"XOF mobile_money wave rejected (not in product catalog)", "XOF", "mobile_money", "wave", false},
		{"USD bank accepted", "USD", "bank", "", true},
		{"GHS bank accepted", "GHS", "bank", "", true},
		{"GHS mobile_money rejected (rail not offered yet)", "GHS", "mobile_money", "mtn", false},
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
