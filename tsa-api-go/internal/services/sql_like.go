package services

import "strings"

// LikeLiteral escapes s for a LIKE pattern written with ESCAPE '\' (works on
// PostgreSQL and SQLite): \, % and _ match only themselves.
func LikeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}
