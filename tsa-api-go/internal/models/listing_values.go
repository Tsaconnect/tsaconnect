package models

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Listing values: a merchant's answers to the listing fields their category
// asks for (Category.listing_fields, resolved up the tree). They are validated
// with the booking-form validator, so the two forms share one field vocabulary,
// and stored on Product.ListingValues where search filters match on them.

// CategoryListingForm is the listing form a category's listings fill in.
func CategoryListingForm(db *gorm.DB, cat Category, catalog FieldCatalog) ([]FormField, error) {
	cfg, err := ResolveCategoryConfig(db, cat)
	if err != nil {
		return nil, err
	}
	return ExpandFieldRefs(cfg.ListingFields.Value, catalog), nil
}

// ValidateListingValues checks values against the listing form and returns
// only the answers the form asks for, empty ones dropped. Keys the form does
// not ask for are dropped rather than refused: a category's fields can change
// after a listing was made, and an edit must not fail on a field that is gone.
func ValidateListingValues(fields []FormField, values map[string]interface{}) (map[string]interface{}, error) {
	if err := ValidateAnswers(&BookingFieldConfig{Fields: BookingFieldsOf(fields)}, values); err != nil {
		return nil, err
	}
	out := make(map[string]interface{}, len(fields))
	for _, f := range fields {
		if v, ok := values[f.Key]; ok && !isEmptyAnswer(v) {
			out[f.Key] = v
		}
	}
	return out, nil
}

// EncodeListingValues stores values in the JSONB column; none clears it.
func EncodeListingValues(values map[string]interface{}) datatypes.JSON {
	if len(values) == 0 {
		return nil
	}
	b, _ := json.Marshal(values)
	return datatypes.JSON(b)
}

// ListingValueFilter turns attr[key]=value query parameters into one JSON
// object for a `listing_values @> ?` containment match. Only fields the
// category marks filterable are honoured; anything else is ignored, so a stale
// or made-up filter narrows nothing rather than failing the search. The value
// travels as a bound parameter, never as SQL.
func ListingValueFilter(fields []FormField, params map[string]string) (string, error) {
	match := make(map[string]interface{})
	for _, f := range fields {
		raw, ok := params[f.Key]
		raw = strings.TrimSpace(raw)
		if !ok || raw == "" || !f.Filterable {
			continue
		}
		switch f.Type {
		case BookingFieldSelect, BookingFieldText:
			match[f.Key] = raw
		case BookingFieldMultiselect:
			match[f.Key] = []string{raw} // the listing offers at least this option
		case BookingFieldBoolean:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return "", fmt.Errorf("%s must be true or false", f.Label)
			}
			match[f.Key] = b
		case BookingFieldNumber, BookingFieldCounter:
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return "", fmt.Errorf("%s must be a number", f.Label)
			}
			match[f.Key] = n
		}
	}
	if len(match) == 0 {
		return "", nil
	}
	b, err := json.Marshal(match)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
