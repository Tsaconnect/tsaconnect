package services

import (
	"net/netip"
	"testing"
)

func TestPayPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "0.1.2.3", "192.0.0.8", "198.18.0.1", "240.0.0.1", "255.255.255.255",
		"::1", "::", "fc00::1", "fe80::1", "ff02::1", "fec0::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "64:ff9b:1::1", "2002:7f00:1::", "2001::1", "::7f00:1"} {
		if payPublicIP(netip.MustParseAddr(s)) {
			t.Fatalf("%s treated as public", s)
		}
	}
	for _, s := range []string{"102.89.0.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !payPublicIP(netip.MustParseAddr(s)) {
			t.Fatalf("%s refused", s)
		}
	}
}

func TestValidPayWebhookURL(t *testing.T) {
	if err := ValidPayWebhookURL("https://skyair.ng/hooks/tsa"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://skyair.ng/h", "https://localhost/h", "https://api.localhost/h", "https://127.0.0.1/h",
		"https://10.0.0.5/h", "https://[::1]/h", "https://skyair.ng:8443/h", "https://user:pw@skyair.ng/h", "not a url", ""} {
		if err := ValidPayWebhookURL(u); err == nil {
			t.Fatalf("%q accepted", u)
		}
	}
}
