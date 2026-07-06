package models

import "testing"

func TestEffectiveServiceType(t *testing.T) {
	cases := []struct {
		name        string
		serviceType string
		feeMode     string
		want        string
	}{
		{"explicit contact", ServiceTypeContact, "", ServiceTypeContact},
		{"explicit order", ServiceTypeOrder, "", ServiceTypeOrder},
		{"explicit booking", ServiceTypeBooking, "", ServiceTypeBooking},
		{"unset falls back to contact", "", ServiceFeeModeStandard, ServiceTypeContact},
		{"legacy booking fee-mode maps to booking", "", ServiceFeeModeBooking, ServiceTypeBooking},
		{"zero value defaults to contact", "", "", ServiceTypeContact},
		{"invalid type falls back", "bogus", ServiceFeeModeStandard, ServiceTypeContact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := Category{ServiceType: tc.serviceType, ServiceFeeMode: tc.feeMode}
			if got := EffectiveServiceType(cat); got != tc.want {
				t.Errorf("EffectiveServiceType(%q,%q) = %q, want %q", tc.serviceType, tc.feeMode, got, tc.want)
			}
		})
	}
}
