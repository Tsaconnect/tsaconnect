package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func hostelForm() []FormField {
	field := func(key, typ string, required, filterable bool, options ...string) FormField {
		return FormField{
			BookingField: BookingField{Key: key, Type: typ, Label: key, Required: required, Options: options},
			Filterable:   filterable,
		}
	}
	return []FormField{
		field("institution", BookingFieldSelect, true, true, "UNN", "UNILAG"),
		field("gender", BookingFieldSelect, false, true, "male", "female", "mixed"),
		field("amenities", BookingFieldMultiselect, false, true, "wifi", "water", "light"),
		field("has_curfew", BookingFieldBoolean, false, true),
		field("distance_to_campus", BookingFieldNumber, false, false),
	}
}

// decode mimics what the handler gets from the form: a JSON object.
func decode(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidateListingValues_KeepsOnlyAnswersTheFormAsksFor(t *testing.T) {
	got, err := ValidateListingValues(hostelForm(), decode(t,
		`{"institution":"UNN","gender":"","amenities":["wifi"],"has_curfew":true,"retired_field":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"institution": "UNN", "amenities": []interface{}{"wifi"}, "has_curfew": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestValidateListingValues_Refuses(t *testing.T) {
	cases := map[string]string{
		"missing required":       `{"gender":"male"}`,
		"select outside options": `{"institution":"UI"}`,
		"multiselect bad option": `{"institution":"UNN","amenities":["pool"]}`,
		"boolean as string":      `{"institution":"UNN","has_curfew":"yes"}`,
		"number as text":         `{"institution":"UNN","distance_to_campus":"near"}`,
	}
	for name, body := range cases {
		if _, err := ValidateListingValues(hostelForm(), decode(t, body)); err == nil {
			t.Errorf("%s: accepted %s", name, body)
		}
	}
}

func TestListingValueFilter(t *testing.T) {
	got, err := ListingValueFilter(hostelForm(), map[string]string{
		"institution":        "UNN",
		"amenities":          "wifi",
		"has_curfew":         "false",
		"distance_to_campus": "2", // not filterable: ignored
		"made_up":            "x", // not on the form: ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(got), &m)
	want := map[string]interface{}{"institution": "UNN", "amenities": []interface{}{"wifi"}, "has_curfew": false}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}

	if got, _ := ListingValueFilter(hostelForm(), map[string]string{"made_up": "x"}); got != "" {
		t.Fatalf("nothing filterable should give no filter, got %q", got)
	}
	if _, err := ListingValueFilter(hostelForm(), map[string]string{"has_curfew": "maybe"}); err == nil {
		t.Fatal("a non-boolean boolean filter was accepted")
	}
}
