package services

import (
	"fmt"
	"strings"
	"testing"
)

// nameMatchCase is one KYC-name/bank-name verdict. The whole suite runs off a
// single table so that every case is also checked for symmetry and determinism
// without being restated.
type nameMatchCase struct {
	name string
	kyc  string
	bank string
	want NameMatchStrength
}

// longName joins n distinct tokens, optionally reversed, to exercise inputs far
// longer than any real account name.
func longName(n int, reversed bool) string {
	toks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		toks = append(toks, fmt.Sprintf("Chidinma%02d", i))
	}
	if reversed {
		for i, j := 0, len(toks)-1; i < j; i, j = i+1, j-1 {
			toks[i], toks[j] = toks[j], toks[i]
		}
	}
	return strings.Join(toks, " ")
}

func nameMatchCases() []nameMatchCase {
	return []nameMatchCase{
		// ---- the six worked examples from the design doc, §5.2 ----
		{"reordered given and family name", "Emmanuel Ademu", "ADEMU EMMANUEL", NameMatchExact},
		{"trailing initial is discarded", "Emmanuel Ademu", "ADEMU EMMANUEL O.", NameMatchExact},
		{"bank dropped the middle name", "Emmanuel Okon Ademu", "EMMANUEL ADEMU", NameMatchPartial},
		{"honorific is stripped", "Emmanuel Ademu", "MR EMMANUEL ADEMU", NameMatchExact},
		{"shared given name only", "Emmanuel John", "EMMANUEL PETER", NameMatchNone},
		{"hyphen splits a double-barrelled surname", "Emmanuel Ademu Okon", "EMMANUEL ADEMU-OKON", NameMatchExact},

		// ---- empty and blank input ----
		// Two empty token sets are trivially "equal". If that were allowed to
		// return exact, a user with no Didit name and a statement row with an
		// unparsable remark would auto-credit each other.
		{"both sides empty", "", "", NameMatchNone},
		{"kyc empty", "", "ADEMU EMMANUEL", NameMatchNone},
		{"bank empty", "Emmanuel Ademu", "", NameMatchNone},
		{"both sides whitespace", "   \t\n ", "   ", NameMatchNone},
		{"bank side whitespace only", "Emmanuel Ademu", "     ", NameMatchNone},
		{"both sides punctuation only", "...", ",,, -- ''", NameMatchNone},

		// ---- input that normalizes away to nothing ----
		{"honorifics only on both sides", "Mr Mrs", "CHIEF", NameMatchNone},
		{"honorifics only against a real name", "Chief Alhaji Dr", "EMMANUEL ADEMU", NameMatchNone},
		{"initials only on both sides", "A. B. C.", "A B C", NameMatchNone},
		{"initials only against a real name", "Emmanuel Ademu", "E A", NameMatchNone},

		// ---- single-token legal names: strict equality or nothing ----
		{"single token both sides, equal", "Ademu", "ADEMU", NameMatchExact},
		{"single token both sides, different", "Ademu", "OKON", NameMatchNone},
		{"single token kyc inside a longer bank name", "Ademu", "ADEMU EMMANUEL", NameMatchNone},
		{"single token bank inside a longer kyc name", "Emmanuel Ademu", "ADEMU", NameMatchNone},
		{"single token after honorific stripping", "Emmanuel Ademu", "MR EMMANUEL", NameMatchNone},

		// ---- diacritics ----
		{"precomposed accents fold to ascii", "Adémù Emmánuel", "EMMANUEL ADEMU", NameMatchExact},
		// Explicitly decomposed: a base letter plus a combining acute. Didit and
		// the bank can disagree on Unicode form for the very same name.
		{"decomposed accents fold to ascii", "Ade\u0301mu Emmanuel", "ADEMU EMMANUEL", NameMatchExact},
		{"yoruba dot-below folds to ascii", "Ẹmmanuel Ọlá", "EMMANUEL OLA", NameMatchExact},
		{"igbo dot-below folds to ascii", "Chinụa Achebe", "CHINUA ACHEBE", NameMatchExact},
		{"undecomposable latin letter folds", "Øyvind Ademu", "OYVIND ADEMU", NameMatchExact},
		{"eszett uppercases to ss on both sides", "Straßer Emmanuel", "STRASSER EMMANUEL", NameMatchExact},

		// ---- case and whitespace shape ----
		{"mixed case", "eMmAnUeL aDeMu", "ademu emmanuel", NameMatchExact},
		{"multiple internal spaces", "Emmanuel     Ademu", "ADEMU  EMMANUEL", NameMatchExact},
		{"leading and trailing whitespace", "  Emmanuel Ademu  ", "\tADEMU EMMANUEL\n", NameMatchExact},
		{"non-breaking space separates", "Emmanuel Ademu", "ADEMU\u00a0EMMANUEL", NameMatchExact},
		{"zero-width joiner is dropped", "Emmanuel\u200d Ademu", "ADEMU EMMANUEL", NameMatchExact},

		// ---- repeated tokens ----
		// Set semantics: a repeat is not extra evidence. If tokens were counted
		// as a multiset, "JOHN JOHN" would clear the two-token floor against
		// any other John.
		{"repeated token both sides", "John John", "JOHN", NameMatchExact},
		{"repeated token inside a full name", "John John Smith", "JOHN SMITH", NameMatchExact},
		{"repeated token cannot clear the floor alone", "John John", "JOHN PETER", NameMatchNone},

		// ---- initials mixed with real tokens on both sides ----
		{"initial on each side", "Emmanuel O Ademu", "ADEMU E EMMANUEL", NameMatchExact},
		{"dotted initial on each side", "Emmanuel O. Ademu", "ADEMU E. EMMANUEL", NameMatchExact},
		{"compound dotted initials collapse", "Emmanuel Ademu", "ADEMU EMMANUEL E.O.", NameMatchExact},

		// ---- hyphens ----
		{"hyphenated on the kyc side", "Emmanuel Ademu-Okon", "EMMANUEL ADEMU OKON", NameMatchExact},
		{"hyphenated on both sides", "Emmanuel Ademu-Okon", "ADEMU-OKON EMMANUEL", NameMatchExact},
		{"spaced hyphen", "Emmanuel Ademu Okon", "EMMANUEL ADEMU - OKON", NameMatchExact},
		{"leading and trailing hyphen", "-Emmanuel Ademu-", "ADEMU EMMANUEL", NameMatchExact},
		{"hyphen never joins tokens", "Emmanuel Ademuokon", "EMMANUEL ADEMU-OKON", NameMatchNone},

		// ---- apostrophes are deleted, not split ----
		{"apostrophe deleted to match the plain spelling", "Michael O'Neill", "MICHAEL ONEILL", NameMatchExact},
		{"typographic apostrophe deleted too", "Michael O’Neill", "MICHAEL ONEILL", NameMatchExact},
		// Documented limitation: a bank that prints the apostrophe as a space
		// leaves NEILL where the KYC name has ONEILL. Held, never mis-bound.
		{"apostrophe respelled as a space does not match", "Michael O'Neill", "MICHAEL O NEILL", NameMatchNone},

		// ---- adversarial: near misses that must not bind ----
		// Two tokens shared but each side holds a token the other has never
		// seen. This is the sibling/relative shape, and the differing token is
		// exactly what tells them apart.
		{"overlap without containment", "Emmanuel Ademu Okon", "EMMANUEL ADEMU JOHN", NameMatchNone},
		{"same surname, different given names", "Emmanuel Ademu", "PETER ADEMU", NameMatchNone},
		{"same given name, different surnames", "Emmanuel Ademu", "EMMANUEL OKON", NameMatchNone},
		{"one shared token out of three each", "Emmanuel Ademu Okon", "EMMANUEL JOHN PETER", NameMatchNone},
		{"no shared tokens", "Emmanuel Ademu", "SEGUN ADEBAYO", NameMatchNone},
		{"substring is not a token match", "Emmanuel Ademu", "EMMANUELLA ADEMU", NameMatchNone},
		// Generational suffixes are not honorifics, so a father and son who
		// share a full name land on partial, not exact, and the caller's
		// single-candidate rule is what stops a wrong bind.
		{"generational suffix keeps the names distinguishable", "Emmanuel Ademu", "EMMANUEL ADEMU JR", NameMatchPartial},
		// A Cyrillic А is not a Latin A. Folding diacritics must not become
		// folding homoglyphs — that would be a forger's cheapest entry point.
		{"cyrillic homoglyph does not match", "Emmanuel Ademu", "\u0410DEMU EMMANUEL", NameMatchNone},

		// ---- real-world statement shapes ----
		{"messy remark with title, hyphen and suffix", "Emmanuel Ademu Okon", "  MR.  EMMANUEL   ADEMU-OKON,  JNR ", NameMatchPartial},
		{"comma-separated surname first", "Emmanuel Ademu", "ADEMU,EMMANUEL", NameMatchExact},
		{"honorific in the middle of the string", "Emmanuel Ademu", "EMMANUEL CHIEF ADEMU", NameMatchExact},
		{"business suffix on the bank side", "Emmanuel Ademu", "ADEMU EMMANUEL ENTERPRISES", NameMatchPartial},
		{"kyc carries two middle names", "Emmanuel Okon Chidi Ademu", "EMMANUEL ADEMU", NameMatchPartial},
		// Documented limitation: initials cannot stand in for the names they
		// abbreviate, so this legitimate transfer holds for an operator.
		{"initials cannot substitute for dropped names", "Emmanuel Okon Ademu", "E.O. ADEMU", NameMatchNone},

		// ---- very long names ----
		{"very long identical names", longName(60, false), longName(60, true), NameMatchExact},
		{"very long names differing by one token", longName(60, false), longName(59, false), NameMatchPartial},
		{"very long single token, equal", strings.Repeat("A", 500), strings.Repeat("A", 500), NameMatchExact},
		{"very long single token, different", strings.Repeat("A", 500), strings.Repeat("A", 499) + "B", NameMatchNone},
	}
}

func TestNameMatchAccountName(t *testing.T) {
	for _, tc := range nameMatchCases() {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchAccountName(tc.kyc, tc.bank); got != tc.want {
				t.Errorf("MatchAccountName(%q, %q) = %q, want %q\n  kyc tokens:  %v\n  bank tokens: %v",
					tc.kyc, tc.bank, got, tc.want,
					AccountNameTokens(tc.kyc), AccountNameTokens(tc.bank))
			}
		})
	}
}

// TestNameMatchIsSymmetric pins that argument order cannot change a verdict. The
// caller passes (kycName, bankName), but a future refactor that swaps them — or
// a second call site that reads the pair the other way round — must not be able
// to turn a hold into a credit.
func TestNameMatchIsSymmetric(t *testing.T) {
	for _, tc := range nameMatchCases() {
		t.Run(tc.name, func(t *testing.T) {
			forward := MatchAccountName(tc.kyc, tc.bank)
			reverse := MatchAccountName(tc.bank, tc.kyc)
			if forward != reverse {
				t.Errorf("asymmetric: (%q,%q)=%q but (%q,%q)=%q",
					tc.kyc, tc.bank, forward, tc.bank, tc.kyc, reverse)
			}
		})
	}
}

// TestNameMatchIsDeterministic guards the purity contract. The token sets are
// built in maps, whose iteration order is randomized per run, so a verdict that
// ever depended on iteration order would show up here.
func TestNameMatchIsDeterministic(t *testing.T) {
	for _, tc := range nameMatchCases() {
		first := MatchAccountName(tc.kyc, tc.bank)
		for i := 0; i < 200; i++ {
			if got := MatchAccountName(tc.kyc, tc.bank); got != first {
				t.Fatalf("%s: call %d returned %q, first call returned %q", tc.name, i, got, first)
			}
		}
	}
}

// TestNameMatchEmptySideNeverMatches states the empty-set guard as its own
// property rather than leaving it implied by table rows. Anything that
// normalizes to no name tokens — blank, punctuation, honorifics, initials — must
// be unattributable against every input, including itself.
func TestNameMatchEmptySideNeverMatches(t *testing.T) {
	empties := []string{"", " ", "\t\n", "...", "-", "'", ",,,", "MR", "Dr Mrs", "A", "A. B.", "‍"}
	others := append([]string{"Emmanuel Ademu", "Ademu", "EMMANUEL OKON ADEMU"}, empties...)

	for _, e := range empties {
		if toks := AccountNameTokens(e); len(toks) != 0 {
			t.Fatalf("AccountNameTokens(%q) = %v, want no tokens", e, toks)
		}
		for _, o := range others {
			if got := MatchAccountName(e, o); got != NameMatchNone {
				t.Errorf("MatchAccountName(%q, %q) = %q, want none", e, o, got)
			}
			if got := MatchAccountName(o, e); got != NameMatchNone {
				t.Errorf("MatchAccountName(%q, %q) = %q, want none", o, e, got)
			}
		}
	}
}

// TestNameMatchHonorificsAreStrippedAnywhere covers every title in the closed
// list, in leading, middle and trailing position. A title left in place on one
// side only would demote an exact match to partial, or a partial to none.
func TestNameMatchHonorificsAreStrippedAnywhere(t *testing.T) {
	titles := []string{"MR", "MRS", "MISS", "MS", "DR", "PROF", "CHIEF", "ALHAJI", "ALHAJA", "ENGR", "BARR", "REV", "PASTOR", "MALLAM"}
	for _, title := range titles {
		for _, shape := range []string{"%s EMMANUEL ADEMU", "EMMANUEL %s ADEMU", "EMMANUEL ADEMU %s", "%s. EMMANUEL ADEMU"} {
			bank := fmt.Sprintf(shape, title)
			if got := MatchAccountName("Emmanuel Ademu", bank); got != NameMatchExact {
				t.Errorf("MatchAccountName(%q, %q) = %q, want exact", "Emmanuel Ademu", bank, got)
			}
		}
	}
}

// TestNameMatchUnlistedTitlesAreNotStripped pins the list closed. Words that
// look like titles but are not on it stay as name tokens, which keeps them
// distinguishing. Widening the list is a loosening of the matcher and must be a
// deliberate edit, visible in this test.
func TestNameMatchUnlistedTitlesAreNotStripped(t *testing.T) {
	for _, word := range []string{"HON", "SIR", "EVANG", "APOSTLE", "CAPT", "JR", "SNR"} {
		bank := "EMMANUEL ADEMU " + word
		if got := MatchAccountName("Emmanuel Ademu", bank); got != NameMatchPartial {
			t.Errorf("MatchAccountName(%q, %q) = %q, want partial (%q must stay a name token)",
				"Emmanuel Ademu", bank, got, word)
		}
	}
}

// TestNameMatchTwoTokenFloor isolates the rule that stops a shared given name
// from moving money. Every pair here overlaps on exactly one token.
func TestNameMatchTwoTokenFloor(t *testing.T) {
	pairs := [][2]string{
		{"Emmanuel John", "EMMANUEL PETER"},
		{"Emmanuel Ademu Okon", "EMMANUEL SEGUN ADEBAYO"},
		{"Chidinma Okoro", "CHIDINMA ADEBAYO"},
		{"Emmanuel Ademu", "ADEMU PETER"},
	}
	for _, p := range pairs {
		if got := MatchAccountName(p[0], p[1]); got != NameMatchNone {
			t.Errorf("MatchAccountName(%q, %q) = %q, want none (intersection is 1)", p[0], p[1], got)
		}
	}
}

// TestNameMatchAccountNameTokens pins the normalization the admin hold queue
// renders, so an operator sees the same view of a name the matcher used.
func TestNameMatchAccountNameTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"Emmanuel Ademu", []string{"ADEMU", "EMMANUEL"}},
		{"MR. EMMANUEL ADEMU-OKON O.", []string{"ADEMU", "EMMANUEL", "OKON"}},
		{"Adémù", []string{"ADEMU"}},
		{"John John", []string{"JOHN"}},
		{"", nil},
	} {
		got := AccountNameTokens(tc.in)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("AccountNameTokens(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNameMatchStrengthIsMatch pins which verdicts bind money. "none" must never
// creep into the accepting arm.
func TestNameMatchStrengthIsMatch(t *testing.T) {
	for s, want := range map[NameMatchStrength]bool{
		NameMatchExact:   true,
		NameMatchPartial: true,
		NameMatchNone:    false,
		"":               false,
		"probably":       false,
	} {
		if got := s.IsMatch(); got != want {
			t.Errorf("NameMatchStrength(%q).IsMatch() = %v, want %v", s, got, want)
		}
	}
}

// FuzzNameMatchAccountName asserts the invariants that must hold for any input,
// including bytes no bank would ever print: the function never panics, is always
// symmetric, only ever returns one of the three verdicts, and never binds when
// either side carries no name tokens.
func FuzzNameMatchAccountName(f *testing.F) {
	for _, tc := range nameMatchCases() {
		f.Add(tc.kyc, tc.bank)
	}
	f.Add("\xff\xfe", "ADEMU")
	f.Add("ADEMU\x00EMMANUEL", "ADEMU EMMANUEL")

	f.Fuzz(func(t *testing.T, kyc, bank string) {
		got := MatchAccountName(kyc, bank)
		switch got {
		case NameMatchNone, NameMatchPartial, NameMatchExact:
		default:
			t.Fatalf("MatchAccountName(%q, %q) returned unknown verdict %q", kyc, bank, got)
		}
		if rev := MatchAccountName(bank, kyc); rev != got {
			t.Fatalf("asymmetric on (%q, %q): %q vs %q", kyc, bank, got, rev)
		}
		if len(AccountNameTokens(kyc)) == 0 || len(AccountNameTokens(bank)) == 0 {
			if got != NameMatchNone {
				t.Fatalf("MatchAccountName(%q, %q) = %q with an empty token set", kyc, bank, got)
			}
		}
	})
}
