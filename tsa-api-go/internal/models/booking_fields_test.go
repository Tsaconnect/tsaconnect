package models

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidateFieldConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *BookingFieldConfig
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"valid catalog fields", &BookingFieldConfig{Fields: []BookingField{
			{Key: "checkin", Type: BookingFieldDate, Label: "Check-in", Required: true},
			{Key: "room_type", Type: BookingFieldSelect, Label: "Room", Options: []string{"Single", "Double"}},
		}}, false},
		{"empty key", &BookingFieldConfig{Fields: []BookingField{{Key: " ", Type: BookingFieldText}}}, true},
		{"duplicate key", &BookingFieldConfig{Fields: []BookingField{
			{Key: "guests", Type: BookingFieldCounter}, {Key: "guests", Type: BookingFieldCounter},
		}}, true},
		{"unknown type", &BookingFieldConfig{Fields: []BookingField{{Key: "guests", Type: "slider"}}}, true},
		{"non-catalog non-custom rejected", &BookingFieldConfig{Fields: []BookingField{{Key: "made_up", Type: BookingFieldText}}}, true},
		{"custom field allowed", &BookingFieldConfig{Fields: []BookingField{{Key: "made_up", Type: BookingFieldText, Custom: true}}}, false},
		{"select without options", &BookingFieldConfig{Fields: []BookingField{{Key: "room_type", Type: BookingFieldSelect}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFieldConfig(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateFieldConfig() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAnswers(t *testing.T) {
	cfg := &BookingFieldConfig{Fields: []BookingField{
		{Key: "checkin", Type: BookingFieldDate, Label: "Check-in", Required: true},
		{Key: "guests", Type: BookingFieldCounter, Label: "Guests", Required: true},
		{Key: "room_type", Type: BookingFieldSelect, Label: "Room", Options: []string{"Single", "Double"}},
		{Key: "notes", Type: BookingFieldTextarea, Label: "Notes"},
	}}
	cases := []struct {
		name    string
		answers map[string]interface{}
		wantErr bool
	}{
		{"all good", map[string]interface{}{"checkin": "2026-08-01", "guests": float64(2), "room_type": "Double"}, false},
		{"missing required", map[string]interface{}{"guests": float64(2)}, true},
		{"empty required string", map[string]interface{}{"checkin": "  ", "guests": float64(2)}, true},
		{"counter not numeric", map[string]interface{}{"checkin": "2026-08-01", "guests": "two"}, true},
		{"select out of options", map[string]interface{}{"checkin": "2026-08-01", "guests": float64(1), "room_type": "Suite"}, true},
		{"optional select omitted", map[string]interface{}{"checkin": "2026-08-01", "guests": float64(1)}, false},
		{"unknown answer key ignored", map[string]interface{}{"checkin": "2026-08-01", "guests": float64(1), "bogus": "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAnswers(cfg, tc.answers)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateAnswers() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAnswers_PayloadBounds(t *testing.T) {
	// Bounds apply even with no config (answers are stored regardless).
	tooMany := map[string]interface{}{}
	for i := 0; i <= maxAnswerKeys; i++ {
		tooMany["field"+strconv.Itoa(i)] = "x"
	}
	if err := ValidateAnswers(nil, tooMany); err == nil {
		t.Error("expected error for too many answer keys")
	}

	if err := ValidateAnswers(nil, map[string]interface{}{"notes": strings.Repeat("x", maxAnswerStringLen+1)}); err == nil {
		t.Error("expected error for over-long answer string")
	}

	// A reasonable payload with no config passes.
	if err := ValidateAnswers(nil, map[string]interface{}{"notes": "fine"}); err != nil {
		t.Errorf("expected nil for small payload, got %v", err)
	}
}

func TestValidateAnswers_BooleanAndMultiselect(t *testing.T) {
	cfg := &BookingFieldConfig{Fields: []BookingField{
		{Key: "parking", Type: BookingFieldBoolean, Label: "Parking", Required: true},
		{Key: "amenities", Type: BookingFieldMultiselect, Label: "Amenities", Required: true, Options: []string{"wifi", "water"}},
	}}
	cases := []struct {
		name    string
		answers map[string]interface{}
		wantErr bool
	}{
		{"valid", map[string]interface{}{"parking": false, "amenities": []interface{}{"wifi", "water"}}, false},
		{"boolean as string", map[string]interface{}{"parking": "yes", "amenities": []interface{}{"wifi"}}, true},
		{"multiselect not a list", map[string]interface{}{"parking": true, "amenities": "wifi"}, true},
		{"multiselect unknown option", map[string]interface{}{"parking": true, "amenities": []interface{}{"pool"}}, true},
		{"multiselect repeated option", map[string]interface{}{"parking": true, "amenities": []interface{}{"wifi", "wifi"}}, true},
		{"required multiselect empty", map[string]interface{}{"parking": true, "amenities": []interface{}{}}, true},
		{"required boolean missing", map[string]interface{}{"amenities": []interface{}{"wifi"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateAnswers(cfg, tc.answers); (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateFieldConfigAgainst_DBCatalogue(t *testing.T) {
	catalog := BuiltinFieldCatalog()
	catalog["pet_friendly"] = FieldDef{Key: "pet_friendly", Type: BookingFieldBoolean, Label: "Pets", Scope: FieldScopeBoth, IsActive: true}

	// An admin-added catalogue key is a catalogue key, not "custom".
	if err := ValidateFieldConfigAgainst(&BookingFieldConfig{Fields: []BookingField{
		{Key: "pet_friendly", Type: BookingFieldBoolean, Label: "Pets"},
	}}, catalog); err != nil {
		t.Fatalf("DB-defined key rejected: %v", err)
	}
	// A listing-only field cannot sit on a booking form unless marked custom.
	if err := ValidateFieldConfigAgainst(&BookingFieldConfig{Fields: []BookingField{
		{Key: "brand", Type: BookingFieldText, Label: "Brand"},
	}}, catalog); err == nil {
		t.Fatal("listing-scope field accepted on a booking form")
	}
	// Multiselect needs options like select.
	if err := ValidateFieldConfigAgainst(&BookingFieldConfig{Fields: []BookingField{
		{Key: "extras", Type: BookingFieldMultiselect, Custom: true},
	}}, catalog); err == nil {
		t.Fatal("multiselect without options accepted")
	}
}
