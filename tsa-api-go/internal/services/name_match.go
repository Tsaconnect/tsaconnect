package services

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Name matching for central-account inbound attribution.
//
// Once every customer pays into one company NUBAN instead of a per-user virtual
// account, a Flutterwave statement row is the only evidence of who sent money.
// That row carries no account number and no structured sender object — just free
// text, "Received Money from GTBANK|ADEMU EMMANUEL O." — so attribution comes
// down to deciding whether that account name and a user's Didit-verified legal
// name belong to the same human.
//
// Nigerian bank account names reorder given and family names freely, drop or add
// middle names, append initials, and carry honorifics. So an equality test on the
// raw strings would send nearly every legitimate payment to a manual queue, while
// anything fuzzier than the rules below risks paying one customer with another
// customer's money.
//
// This file is deliberately a pure function: no DB, no I/O, no clock, no logging.
// Everything it decides is reproducible from its two arguments, which is what
// makes it testable to the depth the risk deserves. The caller
// (services.InboundMatcher) owns the rest of the binding rule — amount equality
// and the "exactly one candidate" requirement — and must never relax those on the
// strength of a match verdict alone.

// NameMatchStrength is how strongly a bank account name identifies the holder of
// a KYC-verified legal name. The three-way result exists because the caller
// treats the two accept levels differently: both bind, but only "exact" is
// self-explanatory in an audit. The string values are stored verbatim in
// models.InboundCredit.MatchStrength.
type NameMatchStrength string

const (
	// NameMatchNone means the two names are not evidence of the same person.
	// The credit must not be bound on this verdict.
	NameMatchNone NameMatchStrength = "none"
	// NameMatchPartial means one name is a strict subset of the other with at
	// least two shared name tokens — the middle-name and dropped-name case.
	NameMatchPartial NameMatchStrength = "partial"
	// NameMatchExact means the two names carry an identical set of name tokens,
	// ignoring order, honorifics and initials.
	NameMatchExact NameMatchStrength = "exact"
)

// IsMatch reports whether the verdict is strong enough to bind a credit. It
// exists so callers never hand-roll `== exact || == partial` and accidentally
// omit one arm.
func (s NameMatchStrength) IsMatch() bool {
	return s == NameMatchExact || s == NameMatchPartial
}

// accountNameHonorifics are titles stripped from either name before comparison.
//
// The list is closed on purpose and matches the design doc exactly. Every entry
// added here is a word that can no longer distinguish two people, so a longer
// list is a looser matcher, not a smarter one. Generational suffixes (JR, SNR,
// II) are deliberately NOT here: they are the only thing separating a father
// from a son who share a full name, and collapsing them would make that pair
// match at "exact".
var accountNameHonorifics = map[string]struct{}{
	"MR": {}, "MRS": {}, "MISS": {}, "MS": {}, "DR": {}, "PROF": {},
	"CHIEF": {}, "ALHAJI": {}, "ALHAJA": {}, "ENGR": {}, "BARR": {},
	"REV": {}, "PASTOR": {}, "MALLAM": {},
}

// accountNameLigatures folds the handful of Latin letters that Unicode
// canonical decomposition cannot take apart, because they are letters in their
// own right rather than a base plus an accent. Without this, "Ø" survives
// normalization and never matches the "O" a Nigerian bank would print.
//
// The table is consulted after the input has been uppercased, so only the
// uppercase forms are needed — except "ß", which Go's ToUpper leaves alone.
var accountNameLigatures = map[rune]string{
	'Æ': "AE", 'Œ': "OE", 'Ø': "O", 'Ð': "D", 'Đ': "D", 'Þ': "TH",
	'Ł': "L", 'Ħ': "H", 'Ŧ': "T", 'Ŋ': "N", 'Ɖ': "D", 'Ɵ': "O",
	'ß': "SS", 'ẞ': "SS",
}

// MatchAccountName decides whether bankName, the free-text account name on an
// inbound statement row, names the same person as kycName, the Didit-verified
// legal name of a user with an open claim.
//
// It is symmetric: swapping the arguments cannot change the verdict.
func MatchAccountName(kycName, bankName string) NameMatchStrength {
	k := accountNameTokenSet(kycName)
	b := accountNameTokenSet(bankName)

	// An empty side carries no identity at all. Without this guard, two empty
	// sets are "equal" and would return exact — a blank Didit name plus a blank
	// statement remark would auto-credit the first user the matcher looked at.
	// This is the single most important line in the file.
	if len(k) == 0 || len(b) == 0 {
		return NameMatchNone
	}

	if len(k) == len(b) && intersectionSize(k, b) == len(k) {
		return NameMatchExact
	}

	// Single-token legal names are rare but real ("Ademu" alone). With only one
	// token there is nothing left to corroborate it, so strict equality — which
	// the branch above already settled — is the only acceptable outcome.
	//
	// The >= 2 floor below would reject these anyway; the branch is kept
	// explicit so that loosening the floor later cannot silently start matching
	// single-token names on a single shared token.
	if len(k) == 1 || len(b) == 1 {
		return NameMatchNone
	}

	shared := intersectionSize(k, b)

	// The two-token floor is what stops a shared given name from moving money.
	// "EMMANUEL JOHN" against "EMMANUEL PETER" overlaps on EMMANUEL alone, and
	// EMMANUEL is one of the most common given names in Nigeria — without this,
	// any Emmanuel could be credited with any other Emmanuel's transfer.
	if shared < 2 {
		return NameMatchNone
	}

	// Subset in either direction: the bank dropped a middle name the KYC record
	// carries, or printed one the record omits. A set is a subset of the other
	// exactly when every one of its tokens is shared. Equality was handled
	// above, so any subset here is strict.
	if shared == len(k) || shared == len(b) {
		return NameMatchPartial
	}

	// Overlapping but neither contained: each side has a token the other has
	// never seen. "EMMANUEL ADEMU OKON" and "EMMANUEL ADEMU JOHN" share two
	// tokens yet disagree on the third, and that third token is the one that
	// tells two relatives apart. Contention holds; it never binds.
	return NameMatchNone
}

// AccountNameTokens returns the normalized, deduplicated, sorted name tokens
// MatchAccountName compares. It is exported so the admin hold queue can show an
// operator why a candidate was rejected using the matcher's own view of the
// names rather than a second, drifting copy of these rules.
func AccountNameTokens(s string) []string {
	set := accountNameTokenSet(s)
	out := make([]string, 0, len(set))
	for tok := range set {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// accountNameTokenSet normalizes a name down to the set of tokens that actually
// carry identity.
//
// Set semantics are intentional: "JOHN JOHN" and "JOHN" describe one person, and
// a repeated token must not inflate an intersection count past the two-token
// floor on its own.
func accountNameTokenSet(s string) map[string]struct{} {
	set := make(map[string]struct{}, 4)
	var tok strings.Builder

	flush := func() {
		if tok.Len() == 0 {
			return
		}
		t := tok.String()
		tok.Reset()

		// Single-character tokens are initials, and initials identify nobody:
		// "ADEMU EMMANUEL O." and "ADEMU EMMANUEL" are routinely the same
		// account. Keeping the "O" would demote that pair from exact to none,
		// because neither set would contain the other.
		if utf8.RuneCountInString(t) < 2 {
			return
		}
		if _, isTitle := accountNameHonorifics[t]; isTitle {
			return
		}
		set[t] = struct{}{}
	}

	for _, r := range foldAccountNameRunes(strings.ToUpper(s)) {
		switch {
		case r == '\'' || r == '’':
			// Apostrophes are deleted rather than split on, so "O'NEILL"
			// becomes the single token ONEILL and matches the apostrophe-less
			// spelling banks usually print. Splitting instead would leave the
			// "O" as a discarded initial and lose half the surname.

		case unicode.IsLetter(r) || unicode.IsDigit(r):
			tok.WriteRune(r)

		default:
			// Everything else separates. Hyphen is the case that matters:
			// "ADEMU-OKON" must become ADEMU and OKON so a double-barrelled
			// surname matches its spaced form, and must never be glued into
			// ADEMUOKON, which would match nothing at all. Dots and commas are
			// separators for the same reason — "ADEMU E.O." has to yield two
			// discardable initials, not the phantom name token "EO", which
			// would sink a legitimate transfer into the manual queue.
			flush()
		}
	}
	flush()

	return set
}

// foldAccountNameRunes strips diacritics and folds undecomposable Latin letters,
// so a Didit record spelling a name "Adémù" or "Ẹmmanuel" still matches the
// plain ASCII a bank prints. Canonical decomposition turns every precomposed
// accented letter into a base letter plus combining marks, which are then
// dropped along with zero-width formatting characters.
func foldAccountNameRunes(s string) []rune {
	decomposed := norm.NFD.String(s)
	out := make([]rune, 0, len(decomposed))
	for _, r := range decomposed {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
			// Combining marks and invisible format characters carry no
			// identity; dropping them is what makes the decomposition above
			// collapse "É" onto "E".
		default:
			if folded, ok := accountNameLigatures[r]; ok {
				out = append(out, []rune(folded)...)
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

// intersectionSize counts tokens present in both sets.
func intersectionSize(a, b map[string]struct{}) int {
	// Iterate the smaller set so the cost is bounded by the shorter name.
	if len(b) < len(a) {
		a, b = b, a
	}
	n := 0
	for tok := range a {
		if _, ok := b[tok]; ok {
			n++
		}
	}
	return n
}

// companyNameSuffixes are legal-form words a bank may add to, drop from, or
// spell differently in a registered business's account name ("SKY AIR LTD",
// "Sky Air Limited", "Sky Air"). They say what kind of entity it is, never which
// one, so they are dropped before a business name is matched. Kept out of
// accountNameHonorifics on purpose: inbound attribution matches people, and a
// looser person matcher is a riskier one.
var companyNameSuffixes = map[string]struct{}{
	"LTD": {}, "LIMITED": {}, "PLC": {}, "INC": {}, "LLC": {}, "CO": {}, "COMPANY": {},
}

// MatchBusinessAccountName is MatchAccountName for a business: the registered
// name and the bank's account name, compared after dropping legal-form suffixes.
// The same floors apply — a lone suffix, or one shared word, is never a match.
func MatchBusinessAccountName(businessName, bankName string) NameMatchStrength {
	return MatchAccountName(withoutCompanySuffixes(businessName), withoutCompanySuffixes(bankName))
}

func withoutCompanySuffixes(s string) string {
	kept := make([]string, 0, 4)
	for _, tok := range AccountNameTokens(s) {
		if _, suffix := companyNameSuffixes[tok]; !suffix {
			kept = append(kept, tok)
		}
	}
	return strings.Join(kept, " ")
}
