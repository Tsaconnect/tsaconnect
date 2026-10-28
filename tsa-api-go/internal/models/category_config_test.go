package models

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func strPtr(s string) *string { return &s }

func TestEffectiveCategoryConfig_InheritsNearestNonNullOverThreeLevels(t *testing.T) {
	root := Category{ID: uuid.New(), Title: "Services",
		BookingMode:   strPtr(BookingModeSlot),
		PriceUnits:    datatypes.JSON(`["fixed","per_hour"]`),
		BookingFields: datatypes.JSON(`[{"key":"appointment","required":true}]`),
		ListingFields: datatypes.JSON(`[{"key":"brand","required":false}]`)}
	mid := Category{ID: uuid.New(), Title: "Accommodation", ParentCategoryID: &root.ID,
		PriceUnits:    datatypes.JSON(`["per_night"]`),
		ListingFields: datatypes.JSON("null")} // SQL NULL scans as "null": inherit
	leaf := Category{ID: uuid.New(), Title: "Student hostel", ParentCategoryID: &mid.ID,
		ListingFields: datatypes.JSON(`[{"key":"institution","required":true,"filterable":true}]`),
		BookingFields: datatypes.JSON(`[]`)} // explicit empty overrides the root's

	cfg := EffectiveCategoryConfig(leaf, []Category{mid, root})

	if cfg.BookingMode.Value == nil || *cfg.BookingMode.Value != BookingModeSlot ||
		cfg.BookingMode.Source.ID != root.ID || !cfg.BookingMode.Source.Inherited {
		t.Errorf("bookingMode should come from root: %+v", cfg.BookingMode)
	}
	if len(cfg.PriceUnits.Value) != 1 || cfg.PriceUnits.Value[0] != PriceUnitPerNight || cfg.PriceUnits.Source.ID != mid.ID {
		t.Errorf("priceUnits should be the middle override: %+v", cfg.PriceUnits)
	}
	if len(cfg.ListingFields.Value) != 1 || cfg.ListingFields.Value[0].Key != "institution" ||
		cfg.ListingFields.Source.ID != leaf.ID || cfg.ListingFields.Source.Inherited {
		t.Errorf("listingFields should be the leaf's own: %+v", cfg.ListingFields)
	}
	if cfg.BookingFields.Value == nil || len(cfg.BookingFields.Value) != 0 || cfg.BookingFields.Source.ID != leaf.ID {
		t.Errorf("an explicit [] must override, not inherit: %+v", cfg.BookingFields)
	}
}

func TestEffectiveCategoryConfig_UnsetEverywhereHasNoSource(t *testing.T) {
	cfg := EffectiveCategoryConfig(Category{ID: uuid.New()}, nil)
	if cfg.BookingMode.Source != nil || cfg.PriceUnits.Source != nil || cfg.ListingFields.Source != nil || cfg.BookingFields.Source != nil {
		t.Fatalf("nothing set, yet a source was reported: %+v", cfg)
	}
}

func TestEffectiveCategoryConfig_CycleEndsTheWalk(t *testing.T) {
	a := Category{ID: uuid.New(), Title: "A"}
	b := Category{ID: uuid.New(), Title: "B", ParentCategoryID: &a.ID}
	a.ParentCategoryID = &b.ID
	// A corrupt chain that repeats a; the repeated a carries a value that must
	// not be read a second time as if it were an ancestor.
	aAgain := a
	aAgain.BookingMode = strPtr(BookingModeQuantity)
	cfg := EffectiveCategoryConfig(a, []Category{b, aAgain, b})
	if cfg.BookingMode.Source != nil {
		t.Fatalf("cycle was walked: %+v", cfg.BookingMode)
	}
}

func openCategoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec(`CREATE TABLE categories (id TEXT PRIMARY KEY, title TEXT, description TEXT, type TEXT,
		parent_category_id TEXT, icon TEXT, image TEXT, color TEXT, is_active INTEGER, sort_order INTEGER,
		product_count INTEGER, service_fee_mode TEXT, service_type TEXT, slug TEXT, booking_mode TEXT,
		price_units TEXT, listing_fields TEXT, booking_fields TEXT, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCategoryAncestorsAndParentWouldCycle(t *testing.T) {
	db := openCategoryDB(t)
	root := Category{ID: uuid.New(), Title: "Root", CreatedAt: time.Now()}
	mid := Category{ID: uuid.New(), Title: "Mid", ParentCategoryID: &root.ID, CreatedAt: time.Now()}
	leaf := Category{ID: uuid.New(), Title: "Leaf", ParentCategoryID: &mid.ID, CreatedAt: time.Now()}
	for _, c := range []*Category{&root, &mid, &leaf} {
		if err := db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
	}
	anc, err := CategoryAncestors(db, leaf)
	if err != nil || len(anc) != 2 || anc[0].ID != mid.ID || anc[1].ID != root.ID {
		t.Fatalf("ancestors nearest first, got %v %v", anc, err)
	}
	if cyc, _ := ParentWouldCycle(db, root.ID, leaf.ID); !cyc {
		t.Error("root under its own grandchild must be a cycle")
	}
	if cyc, _ := ParentWouldCycle(db, leaf.ID, leaf.ID); !cyc {
		t.Error("self-parent must be a cycle")
	}
	if cyc, _ := ParentWouldCycle(db, leaf.ID, root.ID); cyc {
		t.Error("moving the leaf under root is not a cycle")
	}

	// A tree already corrupt (root → leaf) must still terminate.
	db.Model(&Category{}).Where("id = ?", root.ID).Update("parent_category_id", leaf.ID)
	if anc, err := CategoryAncestors(db, leaf); err != nil || len(anc) != 2 {
		t.Fatalf("cyclic tree walk: %v %v", anc, err)
	}
}

func testCatalog() FieldCatalog {
	c := BuiltinFieldCatalog()
	c["wifi_speed"] = FieldDef{Key: "wifi_speed", Type: BookingFieldNumber, Label: "Wi-Fi", Scope: FieldScopeListing, IsActive: true}
	return c
}

func TestValidateCategoryFieldRefs(t *testing.T) {
	cases := []struct {
		name    string
		usage   string
		refs    []CategoryFieldRef
		wantErr string
	}{
		{"listing ok", FieldUsageListing, []CategoryFieldRef{
			{Key: "institution", Required: true, Filterable: true, Options: []string{"UNILAG", "UI"}},
			{Key: "gender", Filterable: true}, // catalogue options
			{Key: "room_type", Options: []string{"single", "2-person"}},
			{Key: "wifi_speed"}, // admin-added, non-system
		}, ""},
		{"booking ok", FieldUsageBooking, []CategoryFieldRef{{Key: "checkin", Required: true}, {Key: "room_type", Options: []string{"single"}}}, ""},
		{"unknown key", FieldUsageListing, []CategoryFieldRef{{Key: "made_up"}}, "not an active catalogue field"},
		{"wrong scope: booking field on listing", FieldUsageListing, []CategoryFieldRef{{Key: "checkin"}}, "cannot be used as a listing field"},
		{"wrong scope: listing field on booking", FieldUsageBooking, []CategoryFieldRef{{Key: "brand"}}, "cannot be used as a booking field"},
		{"options on a number", FieldUsageListing, []CategoryFieldRef{{Key: "years_experience", Options: []string{"1"}}}, "options apply"},
		{"select with no options anywhere", FieldUsageListing, []CategoryFieldRef{{Key: "institution"}}, "needs options"},
		{"filterable on booking", FieldUsageBooking, []CategoryFieldRef{{Key: "guests", Filterable: true}}, "only listing fields"},
		{"duplicate", FieldUsageBooking, []CategoryFieldRef{{Key: "guests"}, {Key: "guests"}}, "duplicate"},
		{"blank option", FieldUsageListing, []CategoryFieldRef{{Key: "gender", Options: []string{" "}}}, "blank"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCategoryFieldRefs(tc.refs, tc.usage, testCatalog())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateCategoryFieldRefs_InactiveFieldRejected(t *testing.T) {
	// LoadFieldCatalog only returns active rows, so an inactive field is absent.
	catalog := testCatalog()
	delete(catalog, "wifi_speed")
	if err := ValidateCategoryFieldRefs([]CategoryFieldRef{{Key: "wifi_speed"}}, FieldUsageListing, catalog); err == nil {
		t.Fatal("an inactive field was accepted")
	}
}

func TestExpandFieldRefs_MergesCatalogueAndDropsInactive(t *testing.T) {
	catalog := testCatalog()
	got := ExpandFieldRefs([]CategoryFieldRef{
		{Key: "gender", Required: true, Filterable: true},
		{Key: "room_type", Options: []string{"single"}},
		{Key: "retired"},
	}, catalog)
	if len(got) != 2 {
		t.Fatalf("want 2 fields (retired dropped), got %+v", got)
	}
	if got[0].Type != BookingFieldSelect || got[0].Label != "Gender" || !got[0].Required || !got[0].Filterable ||
		strings.Join(got[0].Options, ",") != "male,female,mixed" {
		t.Errorf("gender not merged from the catalogue: %+v", got[0])
	}
	if strings.Join(got[1].Options, ",") != "single" {
		t.Errorf("category options must replace catalogue options: %+v", got[1])
	}
}

func TestValidatePriceUnitsAndBookingMode(t *testing.T) {
	if err := ValidatePriceUnits([]string{PriceUnitPerSession, PriceUnitFixed}); err != nil {
		t.Fatal(err)
	}
	if ValidatePriceUnits([]string{"per_week"}) == nil || ValidatePriceUnits([]string{"fixed", "fixed"}) == nil {
		t.Fatal("unknown or duplicate unit accepted")
	}
	if !ValidBookingMode(BookingModeDateRange) || ValidBookingMode("hourly") {
		t.Fatal("booking mode validation wrong")
	}
}

func TestSlugifyAndUniqueSlug(t *testing.T) {
	if got := Slugify("Student Hostel & Co."); got != "student-hostel-co" {
		t.Errorf("Slugify = %q", got)
	}
	if got := Slugify("!!!"); got != "category" {
		t.Errorf("Slugify of punctuation = %q", got)
	}
	taken := map[string]bool{"rides": true, "rides-2": true}
	if got := UniqueSlug("rides", func(s string) bool { return taken[s] }); got != "rides-3" {
		t.Errorf("UniqueSlug = %q", got)
	}
	if !ValidSlug("student-hostel") || ValidSlug("Student") || ValidSlug("a--b") {
		t.Error("ValidSlug wrong")
	}
}
