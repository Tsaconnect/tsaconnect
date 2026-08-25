package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// bannedDDL matches destructive statements that must never run during boot migration.
// Case-insensitive: DB.Exec("drop table x") is as dangerous as the uppercase form.
var bannedDDL = regexp.MustCompile(`(?i)(DropTable\(|DropColumn\(|DropConstraint\(|` +
	`\bDROP\s+TABLE\b|\bTRUNCATE\b|\bDELETE\s+FROM\b)`)

// TestAutoMigrateHasNoDestructiveDDL is an architecture test, not a behaviour test.
// AutoMigrate is Postgres-specific and cannot run under the sqlite harness, so the
// property we protect — boot migration never destroys data — is asserted against the
// source.
//
// Background: a guarded DropTable("it_rates") shipped here. Its guard made it inert on
// already-migrated databases, but it would still fire on a host restored from an older
// dump, silently dropping a live table. Destructive changes belong in deploy/one-off/,
// applied deliberately by an operator who has read them.
//
// DROP INDEX and ALTER TABLE ... DROP CONSTRAINT are deliberately NOT banned: neither
// destroys data, and both are used legitimately in this file.
func TestAutoMigrateHasNoDestructiveDDL(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatalf("read database.go: %v", err)
	}

	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip comments — this file documents past migrations in prose, and a comment
		// mentioning DROP TABLE must not fail the build.
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if m := bannedDDL.FindString(line); m != "" {
			t.Errorf("database.go:%d: destructive DDL %q in boot migration.\n"+
				"During a deploy an older release is still serving from this database, "+
				"and a restored dump would be silently damaged. Put it in "+
				"deploy/one-off/ instead.", i+1, m)
		}
	}
}
