package models

import "testing"

func TestParseListingLocation_ReadsFromTheEnd(t *testing.T) {
	cases := []struct {
		in   string
		want ListingLocation
		ok   bool
	}{
		{"Nsukka, Enugu State, Nigeria", ListingLocation{City: "Nsukka", State: "Enugu State", Country: "Nigeria"}, true},
		{"Enugu State, Nigeria", ListingLocation{State: "Enugu State", Country: "Nigeria"}, true},
		// A typed city with its own commas stays whole.
		{"Dutse , Kubwa extension Abuja., Federal Capital Territory, Nigeria",
			ListingLocation{City: "Dutse, Kubwa extension Abuja.", State: "Federal Capital Territory", Country: "Nigeria"}, true},
		{"Kubwa  Abuja , Federal Capital Territory, Nigeria",
			ListingLocation{City: "Kubwa Abuja", State: "Federal Capital Territory", Country: "Nigeria"}, true},
		// No comma: cannot tell the parts apart, so it stays text.
		{"Enugu Nigeria", ListingLocation{}, false},
		{" , ", ListingLocation{}, false},
		{"", ListingLocation{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseListingLocation(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseListingLocation(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLocationKey_IgnoresCaseSpacingAndStateSuffix(t *testing.T) {
	for _, in := range []string{"Enugu", "enugu state", "  Enugu   State. ", "ENUGU"} {
		if got := LocationKey(in); got != "enugu" {
			t.Errorf("LocationKey(%q) = %q, want enugu", in, got)
		}
	}
	if got := LocationKey("Federal Capital Territory"); got != "federal capital territory" {
		t.Errorf("FCT key = %q", got)
	}
}

func TestSetListingLocation_FormatsDisplayWhenNoneGiven(t *testing.T) {
	var p Product
	p.SetListingLocation(ListingLocation{Country: "Nigeria", State: "Enugu State", City: "Nsukka", Area: " Odenigbo "}, "")
	if p.Location != "Nsukka, Enugu State, Nigeria" || p.Area != "Odenigbo" || p.StateKey != "enugu" || p.CityKey != "nsukka" {
		t.Fatalf("got %+v", p)
	}
	p.SetListingLocation(ListingLocation{Country: "Nigeria"}, "Somewhere, Nigeria")
	if p.Location != "Somewhere, Nigeria" || p.City != "" || p.CityKey != "" {
		t.Fatalf("display kept and stale parts cleared: %+v", p)
	}
}
