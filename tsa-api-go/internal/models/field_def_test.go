package models

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fieldDefsDDL is field_defs in SQLite terms (the model's gen_random_uuid()
// default is Postgres-only).
const fieldDefsDDL = `CREATE TABLE field_defs (id TEXT PRIMARY KEY, key TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
	label TEXT NOT NULL, field_group TEXT, options TEXT, scope TEXT NOT NULL, is_system INTEGER NOT NULL DEFAULT 0,
	is_active INTEGER NOT NULL DEFAULT 1, created_by TEXT, created_at DATETIME, updated_at DATETIME)`

func openFieldDefDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec(fieldDefsDDL).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSeedFieldDefs_IdempotentAndKeepsAdminEdits(t *testing.T) {
	db := openFieldDefDB(t)
	if err := SeedFieldDefs(db); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	var n int64
	db.Model(&FieldDef{}).Count(&n)
	if want := int64(len(BuiltinFieldDefs())); n != want {
		t.Fatalf("seeded %d rows, want %d", n, want)
	}

	// An admin relabels and deactivates a system field, and changes options.
	db.Model(&FieldDef{}).Where("key = ?", "guests").Updates(map[string]interface{}{"label": "Visitors", "is_active": false})
	db.Model(&FieldDef{}).Where("key = ?", "gender").Update("options", `["women only"]`)

	if err := SeedFieldDefs(db); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	db.Model(&FieldDef{}).Count(&n)
	if want := int64(len(BuiltinFieldDefs())); n != want {
		t.Fatalf("re-seed changed the row count to %d, want %d", n, want)
	}
	var guests, gender FieldDef
	db.First(&guests, "key = ?", "guests")
	db.First(&gender, "key = ?", "gender")
	if guests.Label != "Visitors" || guests.IsActive {
		t.Errorf("re-seed overwrote the admin's edit: %+v", guests)
	}
	if len(gender.Options) != 1 || gender.Options[0] != "women only" {
		t.Errorf("re-seed overwrote the admin's options: %+v", gender.Options)
	}

	catalog, err := LoadFieldCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog["guests"]; ok {
		t.Error("an inactive field is in the active catalogue")
	}
	if d := catalog["room_type"]; d.Scope != FieldScopeBoth || !d.IsSystem {
		t.Errorf("room_type should be a system field of scope both: %+v", d)
	}
	if d := catalog["amenities"]; d.Type != BookingFieldMultiselect || d.Scope != FieldScopeListing {
		t.Errorf("amenities should be a listing multiselect: %+v", d)
	}
}

func TestValidateFieldDef(t *testing.T) {
	ok := FieldDef{Key: "parking", Type: BookingFieldBoolean, Label: "Parking", Scope: FieldScopeListing}
	if err := ValidateFieldDef(&ok); err != nil {
		t.Fatal(err)
	}
	bad := []FieldDef{
		{Key: "Parking", Type: BookingFieldBoolean, Label: "x", Scope: FieldScopeListing},
		{Key: "parking", Type: "slider", Label: "x", Scope: FieldScopeListing},
		{Key: "parking", Type: BookingFieldBoolean, Label: " ", Scope: FieldScopeListing},
		{Key: "parking", Type: BookingFieldBoolean, Label: "x", Scope: "everywhere"},
		{Key: "parking", Type: BookingFieldBoolean, Label: "x", Scope: FieldScopeListing, Options: []string{"yes"}},
	}
	for i := range bad {
		if ValidateFieldDef(&bad[i]) == nil {
			t.Errorf("accepted %+v", bad[i])
		}
	}
}
