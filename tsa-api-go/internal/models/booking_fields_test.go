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
