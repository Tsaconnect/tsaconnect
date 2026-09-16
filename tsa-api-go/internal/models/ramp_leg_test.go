package models

import "testing"

func TestRampLegTerminalStatuses(t *testing.T) {
	if !RampLegTerminal(RampLegStatusConfirmed) || !RampLegTerminal(RampLegStatusFailed) {
		t.Fatal("confirmed and failed are terminal")
	}
	if RampLegTerminal(RampLegStatusPending) {
		t.Fatal("pending is not terminal")
	}
}

// A converted order must never look complete while the treasury still holds
// the user's funds: converting is not a terminal Paycrest status.
func TestConvertingIsNotTerminal(t *testing.T) {
	for _, s := range PaycrestTerminalStatuses() {
		if s == PaycrestStatusConverting || s == PaycrestStatusRefunding {
			t.Fatalf("%s must not be terminal", s)
		}
	}
	if !PaycrestOrderTerminal(PaycrestStatusRefunded) {
		t.Fatal("refunded is terminal")
	}
}

// A converted buy parked in recovery_pending is the same shape of problem as
// an unmatched deposit: the money's position is known but only a human can
// settle what is still owed. Both have to surface through one predicate, or
// the admin queue silently never shows one of them.
func TestNeedsOperator_CoversRecoveryPending(t *testing.T) {
	for _, status := range []string{PaycrestStatusUnmatched, PaycrestStatusRecoveryPending} {
		if !(&PaycrestOrderRecord{Status: status}).NeedsOperator() {
			t.Errorf("status %q must surface to the operator queue", status)
		}
	}
	for _, status := range []string{
		PaycrestStatusConverting, PaycrestStatusRefunding,
		PaycrestStatusSettled, PaycrestStatusRefunded,
	} {
		if (&PaycrestOrderRecord{Status: status}).NeedsOperator() {
			t.Errorf("status %q does not need an operator", status)
		}
	}
}
