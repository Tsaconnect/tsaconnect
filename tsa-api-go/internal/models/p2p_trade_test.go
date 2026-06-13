package models

import (
	"slices"
	"testing"
)

// TestP2PTradeValidNextStatuses pins the state machine so any future change
// has to update this test in lock-step. The reachability matrix below is the
// source of truth for service-layer transition guards.
func TestP2PTradeValidNextStatuses(t *testing.T) {
	cases := []struct {
		from string
		want []string
	}{
		{P2PTradeStatusPendingPayment, []string{P2PTradeStatusPaid, P2PTradeStatusCancelled}},
		{P2PTradeStatusPaid, []string{P2PTradeStatusReleasing, P2PTradeStatusDisputed}},
		{P2PTradeStatusReleasing, []string{P2PTradeStatusReleased, P2PTradeStatusPaid}},
		{P2PTradeStatusDisputed, []string{P2PTradeStatusResolved}},
		{P2PTradeStatusReleased, nil},
		{P2PTradeStatusCancelled, nil},
		{P2PTradeStatusResolved, nil},
		{"unknown_garbage", nil},
	}

	for _, tc := range cases {
		got := P2PTradeValidNextStatuses(tc.from)
		if len(got) != len(tc.want) {
			t.Errorf("from=%q: len mismatch want=%v got=%v", tc.from, tc.want, got)
			continue
		}
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("from=%q: missing transition to %q (got=%v)", tc.from, w, got)
			}
		}
	}
}

// TestP2PTradeTerminalStates guards against accidentally allowing transitions
// out of terminal states — a class of bug that silently double-spends.
func TestP2PTradeTerminalStates(t *testing.T) {
	terminals := []string{
		P2PTradeStatusReleased,
		P2PTradeStatusCancelled,
		P2PTradeStatusResolved,
	}
	for _, s := range terminals {
		if next := P2PTradeValidNextStatuses(s); len(next) != 0 {
			t.Errorf("terminal status %q has transitions %v; expected none", s, next)
		}
	}
}
