package config

import (
	"os"
	"strings"
	"testing"
)

// TestAutoMigrateAddsTheOpenMCGPBuyIndex is an architecture test in the same
// style as TestAutoMigrateAddsThePendingRefundGuardIndex: AutoMigrate cannot run
// under SQLite, so the source is asserted.
//
// The index is the backstop for "one open MCGP buy per user" — without it a
// double tap on Buy can mint two open orders, and two same-user orders are
// exactly what makes a central-account credit contested. Its build must also
// stay NON-FATAL: production may already hold a duplicate from the superseding
// era, and a migration that stops on it would block the whole deploy for a
// constraint the application already enforces.
func TestAutoMigrateAddsTheOpenMCGPBuyIndex(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatalf("read database.go: %v", err)
	}
	got := string(src)

	start := strings.Index(got, "CREATE UNIQUE INDEX IF NOT EXISTS uq_it_intents_open_mcgp_buy")
	if start < 0 {
		t.Fatal("database.go no longer builds uq_it_intents_open_mcgp_buy — the one-open-MCGP-buy race is unguarded")
	}
	block := got[start:]
	if end := strings.Index(block, "\n\t}\n"); end > 0 {
		block = block[:end]
	}
	for _, want := range []string{
		"ON it_intents (user_id)",
		"direction = 'buy'",
		"status = 'pending_payment'",
		"(asset IS NULL OR asset = '' OR asset = 'MCGP')",
		"payment_ref LIKE 'IT-%'",
		// Non-fatal: the error is logged as a warning, never returned.
		"[Migrate] WARNING could not build uq_it_intents_open_mcgp_buy",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("uq_it_intents_open_mcgp_buy block is missing %q", want)
		}
	}
	if strings.Contains(block, "return ") || strings.Contains(block, "log.Fatal") {
		t.Error("uq_it_intents_open_mcgp_buy failure is fatal — existing duplicates would block every deploy")
	}
}
