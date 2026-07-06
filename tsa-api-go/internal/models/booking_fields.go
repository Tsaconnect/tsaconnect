package models

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Unified booking-engine field model.
//
// A service's customer booking form is generated from a declarative
// BookingFieldConfig stored on ServiceProfile.FieldConfig. Each field's Type
// maps 1:1 to a client-side UI primitive; the server validates submitted
// answers against the config. The BookingFieldCatalog is the canonical set of
// known fields the PM spec enumerates (flight/hotel/taxi/salon/event) — the
// merchant toggles a subset on; custom (non-catalog) fields are allowed but
// flagged so pricing/slot logic can ignore them.

// Field types — the renderer/validator vocabulary.
const (
	BookingFieldLocation = "location"
	BookingFieldDate     = "date"
	BookingFieldTime     = "time"
	BookingFieldDateTime = "datetime"
	BookingFieldCounter  = "counter" // integer stepper (adults, guests, …)
	BookingFieldSelect   = "select"  // one of Options
	BookingFieldText     = "text"
	BookingFieldTextarea = "textarea"
	BookingFieldNumber   = "number" // free numeric (budget, …)
)

// Field groups — how the builder/form organizes fields.
const (
	BookingGroupLocation       = "location"
	BookingGroupDateTime       = "datetime"
	BookingGroupPeople         = "people"
	BookingGroupServiceOptions = "serviceOptions"
)

// validFieldType reports whether t is a known field type.
func validFieldType(t string) bool {
	switch t {
	case BookingFieldLocation, BookingFieldDate, BookingFieldTime, BookingFieldDateTime,
		BookingFieldCounter, BookingFieldSelect, BookingFieldText, BookingFieldTextarea, BookingFieldNumber:
		return true
	}
	return false
}

// BookingFieldDef is a catalog entry: a known field's canonical type/label/group.
type BookingFieldDef struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Group string `json:"group"`
}

// BookingFieldCatalog is the canonical set of known booking fields. The merchant
// builder renders these as toggles; enabling one copies it into FieldConfig.
var BookingFieldCatalog = []BookingFieldDef{
	// Location
	{"pickup_location", BookingFieldLocation, "Pickup", BookingGroupLocation},
	{"dropoff_location", BookingFieldLocation, "Drop-off", BookingGroupLocation},
	{"departure", BookingFieldLocation, "From", BookingGroupLocation},
	{"destination", BookingFieldLocation, "To", BookingGroupLocation},
	// Date & time
	{"travel_date", BookingFieldDate, "Travel date", BookingGroupDateTime},
	{"return_date", BookingFieldDate, "Return date", BookingGroupDateTime},
	{"checkin", BookingFieldDate, "Check-in", BookingGroupDateTime},
	{"checkout", BookingFieldDate, "Check-out", BookingGroupDateTime},
	{"appointment", BookingFieldDateTime, "Appointment", BookingGroupDateTime},
	{"pickup_time", BookingFieldDateTime, "Pickup time", BookingGroupDateTime},
	// People
	{"adults", BookingFieldCounter, "Adults", BookingGroupPeople},
	{"children", BookingFieldCounter, "Children", BookingGroupPeople},
	{"infants", BookingFieldCounter, "Infants", BookingGroupPeople},
	{"guests", BookingFieldCounter, "Guests", BookingGroupPeople},
	{"passengers", BookingFieldCounter, "Passengers", BookingGroupPeople},
	{"attendees", BookingFieldCounter, "Attendees", BookingGroupPeople},
	// Service options
	{"vehicle_type", BookingFieldSelect, "Vehicle type", BookingGroupServiceOptions},
	{"airline", BookingFieldSelect, "Preferred airline", BookingGroupServiceOptions},
	{"cabin_class", BookingFieldSelect, "Cabin class", BookingGroupServiceOptions},
	{"room_type", BookingFieldSelect, "Room type", BookingGroupServiceOptions},
	{"staff", BookingFieldSelect, "Staff preference", BookingGroupServiceOptions},
	{"budget", BookingFieldNumber, "Budget", BookingGroupServiceOptions},
	{"special_requests", BookingFieldTextarea, "Special requests", BookingGroupServiceOptions},
}

// catalogByKey indexes the catalog for O(1) lookup during validation.
var catalogByKey = func() map[string]BookingFieldDef {
	m := make(map[string]BookingFieldDef, len(BookingFieldCatalog))
	for _, d := range BookingFieldCatalog {
		m[d.Key] = d
	}
	return m
}()

// BookingFieldPresets are the PM's per-example starting field sets. The merchant
// builder can apply one to pre-check the common toggles for a category.
var BookingFieldPresets = map[string][]string{
	"flight": {"departure", "destination", "travel_date", "return_date", "passengers", "cabin_class"},
	"hotel":  {"checkin", "checkout", "guests", "room_type", "special_requests"},
	"taxi":   {"pickup_location", "destination", "pickup_time", "vehicle_type"},
	"salon":  {"appointment", "staff", "special_requests"},
	"event":  {"travel_date", "attendees"},
}

// BookingField is one enabled field in a service's FieldConfig.
type BookingField struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"`
	Label    string   `json:"label"`
	Group    string   `json:"group,omitempty"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"` // for select
	Custom   bool     `json:"custom,omitempty"`  // true = not a catalog key (informational only)
}

// BookingFieldConfig is the declarative set of enabled customer-form fields.
type BookingFieldConfig struct {
	Fields []BookingField `json:"fields"`
}

// SetFieldConfig serializes cfg into the ServiceProfile JSONB column. A nil or
// empty config clears it (→ legacy per-serviceType flow).
func (p *ServiceProfile) SetFieldConfig(cfg *BookingFieldConfig) {
	if cfg == nil || len(cfg.Fields) == 0 {
		p.FieldConfig = nil
		return
	}
	data, _ := json.Marshal(cfg)
	p.FieldConfig = data
}

// GetFieldConfig deserializes the JSONB column, or nil if unset.
func (p *ServiceProfile) GetFieldConfig() *BookingFieldConfig {
	if len(p.FieldConfig) == 0 {
		return nil
	}
	var cfg BookingFieldConfig
	if err := json.Unmarshal(p.FieldConfig, &cfg); err != nil {
		return nil
	}
	return &cfg
}

// ValidateFieldConfig checks a merchant-submitted config: known/valid types,
// non-empty keys, select fields carry options, and non-custom keys exist in the
// catalog. Returns the first problem found.
func ValidateFieldConfig(cfg *BookingFieldConfig) error {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, f := range cfg.Fields {
		key := strings.TrimSpace(f.Key)
		if key == "" {
			return fmt.Errorf("field key is required")
		}
		if seen[key] {
			return fmt.Errorf("duplicate field key %q", key)
		}
		seen[key] = true
		if !validFieldType(f.Type) {
			return fmt.Errorf("field %q has unknown type %q", key, f.Type)
		}
		if !f.Custom {
			if _, ok := catalogByKey[key]; !ok {
				return fmt.Errorf("field %q is not in the catalog (mark it custom to allow)", key)
			}
		}
		if f.Type == BookingFieldSelect && len(f.Options) == 0 {
			return fmt.Errorf("select field %q needs at least one option", key)
		}
	}
	return nil
}

// Bounds on a submitted answers payload — enforced regardless of the field
// config so a crafted client can't persist an unbounded JSONB blob (answers are
// stored even for services with no FieldConfig).
const (
	maxAnswerKeys      = 50
	maxAnswerStringLen = 2000
)

// ValidateAnswers checks a buyer's submitted answers against the field config:
// required fields present, select values within options, counter/number numeric.
// Unknown answer keys (not in the config) are ignored. Lenient on optional
// fields. The payload size bounds are enforced even when cfg is nil. Returns the
// first problem found.
func ValidateAnswers(cfg *BookingFieldConfig, answers map[string]interface{}) error {
	if len(answers) > maxAnswerKeys {
		return fmt.Errorf("too many answer fields (max %d)", maxAnswerKeys)
	}
	for k, v := range answers {
		if s, ok := v.(string); ok && len(s) > maxAnswerStringLen {
			return fmt.Errorf("answer %q is too long (max %d characters)", k, maxAnswerStringLen)
		}
	}
	if cfg == nil {
		return nil
	}
	for _, f := range cfg.Fields {
		raw, present := answers[f.Key]
		if !present || isEmptyAnswer(raw) {
			if f.Required {
				label := f.Label
				if label == "" {
					label = f.Key
				}
				return fmt.Errorf("%s is required", label)
			}
			continue
		}
		switch f.Type {
		case BookingFieldCounter, BookingFieldNumber:
			if !isNumericAnswer(raw) {
				return fmt.Errorf("%s must be a number", answerLabel(f))
			}
		case BookingFieldSelect:
			s, ok := raw.(string)
			if !ok || !containsString(f.Options, s) {
				return fmt.Errorf("%s must be one of the provided options", answerLabel(f))
			}
		}
	}
	return nil
}

func answerLabel(f BookingField) string {
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}

func isEmptyAnswer(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	}
	return false
}

func isNumericAnswer(v interface{}) bool {
	switch v.(type) {
	case float64, float32, int, int64:
		return true
	}
	return false
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
