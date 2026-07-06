package models

import "testing"

func TestValidBookingType(t *testing.T) {
	for _, s := range []string{BookingTypeInstant, BookingTypeRequest} {
		if !ValidBookingType(s) {
			t.Errorf("ValidBookingType(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"request_approval", "quote_before_payment", "bogus", ""} {
		if ValidBookingType(s) {
			t.Errorf("ValidBookingType(%q) = true, want false", s)
		}
	}
}

func TestValidPricingMethod(t *testing.T) {
	for _, s := range []string{PricingMethodFixed, PricingMethodOnRequest} {
		if !ValidPricingMethod(s) {
			t.Errorf("ValidPricingMethod(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"starting_from", "dynamic", "quote_later", "bogus", ""} {
		if ValidPricingMethod(s) {
			t.Errorf("ValidPricingMethod(%q) = true, want false", s)
		}
	}
}
