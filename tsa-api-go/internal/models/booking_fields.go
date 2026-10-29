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
	// Listing attributes need these two; booking forms may use them too.
	BookingFieldBoolean     = "boolean"     // yes/no
	BookingFieldMultiselect = "multiselect" // any subset of Options
)

// Field groups — how the builder/form organizes fields.
const (
	BookingGroupLocation       = "location"
	BookingGroupDateTime       = "datetime"
	BookingGroupPeople         = "people"
	BookingGroupServiceOptions = "serviceOptions"
	FieldGroupDetails          = "details" // listing attributes (institution, amenities, …)
)

// ValidFieldType reports whether t is a known field type.
func ValidFieldType(t string) bool { return validFieldType(t) }

// fieldTypeHasOptions reports whether a field of type t picks from Options.
func fieldTypeHasOptions(t string) bool {
	return t == BookingFieldSelect || t == BookingFieldMultiselect
}

// validFieldType reports whether t is a known field type.
func validFieldType(t string) bool {
	switch t {
	case BookingFieldLocation, BookingFieldDate, BookingFieldTime, BookingFieldDateTime,
		BookingFieldCounter, BookingFieldSelect, BookingFieldText, BookingFieldTextarea, BookingFieldNumber,
		BookingFieldBoolean, BookingFieldMultiselect:
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

// BookingFieldCatalog is the built-in set of booking fields. It is the SEED for
// the field_defs table (see field_def.go), which is now the live catalogue: the
// admin can add fields there, and validation reads it. The merchant builder
// renders these as toggles; enabling one copies it into FieldConfig.
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
	Options  []string `json:"options,omitempty"` // for select / multiselect
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

// ValidateFieldConfig checks a merchant-submitted config against the built-in
// catalogue. Request paths use ValidateFieldConfigAgainst with the live
// (field_defs) catalogue so admin-added fields are accepted.
func ValidateFieldConfig(cfg *BookingFieldConfig) error {
	return ValidateFieldConfigAgainst(cfg, BuiltinFieldCatalog())
}

// ValidateFieldConfigAgainst checks a merchant-submitted booking config: known
// types, non-empty keys, select/multiselect fields carry options, and non-custom
// keys exist in catalog as active fields usable on a booking form. Returns the
// first problem found.
func ValidateFieldConfigAgainst(cfg *BookingFieldConfig, catalog FieldCatalog) error {
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
			def, ok := catalog[key]
			if !ok {
				return fmt.Errorf("field %q is not in the catalog (mark it custom to allow)", key)
			}
			if !def.Scope.Allows(FieldUsageBooking) {
				return fmt.Errorf("field %q cannot be used on a booking form", key)
			}
		}
		if fieldTypeHasOptions(f.Type) && len(f.Options) == 0 {
			return fmt.Errorf("%s field %q needs at least one option", f.Type, key)
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
// required fields present, select values within options, multiselect values a
// list of options, boolean a bool, counter/number numeric.
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
		case BookingFieldBoolean:
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("%s must be yes or no", answerLabel(f))
			}
		case BookingFieldMultiselect:
			if !isOptionList(raw, f.Options) {
				return fmt.Errorf("%s must be a list of the provided options", answerLabel(f))
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
	case []interface{}:
		return len(t) == 0
	}
	return false
}

// isOptionList reports whether v is a JSON array of distinct strings, each one
// of options.
func isOptionList(v interface{}, options []string) bool {
	items, ok := v.([]interface{})
	if !ok {
		return false
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || seen[s] || !containsString(options, s) {
			return false
		}
		seen[s] = true
	}
	return true
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
