package services

import (
	"os"
	"strings"
	"testing"
)

// TestResetDDL_NoPostgresIncompatibleTypes is a SOURCE-level portability guard
// for the cutover reset DDL. SQLite (the unit-test engine) accepts DATETIME and
// REAL as type-affinity tokens, so a green functional test against SQLite cannot
// catch a PostgreSQL-incompatible type — but DATETIME is not a PostgreSQL type
// at all (the CREATE TABLE errors and rolls the single-tx cutover back, leaving
// balances un-zeroed in production), and REAL is a lossy 4-byte float there.
//
// We therefore assert directly on the reset source: the archive DDL must declare
// TIMESTAMP / DOUBLE PRECISION and must NOT declare DATETIME / REAL as a column
// type. This fails loudly if anyone reverts to the non-portable types.
func TestResetDDL_NoPostgresIncompatibleTypes(t *testing.T) {
	src, err := os.ReadFile("fee_distribution_reset.go")
	if err != nil {
		t.Fatalf("read reset source: %v", err)
	}

	// Scan only DDL lines, excluding Go comment lines (which legitimately mention
	// the banned types in prose like "NOT DATETIME"). A DDL column declaration
	// looks like `    created_at TIMESTAMP,` — it is NOT a comment and ends in a
	// comma. Comment lines are dropped before the check.
	var ddlLines, allCode []string
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		allCode = append(allCode, line)
		if strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "(") {
			ddlLines = append(ddlLines, line)
		}
	}
	code := strings.Join(allCode, "\n")
	ddl := strings.Join(ddlLines, "\n")

	// The portable types must be present in the DDL.
	for _, want := range []string{"TIMESTAMP", "DOUBLE PRECISION"} {
		if !strings.Contains(code, want) {
			t.Errorf("reset DDL must declare %q (PostgreSQL-portable), but it is absent", want)
		}
	}

	// The non-portable tokens must NOT appear in any DDL column declaration.
	for _, bad := range []string{"DATETIME", "REAL"} {
		if strings.Contains(ddl, bad) {
			t.Errorf("reset DDL contains PostgreSQL-incompatible type %q — use TIMESTAMP / DOUBLE PRECISION instead", bad)
		}
	}
}
