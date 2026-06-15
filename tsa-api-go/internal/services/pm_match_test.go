package services

import "testing"

func TestMatchesPayerName(t *testing.T) {
	cases := []struct {
		sender, kyc string
		want        bool
		note        string
	}{
		{"ADEYEMI JOHN OLU", "John Adeyemi", true, "reordered, extra middle name"},
		{"John A. Doe", "JOHN DOE", true, "middle initial dropped"},
		{"Doe John", "John Doe", true, "reordered two tokens"},
		{"JANE SMITH", "John Doe", false, "different person"},
		{"John", "John Doe", false, "only first name matches (need 2)"},
		{"John Doe Holdings Ltd", "John Doe", true, "extra business tokens, both names present"},
		{"Mary", "Mary", true, "single-token exact"},
		{"Mary", "Maria", false, "single-token mismatch"},
		{"", "John Doe", false, "empty sender"},
		{"John Doe", "", false, "empty kyc"},
	}
	for _, c := range cases {
		if got := MatchesPayerName(c.sender, c.kyc); got != c.want {
			t.Errorf("MatchesPayerName(%q, %q) = %v, want %v (%s)", c.sender, c.kyc, got, c.want, c.note)
		}
	}
}
