package models

import (
	"net/netip"
	"testing"
)

func TestPayMerchant_LiveAllowedNeedsBothSwitches(t *testing.T) {
	for _, tc := range []struct{ global, merchant, want bool }{
		{false, false, false}, {false, true, false}, {true, false, false}, {true, true, true},
	} {
		if got := (&PayMerchant{LiveEnabled: tc.merchant}).LiveAllowed(tc.global); got != tc.want {
			t.Errorf("global=%v merchant=%v: %v, want %v", tc.global, tc.merchant, got, tc.want)
		}
	}
}

func TestPayMerchant_OnlyTheLiveKeyIsHeldToItsAllowlist(t *testing.T) {
	listed := &PayMerchant{AllowedIPs: "203.0.113.7,198.51.100.0/24,2001:db8:1::/48"}
	for _, tc := range []struct {
		m    *PayMerchant
		mode string
		ip   string
		want bool
	}{
		{&PayMerchant{}, PayModeLive, "192.0.2.1", true}, // no list: anywhere
		{listed, PayModeTest, "192.0.2.1", true},         // a test key is never restricted
		{listed, PayModeLive, "203.0.113.7", true},
		{listed, PayModeLive, "::ffff:203.0.113.7", true}, // the same address, IPv4-mapped
		{listed, PayModeLive, "198.51.100.200", true},
		{listed, PayModeLive, "2001:db8:1:ff::1", true},
		{listed, PayModeLive, "203.0.113.8", false},
		{listed, PayModeLive, "2001:db8:2::1", false},
		{listed, PayModeLive, "", false}, // an address we could not read is refused
		{&PayMerchant{AllowedIPs: "not-an-ip"}, PayModeLive, "203.0.113.7", false},
	} {
		ip, _ := netip.ParseAddr(tc.ip)
		if got := tc.m.KeyAllowedFrom(tc.mode, ip); got != tc.want {
			t.Errorf("%q %s from %q: %v, want %v", tc.m.AllowedIPs, tc.mode, tc.ip, got, tc.want)
		}
	}
}
