package services

import "testing"

func TestIsValidProvider(t *testing.T) {
	if !IsValidProvider("bank_transfer") {
		t.Fatal("expected bank_transfer to be valid")
	}
	if IsValidProvider("nope_not_real") {
		t.Fatal("expected unknown provider to be invalid")
	}
}

func TestProviderLabelKnown(t *testing.T) {
	if got := ProviderLabel("paypal"); got != "PayPal" {
		t.Fatalf("ProviderLabel(paypal) = %q, want PayPal", got)
	}
}

func TestProviderLabelUnknownFallsBackToTitleCase(t *testing.T) {
	if got := ProviderLabel("foo_bar"); got != "Foo Bar" {
		t.Fatalf("ProviderLabel(foo_bar) = %q, want Foo Bar", got)
	}
}

func TestProviderTypeDerivation(t *testing.T) {
	if got := ProviderType("paypal"); got != "wallet" {
		t.Fatalf("ProviderType(paypal) = %q, want wallet", got)
	}
	if got := ProviderType("bank_transfer"); got != "bank" {
		t.Fatalf("ProviderType(bank_transfer) = %q, want bank", got)
	}
}

func TestAllPaymentProvidersNonEmpty(t *testing.T) {
	providers := AllPaymentProviders()
	if len(providers) != 10 {
		t.Fatalf("expected 10 providers, got %d", len(providers))
	}
}

func TestPaymentProviderBySlug(t *testing.T) {
	p, ok := PaymentProviderBySlug("paypal")
	if !ok {
		t.Fatal("expected paypal to be found")
	}
	if p.Label != "PayPal" || p.Type != "wallet" {
		t.Fatalf("unexpected provider for paypal: %+v", p)
	}
	if _, ok := PaymentProviderBySlug("nope_not_real"); ok {
		t.Fatal("expected unknown slug to return ok=false")
	}
}

func TestPaymentSummaryFromProviders(t *testing.T) {
	got := PaymentSummaryFromProviders([]string{"bank_transfer", "opay", "bank_transfer"})
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped summaries, got %d", len(got))
	}
	if got[0].Provider != "bank_transfer" || got[0].Label != "Bank Transfer" {
		t.Fatalf("unexpected first summary: %+v", got[0])
	}
	if got[1].Provider != "opay" || got[1].Label != "OPay" {
		t.Fatalf("unexpected second summary: %+v", got[1])
	}
}

// TestPaymentSummaryFromProvidersWithUnknownAndEmpty verifies that empty strings
// are skipped, duplicates are deduplicated, and unknown slugs fall back to a
// title-cased label.
func TestPaymentSummaryFromProvidersWithUnknownAndEmpty(t *testing.T) {
	got := PaymentSummaryFromProviders([]string{"opay", "opay", "", "paypal", "weird_slug"})
	want := []PaymentMethodSummary{
		{Provider: "opay", Label: "OPay"},
		{Provider: "paypal", Label: "PayPal"},
		{Provider: "weird_slug", Label: "Weird Slug"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func hasProvider(ps []PaymentProvider, slug string) bool {
	for _, p := range ps {
		if p.Slug == slug {
			return true
		}
	}
	return false
}

// TestProvidersForCurrency locks the region-scoped picker (#24): the catalog is
// filtered to providers serving the active fiat (plus universal ones), and every
// provider carries a speed label.
func TestProvidersForCurrency(t *testing.T) {
	ng := ProvidersForCurrency("NGN")
	for _, s := range []string{"bank_transfer", "opay", "palmpay", "kuda"} {
		if !hasProvider(ng, s) {
			t.Errorf("NGN scope missing %q", s)
		}
	}
	if hasProvider(ng, "interac") || hasProvider(ng, "sepa") {
		t.Errorf("NGN scope should not include interac/sepa")
	}

	ca := ProvidersForCurrency("cad") // case-insensitive
	if !hasProvider(ca, "interac") || !hasProvider(ca, "bank_transfer") {
		t.Errorf("CAD scope missing interac/bank_transfer")
	}
	if hasProvider(ca, "opay") {
		t.Errorf("CAD scope should not include opay")
	}

	eur := ProvidersForCurrency("EUR")
	if !hasProvider(eur, "sepa") || !hasProvider(eur, "revolut") {
		t.Errorf("EUR scope missing sepa/revolut")
	}

	xx := ProvidersForCurrency("XYZ")
	if !hasProvider(xx, "bank_transfer") {
		t.Errorf("unknown currency should still include universal bank_transfer")
	}
	if hasProvider(xx, "opay") || hasProvider(xx, "interac") {
		t.Errorf("unknown currency should include only universal providers")
	}

	if len(ProvidersForCurrency("")) != len(AllPaymentProviders()) {
		t.Errorf("empty currency should return the full catalog")
	}

	for _, p := range AllPaymentProviders() {
		if p.Speed == "" || p.SpeedLabel == "" {
			t.Errorf("provider %q missing speed/speedLabel", p.Slug)
		}
	}
}
