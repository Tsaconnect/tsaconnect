package services

import "strings"

// nameTokens lowercases s, keeps only letter runs as tokens, and drops
// single-letter tokens (initials like "A.").
func nameTokens(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	}) {
		if len(f) > 1 {
			out = append(out, f)
		}
	}
	return out
}

// MatchesPayerName reports whether a bank sender name matches a KYC name closely
// enough to AUTO-release a PM buy trade (design §5). Conservative by intent: a
// non-match routes to the PM manual-release queue, not a failure.
//
// Rule: normalize both to letter-only tokens (single letters dropped). The KYC
// name must have >= 1 token. If it has a single token, require an exact token
// match. Otherwise require at least 2 KYC tokens present in the sender set AND
// at most one KYC token missing (tolerates one extra/typo'd middle name).
func MatchesPayerName(sender, kyc string) bool {
	st := nameTokens(sender)
	kt := nameTokens(kyc)
	if len(st) == 0 || len(kt) == 0 {
		return false
	}
	senderSet := make(map[string]bool, len(st))
	for _, t := range st {
		senderSet[t] = true
	}
	matched := 0
	for _, t := range kt {
		if senderSet[t] {
			matched++
		}
	}
	if len(kt) == 1 {
		return matched == 1
	}
	return matched >= 2 && matched >= len(kt)-1
}
