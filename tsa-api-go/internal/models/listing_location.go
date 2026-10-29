package models

import (
	"strings"

	"gorm.io/gorm"
)

// ListingLocation is where a listing is: the country/state/city the app's
// location picker fills (for Nigeria its "cities" are LGAs) and a free-text
// area ("Kubwa"). Product.Location stays the display string; these columns are
// what search filters on.
type ListingLocation struct {
	Country string
	State   string
	City    string
	Area    string
}

// FormatListingLocation is the display string the app has always stored:
// "city, state, country", skipping empty parts.
func FormatListingLocation(l ListingLocation) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{l.City, l.State, l.Country} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// ParseListingLocation reads a stored "city, state, country" string back into
// its parts, from the end: the last part is the country, the one before it the
// state, and everything before that the city — a typed city may itself contain
// commas ("Dutse, Kubwa extension Abuja"). A string with no comma ("Enugu
// Nigeria") cannot be split safely and returns ok=false, leaving it as text.
func ParseListingLocation(s string) (ListingLocation, bool) {
	var parts []string
	for _, p := range strings.Split(s, ",") {
		if p = collapseSpaces(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return ListingLocation{}, false
	}
	n := len(parts)
	loc := ListingLocation{Country: parts[n-1], State: parts[n-2]}
	if n > 2 {
		loc.City = strings.Join(parts[:n-2], ", ")
	}
	return loc, true
}

// LocationKey is a place name as search compares it: lower-case, single
// spaces, no trailing full stop, and no " state" suffix, so "Enugu", "enugu
// state" and "Enugu State." are the same state.
func LocationKey(s string) string {
	k := strings.ToLower(collapseSpaces(s))
	k = strings.TrimRight(k, ". ")
	k = strings.TrimSuffix(k, " state")
	return k
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// SetListingLocation writes l onto p: the display string (unless display is
// given), the parts, and their search keys.
func (p *Product) SetListingLocation(l ListingLocation, display string) {
	p.Country = collapseSpaces(l.Country)
	p.State = collapseSpaces(l.State)
	p.City = collapseSpaces(l.City)
	p.Area = collapseSpaces(l.Area)
	p.CountryKey = LocationKey(p.Country)
	p.StateKey = LocationKey(p.State)
	p.CityKey = LocationKey(p.City)
	if display = strings.TrimSpace(display); display == "" {
		display = FormatListingLocation(l)
	}
	p.Location = display
}

// ListingLocationColumns is SetListingLocation as an Updates map, for the
// update handler and the boot backfill.
func ListingLocationColumns(l ListingLocation, display string) map[string]interface{} {
	var p Product
	p.SetListingLocation(l, display)
	return map[string]interface{}{
		"location": p.Location, "country": p.Country, "state": p.State, "city": p.City,
		"area": p.Area, "country_key": p.CountryKey, "state_key": p.StateKey, "city_key": p.CityKey,
	}
}

// BackfillListingLocations fills the structured location of listings that only
// have the display string, parsed as ParseListingLocation reads it. It writes
// only rows with no location key yet and never changes the display string, so
// it is safe on every boot; a string it cannot split stays text only.
func BackfillListingLocations(db *gorm.DB) (int, error) {
	var rows []Product
	if err := db.Unscoped().Select("id", "location").
		Where("COALESCE(country_key, '') = '' AND COALESCE(state_key, '') = '' AND location LIKE ?", "%,%").
		Find(&rows).Error; err != nil {
		return 0, err
	}
	filled := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, r := range rows {
			loc, ok := ParseListingLocation(r.Location)
			if !ok {
				continue
			}
			cols := ListingLocationColumns(loc, r.Location)
			delete(cols, "location")
			res := tx.Unscoped().Model(&Product{}).
				Where("id = ? AND COALESCE(country_key, '') = '' AND COALESCE(state_key, '') = ''", r.ID).
				UpdateColumns(cols)
			if res.Error != nil {
				return res.Error
			}
			filled += int(res.RowsAffected)
		}
		return nil
	})
	return filled, err
}
