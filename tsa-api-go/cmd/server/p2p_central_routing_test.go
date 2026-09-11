package main

import (
	"strings"
	"testing"
)

// Telling a P2P buyer to pay the company account and being able to turn that
// payment back into their trade are two halves of one thing, and boot wires
// them far apart: the routing beside the P2P handler, the claim source deep
// inside the PM-automation block that additionally needs a release key and a
// chain client. With Instant Transfer live and PM automation dormant the halves
// disagreed — every buyer was sent to an account nothing was watching and every
// payment held, while the only warning covered the inverse case.
func TestP2PCentralRoutingNeedsTheClaimSource(t *testing.T) {
	cases := []struct {
		name                                     string
		itLive, claimsRegistered, centralEnabled bool
		wantRoute                                bool
		wantWarning                              string // substring; "" means no warning at all
	}{
		{
			name:   "both halves live",
			itLive: true, claimsRegistered: true, centralEnabled: true,
			wantRoute: true,
		},
		{
			name:   "IT live but PM automation dormant, so nothing would attribute the payment",
			itLive: true, claimsRegistered: false, centralEnabled: true,
			wantRoute: false, wantWarning: "claim source",
		},
		{
			name:   "IT dormant, so P2P cannot reach the company account at all",
			itLive: false, claimsRegistered: true, centralEnabled: true,
			wantRoute: false, wantWarning: "Instant Transfer is dormant",
		},
		{
			name:   "central mode off: nothing is wrong and nothing is claimed",
			itLive: true, claimsRegistered: false, centralEnabled: false,
			wantRoute: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route, warning := p2pCentralRouting(c.itLive, c.claimsRegistered, c.centralEnabled)
			if route != c.wantRoute {
				t.Fatalf("route = %t, want %t", route, c.wantRoute)
			}
			if c.wantWarning == "" {
				if warning != "" {
					t.Fatalf("warning = %q, want none", warning)
				}
				return
			}
			if !strings.Contains(warning, c.wantWarning) {
				t.Fatalf("warning = %q, want it to mention %q", warning, c.wantWarning)
			}
		})
	}
}

// The refusal is only worth anything if it is the same fact the routing call
// reads. A decision that says "do not route" while main still hands the handler
// the central source would be a comment, not a control.
func TestP2PCentralRoutingNeverRoutesWithoutBothHalves(t *testing.T) {
	for _, itLive := range []bool{false, true} {
		for _, claims := range []bool{false, true} {
			for _, central := range []bool{false, true} {
				route, _ := p2pCentralRouting(itLive, claims, central)
				if route && !(itLive && claims) {
					t.Fatalf("routed centrally with itLive=%t claimsRegistered=%t centralEnabled=%t",
						itLive, claims, central)
				}
			}
		}
	}
}
