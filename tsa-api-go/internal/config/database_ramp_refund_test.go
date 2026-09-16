package config

import (
	"os"
	"strings"
	"testing"
)

// TestAutoMigrateAddsThePendingRefundGuardIndex is an architecture test, like
// TestAutoMigrateHasNoDestructiveDDL: AutoMigrate is Postgres-specific and
// cannot run under the sqlite harness services' unit tests use, so this
// asserts against the source that the constraint is still there.
//
// Why this constraint exists: the (order_id, kind, attempt) unique index
// alone only rejects two callers landing on the SAME attempt number.
// services.ConversionOrchestrator.RefundFromTreasury and the automatic
// Advance/refund() path each read the ledger, then separately compute
// NextAttempt, then Reserve — that read-then-decide is not atomic, so two
// concurrent callers can each land on a different, non-colliding attempt
// number and both go on to broadcast a refund for the same order. A partial
// unique index over PENDING refund legs, keyed on order_id alone, is the
// only thing that closes that window: it rejects the second INSERT
// regardless of which attempt number it chose. If this index is ever
// removed, that double-payment path silently reopens.
func TestAutoMigrateAddsThePendingRefundGuardIndex(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatalf("read database.go: %v", err)
	}
	got := string(src)

	for _, want := range []string{
		"uq_ramp_legs_one_pending_refund",
		"ON ramp_legs (order_id)",
		"kind = 'refund'",
		"status = 'pending'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("database.go is missing %q from the one-pending-refund-per-order guard index; "+
				"this is the constraint that stops two concurrent refunds paying the same order twice", want)
		}
	}
}
