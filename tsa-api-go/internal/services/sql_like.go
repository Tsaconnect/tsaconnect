package services

import "strings"

// likeLiteral escapes s for a LIKE pattern written with ESCAPE '\' (works on
// PostgreSQL and SQLite): \, % and _ match only themselves.
func likeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}
