package services

import (
	"encoding/json"
	"testing"
)

func TestParseFWPhone(t *testing.T) {
	cases := []struct {
		in, code, num string
		ok            bool
	}{
		{"+2348012345678", "234", "8012345678", true},
		{"2348012345678", "234", "8012345678", true},
		{"08012345678", "234", "8012345678", true},
		{"8012345678", "234", "8012345678", true},
		{"+234 801 234 5678", "234", "8012345678", true},
		{"", "", "", false},
		{"not-a-phone", "", "", false},
		{"12345", "", "", false},
	}
	for _, c := range cases {
		got := ParseFWPhone(c.in)
		if !c.ok {
			if got != nil {
				t.Fatalf("ParseFWPhone(%q) = %+v, want nil", c.in, got)
			}
			continue
		}
		if got == nil || got.CountryCode != c.code || got.Number != c.num {
			t.Fatalf("ParseFWPhone(%q) = %+v, want {%s %s}", c.in, got, c.code, c.num)
		}
	}
}

func TestFWCustomerRequest_MarshalsNestedPhoneNoIDFields(t *testing.T) {
	raw, err := json.Marshal(FWCustomerRequest{
		Email: "a@b.co",
		Name:  &FWName{First: "Ada", Last: "Lovelace"},
		Phone: ParseFWPhone("+2348012345678"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["id_type"]; ok {
		t.Fatalf("id_type must not be sent on /customers: %s", raw)
	}
	if _, ok := m["id_number"]; ok {
		t.Fatalf("id_number must not be sent on /customers: %s", raw)
	}
	phone, ok := m["phone"].(map[string]any)
	if !ok {
		t.Fatalf("phone must be object, got %T in %s", m["phone"], raw)
	}
	if phone["country_code"] != "234" || phone["number"] != "8012345678" {
		t.Fatalf("phone = %+v", phone)
	}
}
