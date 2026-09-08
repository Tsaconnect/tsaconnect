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
		ramp_provider        TEXT NOT NULL DEFAULT '',
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

// TestRegistryGatesPayout_NotJustTheCatalog pins that the registry is consulted
// as an authority, not merely as a source of floors.
//
// Two lists govern a payout currency: the withdrawal catalog (what we OFFER as
// a product) and the registry (whether the currency may move money at all).
// They can drift — disabling a currency in the registry while it is still in the
// catalog is exactly how an operator would expect to switch it off in a hurry.
// If only the catalog is checked, that switch does nothing.
func TestRegistryGatesPayout_NotJustTheCatalog(t *testing.T) {
	db := newCurrencyTestDB(t)
	reg := NewCurrencyRegistry(db)

	if _, err := reg.RequirePayout("GHS"); err != nil {
		t.Fatalf("GHS should be payable when enabled: %v", err)
	}

	// Operator switches GHS off without touching the product catalog.
	if err := db.Model(&models.Currency{}).Where("code = ?", "GHS").
		Update("payout_enabled", false).Error; err != nil {
		t.Fatalf("disable GHS: %v", err)
	}
	if err := reg.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if _, err := reg.RequirePayout("GHS"); !errors.Is(err, ErrCurrencyNotPayable) {
		t.Errorf("RequirePayout(GHS) = %v, want ErrCurrencyNotPayable — disabling a currency "+
			"in the registry must stop payouts even while it remains in the catalog", err)
	}
}

// NGN is the ONLY currency the ramp actually serves today, and both places
// that can claim otherwise are asserted here.
//
// The regression this guards: GHS was seeded with RampProviderFlutterwave, so
// /it/currencies advertised two ramp currencies and the app's IT hub grew a
// currency picker. Selecting GHS changed nothing — ActivateFiatVA mints NGN
// accounts only, sells hard-code Currency "NGN", and the app's stablecoin
// screens quote and settle in naira regardless of what they are handed. A
// picker whose choice is silently ignored is worse than no picker.
//
// Adding a ramp currency means making the buy/sell path currency-aware first,
// then updating this test on purpose.
func TestDefaultCurrencies_OnlyNGNHasARamp(t *testing.T) {
	for _, c := range DefaultCurrencies() {
		want := RampProviderNone
		if c.Code == "NGN" {
			want = RampProviderPaycrest
		}
		if c.RampProvider != want {
			t.Errorf("%s ramp = %q, want %q — the ramp is NGN-only until the buy/sell path is currency-aware",
				c.Code, c.RampProvider, want)
		}
	}
	for code, provider := range defaultRampProviders {
		if code != "NGN" {
			t.Errorf("defaultRampProviders[%s] = %q; the fallback must not resurrect a ramp the screens cannot serve",
				code, provider)
		}
	}
}

func TestCurrencyRegistry_RampProviderFallsBackWhenColumnEmpty(t *testing.T) {
	db := newCurrencyTestDB(t)
	// A row that predates the column: ramp_provider is the zero value.
	if err := db.Create(&models.Currency{Code: "NGN", Exponent: 2,
		DepositEnabled: true, PayoutEnabled: true}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := NewCurrencyRegistry(db)
	if err := r.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// An empty column must NOT read as "no ramp" — that would take the live
	// currency offline the moment the column is added.
	if got := r.RampProvider("NGN"); got != RampProviderPaycrest {
		t.Fatalf("RampProvider(NGN) = %q, want %q from the compiled-in default", got, RampProviderPaycrest)
	}
	// An explicit value in the column wins over the default.
	if err := db.Model(&models.Currency{}).Where("code = ?", "NGN").
		Update("ramp_provider", RampProviderFlutterwave).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := r.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := r.RampProvider("NGN"); got != RampProviderFlutterwave {
		t.Fatalf("RampProvider(NGN) = %q, want the column value to win", got)
	}
	// A currency with no ramp anywhere stays empty.
	if got := r.RampProvider("USD"); got != RampProviderNone {
		t.Fatalf("RampProvider(USD) = %q, want none", got)
	}
	// An unknown code is not an error, just no ramp.
	if got := r.RampProvider("XYZ"); got != RampProviderNone {
		t.Fatalf("RampProvider(XYZ) = %q, want none", got)
	}
}

func TestDefaultCurrencies_EastAfricanCorridors(t *testing.T) {
	byCode := map[string]models.Currency{}
	for _, c := range DefaultCurrencies() {
		byCode[c.Code] = c
	}
	for _, code := range []string{"KES", "UGX", "TZS"} {
		c, ok := byCode[code]
		if !ok {
			t.Fatalf("%s missing from DefaultCurrencies", code)
		}
		// RampProvider must be empty (RampProviderNone), NOT paycrest: buy.tsx
		// and sell.tsx are not currency-aware (they call getQuote/createBuy/
		// createSell with no currency, which server-defaults to NGN), so
		// advertising a live ramp here would hand a Kenyan/Ugandan/Tanzanian
		// user a naira order — a ₦ amount field, a Nigerian refund-bank
		// picker, and instructions to wire naira to a Nigerian account. The
		// row itself stays a valid payout currency; only the ramp claim goes.
		if c.RampProvider != RampProviderNone {
			t.Errorf("%s ramp = %q, want none (screens are NGN-only)", code, c.RampProvider)
		}
		if !c.PayoutEnabled {
			t.Errorf("%s must be payout-enabled — Paycrest pays out in it", code)
		}
		// DepositEnabled means "Flutterwave has a VA product here". Nothing
		// uses it for these currencies, and a true we have never exercised is
		// a claim nobody has verified.
		if c.DepositEnabled {
			t.Errorf("%s DepositEnabled must be false; nothing exercises FW VAs there", code)
		}
	}
}
