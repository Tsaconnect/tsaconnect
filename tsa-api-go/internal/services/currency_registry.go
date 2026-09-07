package services

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrUnknownCurrency is returned when a code has no row in the registry. It is
// deliberately an error rather than a zero value: a money path that silently
// treated an unknown currency as "the default" would denominate an amount in
// the wrong currency, which for NGN vs GHS is roughly a 100x error.
var ErrUnknownCurrency = errors.New("currency is not supported")

// ErrCurrencyNotDepositable / ErrCurrencyNotPayable are the enabled-flag
// refusals, kept separate from ErrUnknownCurrency so callers (and operators
// reading logs) can tell "we've never heard of this" from "we know it but it is
// switched off for this direction".
var (
	ErrCurrencyNotDepositable = errors.New("currency is not enabled for deposits")
	ErrCurrencyNotPayable     = errors.New("currency is not enabled for payouts")
)

// Ramp providers. Empty means the stablecoin ramp is unavailable in that
// currency and the app says so.
const (
	RampProviderPaycrest    = "paycrest"
	RampProviderFlutterwave = "flutterwave"
	RampProviderNone        = ""
)

// defaultRampProviders is the compiled-in fallback for a row whose
// ramp_provider column is empty — which every row is immediately after the
// column is added, since seedMissing only ever INSERTs.
//
// This is what makes the deploy safe in any order: without it, adding the
// column would take NGN offline until a backfill ran, and a backfill that has
// to run BEFORE the code is a deploy hazard nobody remembers at 3am.
var defaultRampProviders = map[string]string{
	"NGN": RampProviderPaycrest,
	"GHS": RampProviderFlutterwave,
}

// DefaultCurrencies is the seed set. See the multi-currency spec §4.1.
//
// Every entry is Exponent 2, which is not a coincidence: Flutterwave's
// fiat<->stablecoin conversion set is NGN/USD/GHS/EUR/GBP and all five happen
// to be 2-decimal, so the existing kobo arithmetic stays correct. The first
// 0-decimal (XOF, UGX) or 3-decimal (TND, KWD) currency added here is the one
// that makes Exponent load-bearing.
//
// MinPayoutMinor is seeded from the ONE floor we have confirmed — Flutterwave's
// ₦100 TRANSFER_AMOUNT_LIMIT. The others are 0 (ungated) until each is
// sandbox-confirmed per currency and rail; see the plan's open questions.
func DefaultCurrencies() []models.Currency {
	return []models.Currency{
		{
			// Nigeria: the live rail. Deposits require BVN/NIN in FW live mode.
			// Priced off the Bybit P2P parallel market, not OER official.
			Code: "NGN", Exponent: 2,
			DepositEnabled: true, PayoutEnabled: true,
			RequiresNationalID: true,
			IsBaseFloat:        true,
			HasMarketAnchor:    true,
			MinPayoutMinor:     10000, // ₦100 — FW TRANSFER_AMOUNT_LIMIT
			RampProvider:       RampProviderPaycrest,
		},
		{
			// Ghana: the second live rail. FW documents NO identity document
			// for GHS virtual accounts, which is why deposits here do not need
			// a per-country KYC model — only that the NGN gate stop being
			// unconditional. Market-anchored: Bybit P2P quotes GHS.
			Code: "GHS", Exponent: 2,
			DepositEnabled: true, PayoutEnabled: true,
			RequiresNationalID: false,
			IsBaseFloat:        true,
			HasMarketAnchor:    true,
			MinPayoutMinor:     0, // unconfirmed — sandbox-verify before enabling GHS payouts
			RampProvider:       RampProviderFlutterwave,
		},
		// East African corridors. Payout-enabled because Paycrest pays out in
		// them, but RampProvider is deliberately left empty (RampProviderNone) —
		// buy.tsx/sell.tsx are not currency-aware yet (they call getQuote and
		// createBuy/createSell with no currency, which server-defaults to NGN),
		// so advertising a live ramp here would hand a Kenyan/Ugandan/Tanzanian
		// user a naira order: a ₦ amount field, a Nigerian refund-bank picker,
		// and instructions to wire naira to a Nigerian account. The row stays —
		// it is a valid payout currency and the per-currency institution list
		// already works — only the ramp claim goes. Enabling it later is one
		// UPDATE plus currency-aware screens.
		//
		// MinPayoutMinor is 0 (ungated) because no floor has been confirmed for
		// these rails — same position GHS is in. Confirm before volume.
		{Code: "KES", Exponent: 2, PayoutEnabled: true, HasMarketAnchor: true},
		{Code: "UGX", Exponent: 0, PayoutEnabled: true},
		{Code: "TZS", Exponent: 2, PayoutEnabled: true},
		// USD/EUR/GBP: payout-only, permanently. Flutterwave has no virtual
		// account product for them, so DepositEnabled can never become true
		// here — a balance in these arrives by settlement or treasury wire.
		// Not market-anchored: priced off OER's official cross.
		{Code: "USD", Exponent: 2, PayoutEnabled: true},
		{Code: "EUR", Exponent: 2, PayoutEnabled: true},
		{Code: "GBP", Exponent: 2, PayoutEnabled: true},
	}
}

// CurrencyRegistry is the read path for the currencies table. Rows change only
// by deploy or operator action, so the whole table is cached in memory and
// refreshed explicitly rather than read per request.
type CurrencyRegistry struct {
	db *gorm.DB

	mu    sync.RWMutex
	cache map[string]models.Currency
}

// NewCurrencyRegistry seeds any missing rows, loads the table, and returns the
// registry. A nil db yields a registry that reports every currency unknown,
// which is the correct fail-closed posture for a money path.
func NewCurrencyRegistry(db *gorm.DB) *CurrencyRegistry {
	r := &CurrencyRegistry{db: db, cache: map[string]models.Currency{}}
	if db == nil {
		return r
	}
	if err := r.seedMissing(); err != nil {
		log.Printf("[currency] seed failed: %v", err)
	}
	if err := r.Load(); err != nil {
		log.Printf("[currency] load failed: %v", err)
	}
	return r
}

// seedMissing inserts seed rows that are absent and NEVER updates rows that
// already exist.
//
// This is deliberately per-row rather than the seed-if-table-empty pattern used
// by fee_config. Two properties matter and only per-row insert gives both:
// adding a sixth currency in a later release actually seeds it (an empty-table
// guard would skip it forever), and an operator who tunes min_payout_minor in
// production does not have that value silently reverted on the next deploy.
func (r *CurrencyRegistry) seedMissing() error {
	var existing []string
	if err := r.db.Model(&models.Currency{}).Pluck("code", &existing).Error; err != nil {
		return err
	}
	have := make(map[string]bool, len(existing))
	for _, c := range existing {
		have[strings.ToUpper(c)] = true
	}

	var added int
	for _, c := range DefaultCurrencies() {
		if have[c.Code] {
			continue
		}
		if err := r.db.Create(&c).Error; err != nil {
			return fmt.Errorf("seed %s: %w", c.Code, err)
		}
		added++
	}
	if added > 0 {
		log.Printf("[currency] seeded %d new currency row(s)", added)
	}
	return nil
}

// Load refreshes the in-memory cache from the table. On error the existing
// cache is left untouched — a transient DB blip must not empty the registry and
// turn every currency into ErrUnknownCurrency mid-flight.
func (r *CurrencyRegistry) Load() error {
	if r.db == nil {
		return nil
	}
	var rows []models.Currency
	if err := r.db.Find(&rows).Error; err != nil {
		return err
	}
	next := make(map[string]models.Currency, len(rows))
	for _, row := range rows {
		next[strings.ToUpper(strings.TrimSpace(row.Code))] = row
	}

	r.mu.Lock()
	r.cache = next
	r.mu.Unlock()
	return nil
}

// Get returns the registry row for code, or ErrUnknownCurrency. Codes are
// matched case-insensitively and trimmed.
func (r *CurrencyRegistry) Get(code string) (models.Currency, error) {
	key := strings.ToUpper(strings.TrimSpace(code))
	if key == "" {
		return models.Currency{}, fmt.Errorf("%w: (empty)", ErrUnknownCurrency)
	}

	r.mu.RLock()
	row, ok := r.cache[key]
	r.mu.RUnlock()
	if !ok {
		return models.Currency{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, key)
	}
	return row, nil
}

// RequireDeposit resolves code and asserts it may be paid into.
func (r *CurrencyRegistry) RequireDeposit(code string) (models.Currency, error) {
	row, err := r.Get(code)
	if err != nil {
		return models.Currency{}, err
	}
	if !row.DepositEnabled {
		return models.Currency{}, fmt.Errorf("%w: %s", ErrCurrencyNotDepositable, row.Code)
	}
	return row, nil
}

// RequirePayout resolves code and asserts it may be paid out in.
func (r *CurrencyRegistry) RequirePayout(code string) (models.Currency, error) {
	row, err := r.Get(code)
	if err != nil {
		return models.Currency{}, err
	}
	if !row.PayoutEnabled {
		return models.Currency{}, fmt.Errorf("%w: %s", ErrCurrencyNotPayable, row.Code)
	}
	return row, nil
}

// RampProvider names who serves the stablecoin ramp for a currency, or
// RampProviderNone.
//
// An explicit column value always wins, so an operator can disable a currency
// by setting it to something unrecognised without a deploy. An EMPTY column
// resolves against the compiled-in default instead of being read literally —
// see defaultRampProviders for why.
//
// An unknown currency is not an error here: the caller is asking "can I ramp
// in this?", and "no" is a complete answer.
func (r *CurrencyRegistry) RampProvider(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	row, err := r.Get(code)
	if err != nil {
		return RampProviderNone
	}
	if p := strings.TrimSpace(row.RampProvider); p != "" {
		return p
	}
	return defaultRampProviders[code]
}

// All returns every registry row, for admin listings and diagnostics.
func (r *CurrencyRegistry) All() []models.Currency {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]models.Currency, 0, len(r.cache))
	for _, row := range r.cache {
		out = append(out, row)
	}
	return out
}
