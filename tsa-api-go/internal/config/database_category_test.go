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
