package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Category configuration (spec 2026-10-28 §2): booking mode, allowed price
// units and the listing/booking field sets. Every column is nullable on the
// category; NULL inherits from the parent, resolved by EffectiveCategoryConfig.

// Booking modes for a booking category.
const (
	BookingModeSlot      = "slot"       // reserve a date/time slot (today's flow)
	BookingModeDateRange = "date_range" // nights/months between two dates
	BookingModeQuantity  = "quantity"   // N units (seats, sessions)
)

// ValidBookingMode reports whether s is a known booking mode.
func ValidBookingMode(s string) bool {
	return s == BookingModeSlot || s == BookingModeDateRange || s == BookingModeQuantity
}

// Price units a category may allow a listing to be priced in.
const (
	PriceUnitFixed      = "fixed"
	PriceUnitPerHour    = "per_hour"
	PriceUnitPerNight   = "per_night"
	PriceUnitPerMonth   = "per_month"
	PriceUnitPerSession = "per_session"
	PriceUnitPerSeat    = "per_seat"
	PriceUnitOnRequest  = "on_request"
)

// PriceUnits lists every price unit in display order.
var PriceUnits = []string{
	PriceUnitFixed, PriceUnitPerHour, PriceUnitPerNight, PriceUnitPerMonth,
	PriceUnitPerSession, PriceUnitPerSeat, PriceUnitOnRequest,
}

// ValidPriceUnit reports whether s is a known price unit.
func ValidPriceUnit(s string) bool { return containsString(PriceUnits, s) }

// ChargesPerUnit reports whether a listing priced in unit is charged once per
// booked unit (seat, ticket, session) in a category of booking mode. Only
// quantity mode multiplies; there a listing that never chose a unit is per-unit
// too, so a ticket listing set up before price units existed does not sell
// three tickets for the price of one. Fixed and on-request prices are a total.
func ChargesPerUnit(mode, unit string) bool {
	if mode != BookingModeQuantity {
		return false
	}
	return unit != PriceUnitFixed && unit != PriceUnitOnRequest
}

// ValidatePriceUnits checks a category's allowed price units: known, no repeats.
// An empty list is valid and means "no units allowed" (an explicit override).
func ValidatePriceUnits(units []string) error {
	seen := make(map[string]bool, len(units))
	for _, u := range units {
		if !containsString(PriceUnits, u) {
			return fmt.Errorf("unknown price unit %q", u)
		}
		if seen[u] {
			return fmt.Errorf("duplicate price unit %q", u)
		}
		seen[u] = true
	}
	return nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ValidSlug reports whether s is a well-formed category slug.
func ValidSlug(s string) bool { return len(s) <= 80 && slugPattern.MatchString(s) }

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a slug from a title ("Student Hostel & Co" → "student-hostel-co").
// A title with no usable characters gives "category".
func Slugify(title string) string {
	s := strings.Trim(slugStrip.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 72 { // leave room for a "-NN" dedupe suffix
		s = strings.TrimRight(s[:72], "-")
	}
	if s == "" {
		return "category"
	}
	return s
}

// UniqueSlug returns base, or base-2, base-3 … — the first one taken(s) says is
// free.
func UniqueSlug(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		if s := fmt.Sprintf("%s-%d", base, i); !taken(s) {
			return s
		}
	}
}

// isUnsetJSON reports whether a nullable JSON column is NULL (Scan gives
// "null" for SQL NULL).
func isUnsetJSON(j datatypes.JSON) bool {
	s := strings.TrimSpace(string(j))
	return s == "" || s == "null"
}

// GetPriceUnits decodes PriceUnits; ok is false when unset (inherit).
func (c *Category) GetPriceUnits() (units []string, ok bool) {
	if isUnsetJSON(c.PriceUnits) || json.Unmarshal(c.PriceUnits, &units) != nil {
		return nil, false
	}
	if units == nil {
		units = []string{}
	}
	return units, true
}

// GetListingFields decodes ListingFields; ok is false when unset (inherit).
func (c *Category) GetListingFields() ([]CategoryFieldRef, bool) {
	return decodeFieldRefs(c.ListingFields)
}

// GetBookingFields decodes BookingFields; ok is false when unset (inherit).
func (c *Category) GetBookingFields() ([]CategoryFieldRef, bool) {
	return decodeFieldRefs(c.BookingFields)
}

func decodeFieldRefs(j datatypes.JSON) ([]CategoryFieldRef, bool) {
	var refs []CategoryFieldRef
	if isUnsetJSON(j) || json.Unmarshal(j, &refs) != nil {
		return nil, false
	}
	if refs == nil {
		refs = []CategoryFieldRef{}
	}
	return refs, true
}

// ConfigSource names the category a resolved value came from.
type ConfigSource struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
	// Inherited is true when the value came from an ancestor, not the category itself.
	Inherited bool `json:"inherited"`
}

// ResolvedValue is one resolved config value and where it came from. Source is
// nil when no category in the chain sets it.
type ResolvedValue[T any] struct {
	Value  T             `json:"value"`
	Source *ConfigSource `json:"source"`
}

// CategoryConfig is a category's effective configuration.
type CategoryConfig struct {
	BookingMode   ResolvedValue[*string]            `json:"bookingMode"`
	PriceUnits    ResolvedValue[[]string]           `json:"priceUnits"`
	ListingFields ResolvedValue[[]CategoryFieldRef] `json:"listingFields"`
	BookingFields ResolvedValue[[]CategoryFieldRef] `json:"bookingFields"`
}

// EffectiveCategoryConfig resolves cat's configuration: each nullable column
// takes the nearest non-NULL value walking cat → parent → grandparent …
// ancestors must be ordered nearest first (as CategoryAncestors returns them).
// A category repeated in the chain (a cycle) ends the walk, so a corrupt tree
// can never loop. This is the single source of truth for category config, as
// EffectiveServiceType is for the service type; an unset value stays zero
// (BookingMode nil, lists nil) and callers apply their own default.
func EffectiveCategoryConfig(cat Category, ancestors []Category) CategoryConfig {
	var cfg CategoryConfig
	seen := map[uuid.UUID]bool{}
	chain := append([]Category{cat}, ancestors...)
	for i := range chain {
		c := &chain[i]
		if seen[c.ID] {
			break
		}
		seen[c.ID] = true
		src := &ConfigSource{ID: c.ID, Title: c.Title, Inherited: i > 0}
		if cfg.BookingMode.Source == nil && c.BookingMode != nil {
			mode := *c.BookingMode
			cfg.BookingMode = ResolvedValue[*string]{Value: &mode, Source: src}
		}
		if cfg.PriceUnits.Source == nil {
			if v, ok := c.GetPriceUnits(); ok {
				cfg.PriceUnits = ResolvedValue[[]string]{Value: v, Source: src}
			}
		}
		if cfg.ListingFields.Source == nil {
			if v, ok := c.GetListingFields(); ok {
				cfg.ListingFields = ResolvedValue[[]CategoryFieldRef]{Value: v, Source: src}
			}
		}
		if cfg.BookingFields.Source == nil {
			if v, ok := c.GetBookingFields(); ok {
				cfg.BookingFields = ResolvedValue[[]CategoryFieldRef]{Value: v, Source: src}
			}
		}
	}
	return cfg
}

// MaxCategoryDepth bounds every walk up the tree.
const MaxCategoryDepth = 32

// CategoryAncestors loads cat's ancestors, nearest first. It stops at a root,
// a missing parent, a repeated category (cycle) or MaxCategoryDepth.
func CategoryAncestors(db *gorm.DB, cat Category) ([]Category, error) {
	var out []Category
	seen := map[uuid.UUID]bool{cat.ID: true}
	next := cat.ParentCategoryID
	for next != nil && len(out) < MaxCategoryDepth {
		if seen[*next] {
			break
		}
		seen[*next] = true
		var parent Category
		if err := db.First(&parent, "id = ?", *next).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				break
			}
			return nil, err
		}
		out = append(out, parent)
		next = parent.ParentCategoryID
	}
	return out, nil
}

// ResolveCategoryConfig loads cat's ancestors and resolves its configuration.
func ResolveCategoryConfig(db *gorm.DB, cat Category) (CategoryConfig, error) {
	ancestors, err := CategoryAncestors(db, cat)
	if err != nil {
		return CategoryConfig{}, err
	}
	return EffectiveCategoryConfig(cat, ancestors), nil
}

// ParentWouldCycle reports whether making parentID the parent of categoryID
// would put categoryID among its own ancestors.
func ParentWouldCycle(db *gorm.DB, categoryID, parentID uuid.UUID) (bool, error) {
	if categoryID == parentID {
		return true, nil
	}
	var parent Category
	if err := db.First(&parent, "id = ?", parentID).Error; err != nil {
		return false, err
	}
	ancestors, err := CategoryAncestors(db, parent)
	if err != nil {
		return false, err
	}
	for _, a := range ancestors {
		if a.ID == categoryID {
			return true, nil
		}
	}
	// A chain that hit the depth bound cannot be proven acyclic; refuse it.
	return len(ancestors) >= MaxCategoryDepth, nil
}
