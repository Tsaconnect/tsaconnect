package config

import (
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// legacyCategoriesDDL is categories as it stood before service types: a
// fee mode, and no service_type.
const legacyCategoriesDDL = `CREATE TABLE categories (id TEXT PRIMARY KEY, title TEXT, service_fee_mode TEXT DEFAULT 'standard')`

func categoryServiceType(t *testing.T, db *gorm.DB, id string) string {
	t.Helper()
	var st string
	if err := db.Raw(`SELECT service_type FROM categories WHERE id = ?`, id).Scan(&st).Error; err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMigrateCategoryServiceType_BackfillsBookingOnceAndKeepsAdminChoice(t *testing.T) {
	db := openPayMigrateDB(t)
	if err := db.Exec(legacyCategoriesDDL).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO categories (id, title, service_fee_mode) VALUES ('fare', 'Rides', 'booking'), ('plain', 'Plumbing', 'standard')`)

	if err := migrateCategoryServiceType(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if got := categoryServiceType(t, db, "fare"); got != "booking" {
		t.Fatalf("booking-fare category got service_type %q, want booking", got)
	}
	if got := categoryServiceType(t, db, "plain"); got != "contact" {
		t.Fatalf("standard category got service_type %q, want contact", got)
	}

	// An admin moves the legacy fare category to contact; a later boot must keep it.
	db.Exec(`UPDATE categories SET service_type = 'contact' WHERE id = 'fare'`)
	if err := migrateCategoryServiceType(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := categoryServiceType(t, db, "fare"); got != "contact" {
		t.Fatalf("a later migrate reverted the admin's contact choice to %q", got)
	}
}

func TestMigrateCategoryServiceType_AFreshDatabaseHasNothingToDo(t *testing.T) {
	db := openPayMigrateDB(t)
	if err := migrateCategoryServiceType(db); err != nil {
		t.Fatalf("no table yet: %v", err)
	}
	if db.Migrator().HasTable("categories") {
		t.Fatal("the service-type step created categories; AutoMigrate owns that")
	}
}

// The step must run before AutoMigrate, or AutoMigrate adds the column first
// and the booking backfill never runs.
func TestAutoMigrate_RunsTheCategoryServiceTypeStepFirst(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)[strings.Index(string(src), "func AutoMigrate() error {"):]
	step, migrate := strings.Index(body, "migrateCategoryServiceType(DB)"), strings.Index(body, "DB.AutoMigrate(")
	if step < 0 || migrate < 0 || step > migrate {
		t.Fatalf("migrateCategoryServiceType(DB) must be called before DB.AutoMigrate (step at %d, AutoMigrate at %d)", step, migrate)
	}
	if strings.Contains(body, "service_type = 'contact')") {
		t.Fatal("the every-boot service_type backfill is back; it reverts admins' contact choices")
	}
}

func TestBackfillCategorySlugs_FillsOnlyMissingAndDedupes(t *testing.T) {
	db := openPayMigrateDB(t)
	if err := db.Exec(`CREATE TABLE categories (id TEXT PRIMARY KEY, title TEXT, slug TEXT, created_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO categories (id, title, slug, created_at) VALUES
		('00000000-0000-0000-0000-00000000000a', 'Rides', NULL, '2026-01-01'),
		('00000000-0000-0000-0000-00000000000b', 'rides!', NULL, '2026-01-02'),
		('00000000-0000-0000-0000-00000000000c', 'Hostels', 'rides-2', '2026-01-03'),
		('00000000-0000-0000-0000-00000000000d', 'Salon', 'beauty', '2026-01-04')`)

	if err := backfillCategorySlugs(db); err != nil {
		t.Fatal(err)
	}
	slug := func(id string) string {
		var s string
		db.Raw(`SELECT slug FROM categories WHERE id = ?`, id).Scan(&s)
		return s
	}
	if slug("00000000-0000-0000-0000-00000000000a") != "rides" || slug("00000000-0000-0000-0000-00000000000b") != "rides-3" {
		t.Fatalf("dedupe wrong: a=%q b=%q", slug("00000000-0000-0000-0000-00000000000a"), slug("00000000-0000-0000-0000-00000000000b"))
	}
	if slug("00000000-0000-0000-0000-00000000000c") != "rides-2" || slug("00000000-0000-0000-0000-00000000000d") != "beauty" {
		t.Fatal("an existing slug was rewritten")
	}
	db.Exec(`UPDATE categories SET slug = 'cars' WHERE id = '00000000-0000-0000-0000-00000000000a'`)
	if err := backfillCategorySlugs(db); err != nil {
		t.Fatal(err)
	}
	if slug("00000000-0000-0000-0000-00000000000a") != "cars" {
		t.Fatal("a later boot rewrote an admin-set slug")
	}
}
