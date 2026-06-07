package services

import (
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// FeeConfigService is the single source of truth for per-feature fee rates.
//
// It loads all active models.FeeConfig rows into an in-memory cache and
// answers RateFor(feature, token) lookups. On first use against an empty table
// it seeds DEFAULTS that mirror the current hardcoded fee constants exactly —
// this is a behavior-preserving refactor, so the seeded rates produce the same
// fees as the constants they replace.
//
// The cache is invalidated explicitly (Invalidate) when the admin API mutates
// a row.
type FeeConfigService struct {
	db    *gorm.DB
	mu    sync.RWMutex
	cache map[string]models.FeeConfig // key: feature + "|" + tokenSymbol(upper)
}

// Fees is the process-wide FeeConfigService singleton. It is initialized at
// boot via InitFeeConfigService. Call sites that are not struct methods (e.g.
// CalculatePlatformFee, GetFeeAmount, withdrawal/swap helpers) read rates
// through the FeeRate / FeeFlatUSD package helpers, which fall back to the
// built-in defaults when the singleton has not been initialized (tests).
var Fees *FeeConfigService

// feeDefault describes one seeded fee_config row. Values mirror the current
// hardcoded constants — see fee_config_service.go DefaultFeeConfigs.
type feeDefault struct {
	Feature     string
	TokenSymbol string
	BPS         int
	FlatUSD     float64
	MCGPExempt  bool
}

// DefaultFeeConfigs returns the canonical seed set. Each value is pinned to the
// current production behavior so seeding an empty table changes no fee math:
//
//   - product_merchant: models.MerchantFeeBPS (200 = 2%), MCGP-exempt
//     (CalculatePlatformFee returns 0 for MCGP today).
//   - product: buyer pays 0% platform fee today (CartSummary.PlatformFee = 0);
//     a flat per-order gas fee of models.GasFeeUSD ($0.10) applies for non-MCGP
//     orders, MCGP-exempt.
//   - service_contact: $0.10 flat (on-chain feeAmount = 100000 @ 6dp); MCGP
//     unsupported by the contract today, so it is left exempt.
//   - withdrawal: 0.5% platform fee (transaction_handler.calculateWithdrawalFees).
//   - swap: 0.1% fee (transaction_handler.CreateSwap, internal asset swap).
//   - service_other / otc / p2p / p2p_platform: rates carried for SP3+ to read;
//     no Go-side fee math is computed for these in SP1 (P2P fee is enforced
//     on-chain via the voucher feeBps; OTC has no local fee constant), so these
//     rows are informational/forward-looking and do not affect current behavior.
func DefaultFeeConfigs() []models.FeeConfig {
	defs := []feeDefault{
		// Product merchant fee — 2% baked into listing price, MCGP exempt.
		{Feature: models.FeatureProductMerchant, TokenSymbol: models.FeeTokenAny, BPS: models.MerchantFeeBPS, MCGPExempt: true},
		// Buyer product platform fee — 0% today, plus a flat $0.10 gas fee per
		// order for non-MCGP. The flat component is MCGP-exempt.
		{Feature: models.FeatureProduct, TokenSymbol: models.FeeTokenAny, BPS: 0, FlatUSD: models.GasFeeUSD, MCGPExempt: true},
		// Service contact reveal — $0.10 flat. MCGP unsupported by the contract.
		{Feature: models.FeatureServiceContact, TokenSymbol: models.FeeTokenAny, FlatUSD: 0.10, MCGPExempt: true},
		// Withdrawal — 0.5% platform fee.
		{Feature: models.FeatureWithdrawal, TokenSymbol: models.FeeTokenAny, BPS: 50},
		// Internal asset swap — 0.1% fee.
		{Feature: models.FeatureSwap, TokenSymbol: models.FeeTokenAny, BPS: 10},
		// Forward-looking rows (no SP1 Go-side math; read by SP3+).
		{Feature: models.FeatureServiceOther, TokenSymbol: models.FeeTokenAny, BPS: 50},
		{Feature: models.FeatureOTC, TokenSymbol: models.FeeTokenAny, BPS: 50},
		{Feature: models.FeatureP2P, TokenSymbol: models.FeeTokenAny, BPS: 50},
		{Feature: models.FeatureP2PPlatform, TokenSymbol: models.FeeTokenAny, BPS: 100},
	}

	out := make([]models.FeeConfig, 0, len(defs))
	for _, d := range defs {
		out = append(out, models.FeeConfig{
			// ID is generated here (rather than relying on the DB
			// gen_random_uuid() default) so seeding works on both PostgreSQL
			// and SQLite (test) backends.
			ID:            uuid.New(),
			Feature:       d.Feature,
			TokenSymbol:   d.TokenSymbol,
			BPS:           d.BPS,
			FlatAmountUSD: d.FlatUSD,
			MCGPExempt:    d.MCGPExempt,
			Active:        true,
		})
	}
	return out
}

// NewFeeConfigService builds a FeeConfigService and loads its cache. If the
// fee_configs table is empty it is seeded with DefaultFeeConfigs first.
func NewFeeConfigService(db *gorm.DB) *FeeConfigService {
	s := &FeeConfigService{db: db, cache: map[string]models.FeeConfig{}}
	if err := s.seedIfEmpty(); err != nil {
		log.Printf("[FeeConfig] seed check failed: %v", err)
	}
	if err := s.Load(); err != nil {
		log.Printf("[FeeConfig] initial load failed: %v", err)
	}
	return s
}

// InitFeeConfigService constructs the singleton and assigns it to Fees.
func InitFeeConfigService(db *gorm.DB) *FeeConfigService {
	Fees = NewFeeConfigService(db)
	return Fees
}

// seedIfEmpty inserts the default rows when the table has no rows yet,
// mirroring the SupportedToken auto-seed pattern.
func (s *FeeConfigService) seedIfEmpty() error {
	if s.db == nil {
		return nil
	}
	var count int64
	if err := s.db.Model(&models.FeeConfig{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, fc := range DefaultFeeConfigs() {
		if err := s.db.Create(&fc).Error; err != nil {
			log.Printf("[FeeConfig] failed to seed %s/%s: %v", fc.Feature, fc.TokenSymbol, err)
		}
	}
	log.Printf("[FeeConfig] seeded %d default fee rows", len(DefaultFeeConfigs()))
	return nil
}

// Load (re)reads all active rows into the cache.
func (s *FeeConfigService) Load() error {
	if s.db == nil {
		return nil
	}
	var rows []models.FeeConfig
	if err := s.db.Where("active = ?", true).Find(&rows).Error; err != nil {
		return err
	}
	cache := make(map[string]models.FeeConfig, len(rows))
	for _, r := range rows {
		cache[cacheKey(r.Feature, r.TokenSymbol)] = r
	}
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
	return nil
}

// Invalidate reloads the cache from the database. Call after an admin mutation.
func (s *FeeConfigService) Invalidate() error {
	return s.Load()
}

func cacheKey(feature, token string) string {
	return feature + "|" + strings.ToUpper(token)
}

// RateFor returns the fee rate for (feature, tokenSymbol).
//
//   - A token-specific active row is preferred over the FeeTokenAny ("*") row.
//   - If the matched row is MCGPExempt and the token is MCGP, exempt=true is
//     returned with zero rates (mirrors today's MCGP fee exemption).
//   - found=false means no active row exists for the feature at all.
func (s *FeeConfigService) RateFor(feature, tokenSymbol string) (bps int, flatUSD float64, exempt bool, found bool) {
	upperToken := strings.ToUpper(strings.TrimSpace(tokenSymbol))

	s.mu.RLock()
	row, ok := s.cache[cacheKey(feature, upperToken)]
	if !ok {
		row, ok = s.cache[cacheKey(feature, models.FeeTokenAny)]
	}
	s.mu.RUnlock()

	if !ok {
		return 0, 0, false, false
	}

	if row.MCGPExempt && upperToken == "MCGP" {
		return 0, 0, true, true
	}

	return row.BPS, row.FlatAmountUSD, false, true
}

// All returns all cached active rows (used by tests / diagnostics).
func (s *FeeConfigService) All() []models.FeeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.FeeConfig, 0, len(s.cache))
	for _, r := range s.cache {
		out = append(out, r)
	}
	return out
}

// --- Package-level helpers with built-in default fallback ---
//
// These let non-struct call sites read rates without threading a service
// reference everywhere. When Fees is initialized they consult the DB-backed
// cache; otherwise they fall back to DefaultFeeConfigs so behavior (and tests)
// remain correct even before InitFeeConfigService runs.

func defaultRateFor(feature, tokenSymbol string) (bps int, flatUSD float64, exempt bool, found bool) {
	upperToken := strings.ToUpper(strings.TrimSpace(tokenSymbol))
	var anyRow *models.FeeConfig
	for i := range defaultFeeRows {
		r := &defaultFeeRows[i]
		if r.Feature != feature {
			continue
		}
		if strings.ToUpper(r.TokenSymbol) == upperToken {
			anyRow = r
			break
		}
		if r.TokenSymbol == models.FeeTokenAny {
			anyRow = r
		}
	}
	if anyRow == nil {
		return 0, 0, false, false
	}
	if anyRow.MCGPExempt && upperToken == "MCGP" {
		return 0, 0, true, true
	}
	return anyRow.BPS, anyRow.FlatAmountUSD, false, true
}

// defaultFeeRows is the in-memory copy of the seed used for the fallback path.
var defaultFeeRows = DefaultFeeConfigs()

// FeeRate returns (bps, exempt) for a feature/token, consulting the singleton
// when available and the built-in defaults otherwise.
func FeeRate(feature, tokenSymbol string) (bps int, exempt bool) {
	if Fees != nil {
		b, _, ex, found := Fees.RateFor(feature, tokenSymbol)
		if found {
			return b, ex
		}
	}
	b, _, ex, _ := defaultRateFor(feature, tokenSymbol)
	return b, ex
}

// FeeFlatUSD returns (flatUSD, exempt) for a feature/token.
func FeeFlatUSD(feature, tokenSymbol string) (flatUSD float64, exempt bool) {
	if Fees != nil {
		_, flat, ex, found := Fees.RateFor(feature, tokenSymbol)
		if found {
			return flat, ex
		}
	}
	_, flat, ex, _ := defaultRateFor(feature, tokenSymbol)
	return flat, ex
}
