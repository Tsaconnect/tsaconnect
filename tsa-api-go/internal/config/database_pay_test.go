package config

import (
	"os"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAutoMigrate_IncludesTSAPayModels(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"&models.PayMerchant{}", "&models.PayPayment{}", "&models.PayWebhookDelivery{}", "&models.PayAuditEvent{}", "&models.PayOwnerAction{}", "&models.PayOpenLink{}", "&models.PayRetiredSlug{}", "&models.WebSigninChallenge{}", "&models.WebTrustedBrowser{}"} {
		if !strings.Contains(string(src), m) {
			t.Fatalf("AutoMigrate is missing %s", m)
		}
	}
}

// legacyPayMerchantsDDL is pay_merchants as it stood before the per-merchant
// live switch: enough columns for the backfill to read, and no live_enabled.
const legacyPayMerchantsDDL = `CREATE TABLE pay_merchants (id TEXT PRIMARY KEY, business_name TEXT, live_key_hash TEXT)`

func openPayMigrateDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1) // every :memory: connection is its own database
	return db
}

func payLiveEnabled(t *testing.T, db *gorm.DB, id string) bool {
	t.Helper()
	var live bool
	if err := db.Raw(`SELECT live_enabled FROM pay_merchants WHERE id = ?`, id).Scan(&live).Error; err != nil {
		t.Fatal(err)
	}
	return live
}

func TestMigratePayMerchantLiveSwitch_KeepsLiveKeyHoldersLiveOnce(t *testing.T) {
	db := openPayMigrateDB(t)
	if err := db.Exec(legacyPayMerchantsDDL).Error; err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO pay_merchants (id, business_name, live_key_hash) VALUES ('live', 'Sky Air', 'h1'), ('testonly', 'Kite', NULL)`)

	if err := migratePayMerchantLiveSwitch(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if !payLiveEnabled(t, db, "live") {
		t.Fatal("a merchant holding a live key was switched off by the first migrate")
	}
	if payLiveEnabled(t, db, "testonly") {
		t.Fatal("a merchant with no live key was switched on")
	}

	// A super admin switches the merchant off; a later migrate must leave that alone.
	db.Exec(`UPDATE pay_merchants SET live_enabled = false WHERE id = 'live'`)
	if err := migratePayMerchantLiveSwitch(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if payLiveEnabled(t, db, "live") {
		t.Fatal("a second migrate re-enabled a merchant an admin had switched off")
	}
}

func TestMigratePayMerchantLiveSwitch_AFreshDatabaseHasNothingToDo(t *testing.T) {
	db := openPayMigrateDB(t)
	if err := migratePayMerchantLiveSwitch(db); err != nil {
		t.Fatalf("no table yet: %v", err)
	}
	if db.Migrator().HasTable("pay_merchants") {
		t.Fatal("the live-switch step created pay_merchants; AutoMigrate owns that")
	}
}

// AutoMigrate cannot run under SQLite, so the call site is asserted: the step
// must run BEFORE AutoMigrate, or AutoMigrate adds the column first and the
// backfill never runs at all.
func TestAutoMigrate_RunsTheLiveSwitchStepFirst(t *testing.T) {
	src, err := os.ReadFile("database.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)[strings.Index(string(src), "func AutoMigrate() error {"):]
	step, migrate := strings.Index(body, "migratePayMerchantLiveSwitch(DB)"), strings.Index(body, "DB.AutoMigrate(")
	if step < 0 || migrate < 0 || step > migrate {
		t.Fatalf("migratePayMerchantLiveSwitch(DB) must be called in AutoMigrate before DB.AutoMigrate (step at %d, AutoMigrate at %d)", step, migrate)
	}
}
