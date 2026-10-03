package ramp

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestHandle_RoundTripsAndIsOpaque(t *testing.T) {
	h := EncodeHandle(ProviderPaycrest, VenueKey{ChainKey: "base", Token: "USDC"})
	if h == "" || containsAny(h, "paycrest", "|", "=") {
		t.Fatalf("handle %q must be opaque, unpadded base64url", h)
	}
	p, k, err := DecodeHandle(h)
	if err != nil || p != ProviderPaycrest || k != (VenueKey{ChainKey: "base", Token: "USDC"}) {
		t.Fatalf("decode = %q %+v %v", p, k, err)
	}
	for _, bad := range []string{"", "!!!", EncodeHandleRaw("paycrest|base"), EncodeHandleRaw("|base|USDC")} {
		if _, _, err := DecodeHandle(bad); !errors.Is(err, ErrInvalidQuote) {
			t.Errorf("DecodeHandle(%q) err = %v, want ErrInvalidQuote", bad, err)
		}
	}
}

func TestResolveID(t *testing.T) {
	id := uuid.NewString()
	if p, n, err := ResolveID(id); err != nil || p != ProviderPaycrest || n != id {
		t.Fatalf("bare uuid → %q %q %v", p, n, err)
	}
	if p, n, err := ResolveID("flutterwave:abc-123"); err != nil || p != "flutterwave" || n != "abc-123" {
		t.Fatalf("prefixed → %q %q %v", p, n, err)
	}
	for _, bad := range []string{"", "not-a-uuid", ":abc", "flutterwave:"} {
		if _, _, err := ResolveID(bad); !errors.Is(err, ErrOrderNotFound) {
			t.Errorf("ResolveID(%q) err = %v, want ErrOrderNotFound", bad, err)
		}
	}
}

func TestLimitRateAndAcceptable(t *testing.T) {
	buyLimit, err := LimitRate(Buy, "1400", 50) // 1400 × 1.005
	if err != nil || buyLimit != "1407.00000000" {
		t.Fatalf("buy limit = %q %v", buyLimit, err)
	}
	sellLimit, err := LimitRate(Sell, "1400", 50) // 1400 × 0.995
	if err != nil || sellLimit != "1393.00000000" {
		t.Fatalf("sell limit = %q %v", sellLimit, err)
	}
	if !Acceptable(Buy, "1407", buyLimit) || Acceptable(Buy, "1407.0000001", buyLimit) {
		t.Fatal("buy boundary wrong")
	}
	if !Acceptable(Sell, "1393", sellLimit) || Acceptable(Sell, "1392.9999999", sellLimit) {
		t.Fatal("sell boundary wrong")
	}
	for _, bad := range []string{"", "0", "-5", "abc"} {
		if _, err := LimitRate(Buy, bad, 50); !errors.Is(err, ErrInvalidQuote) {
			t.Errorf("LimitRate(%q) err = %v, want ErrInvalidQuote", bad, err)
		}
	}
}

func TestRateBetter(t *testing.T) {
	if !RateBetter(Buy, "1390", "1400") || RateBetter(Buy, "1400", "1390") {
		t.Fatal("buy: lower naira per token is better")
	}
	if !RateBetter(Sell, "1400", "1390") || RateBetter(Sell, "1390", "1400") {
		t.Fatal("sell: higher naira per token is better")
	}
}

func TestPriceMovedErrorMatchesTheServiceSentinel(t *testing.T) {
	var err error = &PriceMovedError{}
	if !errors.Is(err, services.ErrPriceMovedPastQuote) {
		t.Fatal("PriceMovedError must match services.ErrPriceMovedPastQuote")
	}
}

// The KYC refusal is shown to the user verbatim by paycrestError: a class
// must never prefix it.
func TestWrapClass_KeepsTheCauseMessageAndBothMatches(t *testing.T) {
	err := WrapClass(ErrRefusedForUser, services.ErrKYCVerificationRequired)
	if err.Error() != services.ErrKYCVerificationRequired.Error() {
		t.Fatalf("message = %q", err.Error())
	}
	if !errors.Is(err, ErrRefusedForUser) || !errors.Is(err, services.ErrKYCVerificationRequired) {
		t.Fatal("must match both the class and the cause")
	}
	if WrapClass(ErrUnavailable, nil) != ErrUnavailable {
		t.Fatal("a nil cause is the bare class")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
