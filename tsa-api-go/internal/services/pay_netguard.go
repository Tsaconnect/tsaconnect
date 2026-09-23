package services

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// payNonPublicPrefixes are ranges IsPrivate/IsLoopback/etc. below don't
// already cover, but that still resolve to something other than a real
// internet-reachable host: CGNAT, documentation/benchmarking ranges, the
// broadcast address, and the IPv6 transition ranges that embed an IPv4
// address (NAT64, 6to4, Teredo) or an IPv4-compatible literal — each of
// those can smuggle a loopback/link-local/private IPv4 address past a check
// that only looks at the IPv6 form.
var payNonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

// payPublicIP reports whether ip is a real, internet-routable address — the
// gate a webhook URL's host (or the address a webhook dialer actually
// connects to — Task 8) must pass before we send it a signed payload
// carrying a merchant's payment data. It is deliberately conservative:
// anything not affirmatively public is refused.
func payPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, p := range payNonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidPayWebhookURL validates a merchant-supplied webhook URL at
// configuration time: https only, no embedded credentials, default port
// only, not localhost, and — when the host is an IP literal — a public one.
// A DNS name passes here even though it could still resolve to a private
// address later; Task 8's dialer re-checks the address it actually connects
// to, which is the only check that can't be bypassed by DNS rebinding.
func ValidPayWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("webhook url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid webhook url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhook url must use https")
	}
	if u.User != nil {
		return fmt.Errorf("webhook url must not contain credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook url must have a host")
	}
	if port := u.Port(); port != "" && port != "443" {
		return fmt.Errorf("webhook url must use the default https port")
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("webhook url must not target localhost")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !payPublicIP(ip) {
		return fmt.Errorf("webhook url must target a public address")
	}
	return nil
}
