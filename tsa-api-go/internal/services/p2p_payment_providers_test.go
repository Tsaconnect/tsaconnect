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
	if len(AllPaymentProviders()) == 0 {
		t.Fatal("expected a non-empty provider catalog")
	}
}
