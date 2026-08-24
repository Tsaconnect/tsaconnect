package services

import (
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newCurrencyTestDB builds an in-memory SQLite DB with the currencies table via
// raw DDL, matching the repo convention (AutoMigrate would drag in
// PostgreSQL-specific defaults).
func newCurrencyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS currencies (
		code                 TEXT PRIMARY KEY,
		exponent             INTEGER NOT NULL DEFAULT 2,
		deposit_enabled      BOOLEAN NOT NULL DEFAULT 0,
		payout_enabled       BOOLEAN NOT NULL DEFAULT 0,
		requires_national_id BOOLEAN NOT NULL DEFAULT 0,
		is_base_float        BOOLEAN NOT NULL DEFAULT 0,
		has_market_anchor    BOOLEAN NOT NULL DEFAULT 0,
		min_payout_minor     INTEGER NOT NULL DEFAULT 0,
		created_at           DATETIME,
		updated_at           DATETIME
	)`).Error; err != nil {
		t.Fatalf("create currencies: %v", err)
	}
	return db
}

func TestCurrencyRegistrySeedsDefaults(t *testing.T) {
	db := newCurrencyTestDB(t)
	r := NewCurrencyRegistry(db)

	var count int64
	db.Model(&models.Currency{}).Count(&count)
	if int(count) != len(DefaultCurrencies()) {
		t.Fatalf("seeded %d rows, want %d", count, len(DefaultCurrencies()))
	}

	// Spot-check the two properties the rest of the work depends on rather than
	// re-asserting the whole seed literal (which would just restate itself).
	ngn, err := r.Get("NGN")
	if err != nil {
		t.Fatalf("Get(NGN): %v", err)
	}
	if !ngn.RequiresNationalID {
		t.Error("NGN must require a national ID — Flutterwave enforces BVN/NIN in live mode")
	}
	if ngn.MinPayoutMinor != 10000 {
		t.Errorf("NGN floor = %d, want 10000 (₦100 FW TRANSFER_AMOUNT_LIMIT)", ngn.MinPayoutMinor)
	}

	ghs, err := r.Get("GHS")
	if err != nil {
		t.Fatalf("Get(GHS): %v", err)
	}
	if ghs.RequiresNationalID {
		t.Error("GHS must NOT require a national ID — FW documents no equivalent, and " +
			"requiring one would block Ghanaian deposits for no reason")
	}
	if !ghs.DepositEnabled {
		t.Error("GHS must be depositable — it is the second currency FW can collect into")
	}
}

// TestCurrencyRegistryUsdEurGbpArePayoutOnly pins the asymmetry that shapes the
// whole design: Flutterwave has no virtual-account product for these, so they
// can never be deposit currencies no matter what else changes.
func TestCurrencyRegistryUsdEurGbpArePayoutOnly(t *testing.T) {
	r := NewCurrencyRegistry(newCurrencyTestDB(t))

	for _, code := range []string{"USD", "EUR", "GBP"} {
		row, err := r.Get(code)
		if err != nil {
			t.Fatalf("Get(%s): %v", code, err)
		}
		if row.DepositEnabled {
			t.Errorf("%s must not be deposit-enabled: FW has no VA product for it", code)
		}
		if !row.PayoutEnabled {
			t.Errorf("%s must be payout-enabled", code)
		}
		if _, err := r.RequireDeposit(code); !errors.Is(err, ErrCurrencyNotDepositable) {
			t.Errorf("RequireDeposit(%s) = %v, want ErrCurrencyNotDepositable", code, err)
		}
	}
}

// TestCurrencyRegistrySeedDoesNotClobberOperatorValues is the reason seeding is
// per-row rather than seed-if-empty. Payout floors are tuned in production once
// each currency is sandbox-confirmed; a seed that rewrote existing rows would
// silently revert that tuning on every deploy, re-opening the stranded-premium
// hole the floor exists to close.
func TestCurrencyRegistrySeedDoesNotClobberOperatorValues(t *testing.T) {
	db := newCurrencyTestDB(t)
	_ = NewCurrencyRegistry(db)

	// Operator tunes the GHS floor after sandbox-confirming it.
	if err := db.Model(&models.Currency{}).
		Where("code = ?", "GHS").
		Update("min_payout_minor", 500).Error; err != nil {
		t.Fatalf("operator update: %v", err)
	}

	// Redeploy: a second registry over the same DB.
	r2 := NewCurrencyRegistry(db)

	ghs, err := r2.Get("GHS")
	if err != nil {
		t.Fatalf("Get(GHS): %v", err)
	}
	if ghs.MinPayoutMinor != 500 {
		t.Fatalf("operator-tuned floor was clobbered: got %d, want 500", ghs.MinPayoutMinor)
	}

	var count int64
	db.Model(&models.Currency{}).Count(&count)
	if int(count) != len(DefaultCurrencies()) {
		t.Fatalf("re-init changed row count: got %d, want %d", count, len(DefaultCurrencies()))
	}
}

// TestCurrencyRegistrySeedsNewlyAddedCurrency covers the other half: a currency
// added to the seed set in a later release must actually land, which the
// seed-if-table-empty pattern would skip forever.
func TestCurrencyRegistrySeedsNewlyAddedCurrency(t *testing.T) {
	db := newCurrencyTestDB(t)
	_ = NewCurrencyRegistry(db)

	// Simulate a pre-existing deployment that predates one of the seeds.
	if err := db.Where("code = ?", "GBP").Delete(&models.Currency{}).Error; err != nil {
		t.Fatalf("delete GBP: %v", err)
	}

	r2 := NewCurrencyRegistry(db)
	if _, err := r2.Get("GBP"); err != nil {
		t.Fatalf("GBP was not re-seeded on the next init: %v", err)
	}
}

// TestCurrencyRegistryRejectsUnknown is the guard that makes this table the sole
// validation authority. currencyMetadata knows ~100 ISO codes including JPY,
// whose minor unit is 0 decimals — every amount helper here assumes 2, so a JPY
// amount reaching a money path is a 100x error. The registry must refuse it even
// though the display metadata recognises it.
func TestCurrencyRegistryRejectsUnknown(t *testing.T) {
	r := NewCurrencyRegistry(newCurrencyTestDB(t))

	for _, code := range []string{"JPY", "KWD", "XOF", "ZZZ", ""} {
		if _, err := r.Get(code); !errors.Is(err, ErrUnknownCurrency) {
			t.Errorf("Get(%q) = %v, want ErrUnknownCurrency", code, err)
		}
	}

	// JPY is display-known but must not be money-path-known. If this ever
	// fails, isKnownCurrency has been wired into a money path by mistake.
	if !isKnownCurrency("JPY") {
		t.Skip("JPY absent from display metadata; the contrast this test draws no longer holds")
	}
	if _, err := r.RequirePayout("JPY"); !errors.Is(err, ErrUnknownCurrency) {
		t.Error("JPY is display-known but must be rejected by the registry: " +
			"its 0-decimal minor unit would be misread as 2 by the kobo helpers")
	}
}

func TestCurrencyRegistryNormalisesCode(t *testing.T) {
	r := NewCurrencyRegistry(newCurrencyTestDB(t))

	for _, code := range []string{"ngn", " NGN ", "Ngn"} {
		row, err := r.Get(code)
		if err != nil {
			t.Fatalf("Get(%q): %v", code, err)
		}
		if row.Code != "NGN" {
			t.Errorf("Get(%q).Code = %q, want NGN", code, row.Code)
		}
	}
}

// TestCurrencyRegistryNilDBFailsClosed pins the fail-closed posture: with no DB
// the registry knows nothing, and every money path that consults it refuses.
func TestCurrencyRegistryNilDBFailsClosed(t *testing.T) {
	r := NewCurrencyRegistry(nil)
	if _, err := r.Get("NGN"); !errors.Is(err, ErrUnknownCurrency) {
		t.Errorf("nil-db Get(NGN) = %v, want ErrUnknownCurrency", err)
	}
}
