package services

import "testing"

// TestWithdrawalDestinationCatalog pins the resolved v1 catalog: NGN/bank,
// GHS/bank, XOF/mobile_money/wave — nothing more, nothing less.
func TestWithdrawalDestinationCatalog(t *testing.T) {
	if len(WithdrawalDestinationCatalog) != 3 {
		t.Fatalf("catalog has %d entries, want 3: %+v", len(WithdrawalDestinationCatalog), WithdrawalDestinationCatalog)
	}
	want := map[string]WithdrawalDestination{
		"NGN": {Currency: "NGN", Rail: "bank"},
		"GHS": {Currency: "GHS", Rail: "bank"},
		"XOF": {Currency: "XOF", Rail: "mobile_money", Network: "wave"},
	}
	for _, got := range WithdrawalDestinationCatalog {
		w, ok := want[got.Currency]
		if !ok {
			t.Fatalf("unexpected catalog currency %q", got.Currency)
		}
		if got.Rail != w.Rail || got.Network != w.Network {
			t.Fatalf("catalog entry for %s = %+v, want rail=%q network=%q", got.Currency, got, w.Rail, w.Network)
		}
		if got.Label == "" {
			t.Fatalf("catalog entry for %s has no label", got.Currency)
		}
		delete(want, got.Currency)
	}
	if len(want) != 0 {
		t.Fatalf("catalog missing currencies: %+v", want)
	}
}

// TestSupportedWithdrawalQuoteCurrencies_DerivesFromCatalog confirms the FX
// quote allowlist is exactly "the catalog's currencies minus NGN" — the two
// must never be maintained as separate lists that can drift.
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

// TestIsSupportedWithdrawalDestination pins the accept/reject matrix from the
// task brief.
func TestIsSupportedWithdrawalDestination(t *testing.T) {
	cases := []struct {
		name                    string
		currency, rail, network string
		want                    bool
	}{
		{"GHS bank accepted", "GHS", "bank", "", true},
		{"XOF mobile_money wave accepted", "XOF", "mobile_money", "wave", true},
		{"NGN bank accepted", "NGN", "bank", "", true},
		{"case-insensitive currency+rail", "ghs", "BANK", "", true},
		{"case-insensitive network", "xof", "Mobile_Money", "WAVE", true},
		{"USD bank rejected (unknown currency)", "USD", "bank", "", false},
		{"GHS mobile_money rejected (wrong rail for currency)", "GHS", "mobile_money", "", false},
		{"XOF mobile_money mpesa rejected (wrong network)", "XOF", "mobile_money", "mpesa", false},
		{"empty currency rejected", "", "bank", "", false},
		{"empty rail rejected", "GHS", "", "", false},
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
