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
	if len(providers) != 9 {
		t.Fatalf("expected 9 providers, got %d", len(providers))
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
