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
