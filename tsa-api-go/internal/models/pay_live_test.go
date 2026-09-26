package models

import "testing"

func TestPayMerchant_LiveAllowedNeedsBothSwitches(t *testing.T) {
	for _, tc := range []struct{ global, merchant, want bool }{
		{false, false, false}, {false, true, false}, {true, false, false}, {true, true, true},
	} {
		if got := (&PayMerchant{LiveEnabled: tc.merchant}).LiveAllowed(tc.global); got != tc.want {
			t.Errorf("global=%v merchant=%v: %v, want %v", tc.global, tc.merchant, got, tc.want)
		}
	}
}
