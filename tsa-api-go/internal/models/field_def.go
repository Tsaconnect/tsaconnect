package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Field catalogue (DB).
//
// field_defs is the live catalogue of fields a category can put on its listing
// form (merchant-entered listing attributes) or its default booking form. It is
// seeded from the built-in Go catalogue (BuiltinFieldDefs) per row and never
// overwritten, so an admin's edit to a seeded field survives every deploy. An
// admin can add a field ("Add custom field"); it joins the catalogue for any
// category to reuse. A referenced field is deactivated, never deleted.
//
// Merchant per-listing booking fields marked Custom on ServiceProfile.FieldConfig
// are unrelated to this table and keep working as before.

// FieldScope says which forms a field may appear on.
type FieldScope string

const (
	FieldScopeListing FieldScope = "listing"
	FieldScopeBooking FieldScope = "booking"
	FieldScopeBoth    FieldScope = "both"
)

// Field usages: the two forms a category configures.
const (
	FieldUsageListing = "listing"
	FieldUsageBooking = "booking"
)

// Valid reports whether s is a known scope.
func (s FieldScope) Valid() bool {
	return s == FieldScopeListing || s == FieldScopeBooking || s == FieldScopeBoth
}

// Allows reports whether a field of this scope may be used for usage
// (FieldUsageListing or FieldUsageBooking).
func (s FieldScope) Allows(usage string) bool {
	return s == FieldScopeBoth || string(s) == usage
}

// FieldDef is one catalogue entry.
type FieldDef struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Key   string    `gorm:"size:64;not null;uniqueIndex" json:"key"`
	Type  string    `gorm:"size:16;not null" json:"type"`
	Label string    `gorm:"not null" json:"label"`
	// "group" is an SQL keyword; the column is field_group.
	Group     string                      `gorm:"column:field_group;size:32" json:"group"`
	Options   datatypes.JSONSlice[string] `gorm:"type:jsonb" json:"options"`
	Scope     FieldScope                  `gorm:"size:8;not null" json:"scope"`
	IsSystem  bool                        `gorm:"not null;default:false" json:"isSystem"`
	IsActive  bool                        `gorm:"not null;default:true" json:"isActive"`
	CreatedBy *uuid.UUID                  `gorm:"type:uuid" json:"createdBy,omitempty"`
	CreatedAt time.Time                   `json:"createdAt"`
	UpdatedAt time.Time                   `json:"updatedAt"`
}

// TableName overrides the default table name.
func (FieldDef) TableName() string { return "field_defs" }

// FieldCatalog indexes catalogue entries by key.
type FieldCatalog map[string]FieldDef

// fieldKeyPattern is a lowercase snake_case slug, like the built-in keys.
var fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// ValidFieldKey reports whether k is a well-formed catalogue key.
func ValidFieldKey(k string) bool { return fieldKeyPattern.MatchString(k) }

// ValidateFieldDef checks a catalogue entry an admin is writing.
func ValidateFieldDef(d *FieldDef) error {
	if !ValidFieldKey(d.Key) {
		return fmt.Errorf("key must be lowercase letters, digits and underscores, starting with a letter (2-64 chars)")
	}
	if !ValidFieldType(d.Type) {
		return fmt.Errorf("unknown field type %q", d.Type)
	}
	if strings.TrimSpace(d.Label) == "" {
		return fmt.Errorf("label is required")
	}
	if !d.Scope.Valid() {
		return fmt.Errorf("scope must be listing, booking or both")
	}
	if len(d.Options) > 0 && !fieldTypeHasOptions(d.Type) {
		return fmt.Errorf("options apply to select and multiselect fields only")
	}
	return validateOptionList(d.Options)
}

func validateOptionList(options []string) error {
	seen := make(map[string]bool, len(options))
	for _, o := range options {
		if strings.TrimSpace(o) == "" {
			return fmt.Errorf("options cannot be blank")
		}
		if seen[o] {
			return fmt.Errorf("duplicate option %q", o)
		}
		seen[o] = true
	}
	return nil
}

// listingFieldSeed are the built-in listing attributes (spec 2026-10-28 §1).
var listingFieldSeed = []FieldDef{
	{Key: "institution", Type: BookingFieldSelect, Label: "Institution", Group: FieldGroupDetails},
	{Key: "gender", Type: BookingFieldSelect, Label: "Gender", Group: FieldGroupDetails,
		Options: datatypes.JSONSlice[string]{"male", "female", "mixed"}},
	{Key: "amenities", Type: BookingFieldMultiselect, Label: "Amenities", Group: FieldGroupDetails},
	{Key: "distance_to_campus", Type: BookingFieldNumber, Label: "Distance to campus (km)", Group: FieldGroupDetails},
	{Key: "years_experience", Type: BookingFieldNumber, Label: "Years of experience", Group: FieldGroupDetails},
	{Key: "brand", Type: BookingFieldText, Label: "Brand", Group: FieldGroupDetails},
}

// bothScopeKeys are built-in booking fields that also describe a listing.
var bothScopeKeys = map[string]bool{"room_type": true}

// BuiltinFieldDefs is the seed for field_defs: every BookingFieldCatalog entry
// (scope booking, or both for keys that also describe a listing) plus the
// built-in listing attributes. All are system fields.
func BuiltinFieldDefs() []FieldDef {
	defs := make([]FieldDef, 0, len(BookingFieldCatalog)+len(listingFieldSeed))
	for _, b := range BookingFieldCatalog {
		scope := FieldScopeBooking
		if bothScopeKeys[b.Key] {
			scope = FieldScopeBoth
		}
		defs = append(defs, FieldDef{Key: b.Key, Type: b.Type, Label: b.Label, Group: b.Group, Scope: scope})
	}
	for _, l := range listingFieldSeed {
		l.Scope = FieldScopeListing
		defs = append(defs, l)
	}
	for i := range defs {
		defs[i].IsSystem = true
		defs[i].IsActive = true
	}
	return defs
}

// BuiltinFieldCatalog is the seed as a catalogue — what validation falls back
// to where field_defs cannot be read.
func BuiltinFieldCatalog() FieldCatalog {
	return NewFieldCatalog(BuiltinFieldDefs())
}

// NewFieldCatalog indexes defs by key.
func NewFieldCatalog(defs []FieldDef) FieldCatalog {
	m := make(FieldCatalog, len(defs))
	for _, d := range defs {
		m[d.Key] = d
	}
	return m
}

// SeedFieldDefs inserts each built-in field whose key is not yet in field_defs.
// A row that exists is never touched, so an admin's label/options/active edit
// to a system field survives every boot. Idempotent.
func SeedFieldDefs(db *gorm.DB) error {
	defs := BuiltinFieldDefs()
	now := time.Now()
	for i := range defs {
		defs[i].ID = uuid.New()
		defs[i].CreatedAt, defs[i].UpdatedAt = now, now
	}
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).
		Create(&defs).Error
}

// LoadFieldCatalog reads the ACTIVE catalogue. An inactive field is unknown to
// new configs and is dropped from forms that still reference it.
func LoadFieldCatalog(db *gorm.DB) (FieldCatalog, error) {
	var defs []FieldDef
	if err := db.Where("is_active = ?", true).Find(&defs).Error; err != nil {
		return nil, err
	}
	return NewFieldCatalog(defs), nil
}

// CategoryFieldRef is one field a category puts on a form: a catalogue key plus
// per-category settings. Options, when set, replace the catalogue's default
// options for this category. Filterable applies to listing fields only.
type CategoryFieldRef struct {
	Key        string   `json:"key"`
	Required   bool     `json:"required"`
	Filterable bool     `json:"filterable,omitempty"`
	Options    []string `json:"options,omitempty"`
}

// ValidateCategoryFieldRefs checks a category's listing_fields or
// booking_fields (usage FieldUsageListing / FieldUsageBooking): every key is an
// active catalogue field whose scope allows the usage, no key repeats, options
// only on select/multiselect fields, every select/multiselect ends up with
// options, and filterable only on listing fields.
func ValidateCategoryFieldRefs(refs []CategoryFieldRef, usage string, catalog FieldCatalog) error {
	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		if r.Key == "" {
			return fmt.Errorf("%s field key is required", usage)
		}
		if seen[r.Key] {
			return fmt.Errorf("duplicate %s field %q", usage, r.Key)
		}
		seen[r.Key] = true
		def, ok := catalog[r.Key]
		if !ok {
			return fmt.Errorf("%s field %q is not an active catalogue field", usage, r.Key)
		}
		if !def.Scope.Allows(usage) {
			return fmt.Errorf("field %q cannot be used as a %s field (scope %s)", r.Key, usage, def.Scope)
		}
		if r.Filterable && usage != FieldUsageListing {
			return fmt.Errorf("field %q: only listing fields can be filterable", r.Key)
		}
		if len(r.Options) > 0 && !fieldTypeHasOptions(def.Type) {
			return fmt.Errorf("field %q is a %s field; options apply to select and multiselect only", r.Key, def.Type)
		}
		if err := validateOptionList(r.Options); err != nil {
			return fmt.Errorf("field %q: %w", r.Key, err)
		}
		if fieldTypeHasOptions(def.Type) && len(r.Options) == 0 && len(def.Options) == 0 {
			return fmt.Errorf("field %q needs options: the catalogue has none for it", r.Key)
		}
	}
	return nil
}

// FormField is a category field expanded to everything a client needs to render
// it: the catalogue's type/label/group merged with the category's settings.
type FormField struct {
	BookingField
	Filterable bool `json:"filterable,omitempty"`
}

// ExpandFieldRefs merges refs with their catalogue entries. A ref whose field
// has since been deactivated (absent from catalog) is dropped, so retiring a
// field takes it off every form at once.
func ExpandFieldRefs(refs []CategoryFieldRef, catalog FieldCatalog) []FormField {
	out := make([]FormField, 0, len(refs))
	for _, r := range refs {
		def, ok := catalog[r.Key]
		if !ok {
			continue
		}
		options := r.Options
		if len(options) == 0 {
			options = def.Options
		}
		out = append(out, FormField{
			BookingField: BookingField{
				Key: def.Key, Type: def.Type, Label: def.Label, Group: def.Group,
				Required: r.Required, Options: options,
			},
			Filterable: r.Filterable,
		})
	}
	return out
}

// BookingFieldsOf strips the listing-only parts, giving the booking-form shape.
func BookingFieldsOf(fields []FormField) []BookingField {
	out := make([]BookingField, len(fields))
	for i, f := range fields {
		out[i] = f.BookingField
	}
	return out
}
