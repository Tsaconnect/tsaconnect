package models

import (
	"encoding/json"
	"strings"
	"testing"
)

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

// owed_to_user has THREE states on a converted order, and the difference
// between two of them is whether anyone may be paid: "0" means nothing is owed,
// a number is what is owed, and "" means UNDETERMINED — a transfer is in flight
// and the user may already hold the money. `omitempty` would drop the empty
// one, making it indistinguishable over the API from a direct order that never
// had the field at all.
func TestPaycrestOrderRecord_UndeterminedOwedToUserSurvivesJSON(t *testing.T) {
	blob, err := json.Marshal(&PaycrestOrderRecord{
		Status: PaycrestStatusRecoveryPending, RouteKind: RouteKindConverted, OwedToUser: "",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"owedToUser":""`) {
		t.Fatalf("json = %s; UNDETERMINED must cross the API, not be dropped", blob)
	}
}
