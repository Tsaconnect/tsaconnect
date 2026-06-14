package services

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// MaxFeeBPS is the backend mirror of the on-chain FeeManager MAX_BPS (10%, the
// fat-finger ceiling — design §3/§11). RateFor CLAMPS every resolved rate to
// [0, MaxFeeBPS] so a corrupt replica row can never produce an unbounded charge.
const MaxFeeBPS = 1000

// feeCacheMissTotal counts cold-start / empty-replica fallbacks down the §11
// ladder (cache miss → last-known-good → seed default). It is a loud-alert hook
// for a metrics exporter (mirrors the fee_rate_cache_miss_total counter named in
// the design); every increment is also logged at error level. Read via
// FeeCacheMissTotal().
var feeCacheMissTotal atomic.Int64

// FeeCacheMissTotal returns the running count of §11 fallback-ladder misses.
func FeeCacheMissTotal() int64 { return feeCacheMissTotal.Load() }

// FeeConfigService is the backend READ REPLICA of the on-chain fee rates
// (configurable-FeeManager design §2/§4). The on-chain FeeManager is the write
// side / source of truth; this service's fee_config-backed cache is refreshed
// from CONFIRMED on-chain RateSet values (RefreshFromConfirmedRate) and is the
// only thing read on the charging hot-path. It NEVER does a live eth_call.
//
// It loads all active models.FeeConfig rows into an in-memory cache and answers
// RateFor(feature, token, chainID) lookups. On first use against an empty table
// it seeds DEFAULTS that mirror the current hardcoded fee constants exactly —
// this is a behavior-preserving refactor, so the seeded rates produce the same
// fees as the constants they replace.
//
// Resolution is per-chain (design §4/§11): exact (feature, token, chain) →
// (feature, token, NULL global) → (feature, "*", chain) → (feature, "*", NULL).
// On a total miss the §11 NO-$0/NO-UNBOUNDED ladder kicks in: last-known-good →
// seed default + loud alert; the resolved bps is always clamped to [0, MaxFeeBPS].
type FeeConfigService struct {
	db    *gorm.DB
	mu    sync.RWMutex
	cache map[string]models.FeeConfig // key: feature|TOKEN|chain (chain "" = NULL global)

	// lastKnownGood is a persisted snapshot of the last SUCCESSFUL replica load,
	// kept separate from the live cache (which Invalidate can transiently empty
	// or a cold start may leave bare). It is the §11 step-3 tier consulted before
	// the seed default so a hot-path lookup during a reload never falls all the
	// way to the seed.
	lkgMu         sync.RWMutex
	lastKnownGood map[string]models.FeeConfig
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
//   - product_merchant: 200 bps (2%), MCGP-exempt (CalculatePlatformFee returns
//     0 for MCGP today). This is the merchant rate the cart quote AND the
//     on-chain charge both resolve through this service (design §6).
//   - product: buyer pays 0% platform fee today (CartSummary.PlatformFee = 0);
//     a flat per-order gas fee of $0.10 applies for non-MCGP orders, MCGP-exempt.
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
		// Product merchant fee — 2% (200 bps) baked into listing price, MCGP exempt.
		// This is the single source of truth for the merchant rate; the cart quote
		// (CalculateCartSummary) and the on-chain charge (CalculatePlatformFee) both
		// resolve it here, so quote == charge (design §6).
		{Feature: models.FeatureProductMerchant, TokenSymbol: models.FeeTokenAny, BPS: 200, MCGPExempt: true},
		// Buyer product platform fee — 0% today, plus a flat $0.10 gas fee per
		// order for non-MCGP. The flat component is MCGP-exempt. The cart quote and
		// the PrepareApprove charge both read this flat (design §6).
		{Feature: models.FeatureProduct, TokenSymbol: models.FeeTokenAny, BPS: 0, FlatUSD: 0.10, MCGPExempt: true},
		// Service contact reveal — $0.10 flat. MCGP unsupported by the contract.
		{Feature: models.FeatureServiceContact, TokenSymbol: models.FeeTokenAny, FlatUSD: 0.10, MCGPExempt: true},
		// Withdrawal — 0.5% platform fee.
		{Feature: models.FeatureWithdrawal, TokenSymbol: models.FeeTokenAny, BPS: 50},
		// Internal asset (spot) swap — 3% fee, so the 30% TP split = 0.9% TP per
		// the PM spec (matches allswap). FeatureSwap is the rate CreateSwap charges.
		{Feature: models.FeatureSwap, TokenSymbol: models.FeeTokenAny, BPS: 300},
		// Forward-looking rows (no SP1 Go-side math; read by SP3+).
		{Feature: models.FeatureServiceOther, TokenSymbol: models.FeeTokenAny, BPS: 50},
		// OTC — 2% fee → 0.6% TP (PM spec).
		{Feature: models.FeatureOTC, TokenSymbol: models.FeeTokenAny, BPS: 200},
		{Feature: models.FeatureP2P, TokenSymbol: models.FeeTokenAny, BPS: 50},
		{Feature: models.FeatureP2PPlatform, TokenSymbol: models.FeeTokenAny, BPS: 100},
		// Forward-looking rows for features that don't exist as Go-side flows yet
		// (design §2). No current call site computes a fee from FeatureInstantPay /
		// FeatureInstantTransfer, so their rates are read-only until those features
		// ship. Rates carry the PM-spec TP percentages via the v2 split table:
		// spot 3%→0.9%, instant_pay 0.5%→0.15%, instant_transfer 2%→0.25%.
		{Feature: models.FeatureSpot, TokenSymbol: models.FeeTokenAny, BPS: 300},
		{Feature: models.FeatureInstantPay, TokenSymbol: models.FeeTokenAny, BPS: 50},
		{Feature: models.FeatureInstantTransfer, TokenSymbol: models.FeeTokenAny, BPS: 200},
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
	s := &FeeConfigService{db: db, cache: map[string]models.FeeConfig{}, lastKnownGood: map[string]models.FeeConfig{}}
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
		// #5b guard: never seed a per-token product fee row (the seed set is all
		// wildcard today; this guards a future edit that would silently let the
		// cart quote diverge from the charge).
		if productFeeWildcardOnly(fc.Feature, fc.TokenSymbol) {
			return errProductPerTokenOverride(fc.Feature, fc.TokenSymbol)
		}
		if err := s.db.Create(&fc).Error; err != nil {
			log.Printf("[FeeConfig] failed to seed %s/%s: %v", fc.Feature, fc.TokenSymbol, err)
		}
	}
	log.Printf("[FeeConfig] seeded %d default fee rows", len(DefaultFeeConfigs()))
	return nil
}

// Load (re)reads all active rows into the cache. On success the freshly loaded
// map also becomes the last-known-good snapshot (§11 step 3); a failed load
// leaves both the live cache AND last-known-good untouched (chain wins, but a
// transient RPC/DB blip must never erase the safety tier).
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
		// #5b guard: a per-token product fee row would make the token-agnostic
		// cart quote diverge from the per-token charge. Reject at load (a row this
		// shape should never be written — RefreshFromConfirmedRate refuses it — so
		// finding one means a hand-edited/corrupt DB; fail loudly rather than serve
		// a quote that can mismatch the charge).
		if productFeeWildcardOnly(r.Feature, r.TokenSymbol) {
			return errProductPerTokenOverride(r.Feature, r.TokenSymbol)
		}
		cache[cacheKey(r.Feature, r.TokenSymbol, r.ChainID)] = r
	}
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()

	// Promote the just-loaded map to last-known-good. Snapshot a copy so a later
	// Invalidate (which replaces s.cache) can't mutate the LKG out from under a
	// concurrent hot-path lookup. Only promote a NON-EMPTY load — an empty table
	// must not clobber a previously good snapshot.
	if len(cache) > 0 {
		lkg := make(map[string]models.FeeConfig, len(cache))
		for k, v := range cache {
			lkg[k] = v
		}
		s.lkgMu.Lock()
		s.lastKnownGood = lkg
		s.lkgMu.Unlock()
	}
	return nil
}

// Invalidate reloads the cache from the database. Call after an admin mutation.
func (s *FeeConfigService) Invalidate() error {
	return s.Load()
}

// cacheKey builds the per-(feature, token, chain) cache key. A nil chainID
// (the legacy global / backend-charged row) uses an empty chain segment so it
// is distinct from any numeric chain.
func cacheKey(feature, token string, chainID *int64) string {
	chain := ""
	if chainID != nil {
		chain = strconv.FormatInt(*chainID, 10)
	}
	return feature + "|" + strings.ToUpper(token) + "|" + chain
}

// RateFor returns the fee rate for (feature, tokenSymbol, chainID) from the
// read replica. It is the charging hot-path: cache-only, NEVER an eth_call.
//
// Per-chain resolution (design §4), most-specific first:
//  1. exact (feature, token, chain)
//  2. (feature, token, NULL global)
//  3. (feature, "*", chain)
//  4. (feature, "*", NULL global)
//
// A nil chainID skips the chain-specific tiers and resolves the global rows
// directly (documented-global backend-charged fees).
//
// On a TOTAL miss (cold start before the replica loaded, or an empty replica)
// the §11 NO-$0/NO-UNBOUNDED ladder runs (see resolveFallback): last-known-good
// → seed default → loud alert. It NEVER returns a silent (0,0,false,false) miss
// that a caller could turn into a $0 charge.
//
//   - If the matched row is MCGPExempt and the token is MCGP, exempt=true is
//     returned with zero rates (mirrors today's MCGP fee exemption).
//   - The resolved bps is always clamped to [0, MaxFeeBPS].
//   - found is true whenever a usable rate was produced (cache OR fallback).
func (s *FeeConfigService) RateFor(feature, tokenSymbol string, chainID *int64) (bps int, flatUSD float64, exempt bool, found bool) {
	upperToken := strings.ToUpper(strings.TrimSpace(tokenSymbol))

	s.mu.RLock()
	row, ok := s.lookupLocked(s.cache, feature, upperToken, chainID)
	s.mu.RUnlock()

	if !ok {
		// §11 ladder: cache miss → last-known-good → seed default + loud alert.
		return s.resolveFallback(feature, upperToken, chainID)
	}

	if row.MCGPExempt && upperToken == "MCGP" {
		return 0, 0, true, true
	}

	return clampBPS(row.BPS), row.FlatAmountUSD, false, true
}

// lookupLocked walks the 4-tier per-chain resolution over a given cache map.
// Caller holds the appropriate lock for the map being read.
func (s *FeeConfigService) lookupLocked(cache map[string]models.FeeConfig, feature, upperToken string, chainID *int64) (models.FeeConfig, bool) {
	if chainID != nil {
		if row, ok := cache[cacheKey(feature, upperToken, chainID)]; ok { // (1)
			return row, true
		}
	}
	if row, ok := cache[cacheKey(feature, upperToken, nil)]; ok { // (2)
		return row, true
	}
	if chainID != nil {
		if row, ok := cache[cacheKey(feature, models.FeeTokenAny, chainID)]; ok { // (3)
			return row, true
		}
	}
	if row, ok := cache[cacheKey(feature, models.FeeTokenAny, nil)]; ok { // (4)
		return row, true
	}
	return models.FeeConfig{}, false
}

// resolveFallback is the §11 ladder tail, reached only when the live cache has
// no row for (feature, token, chain). It tries last-known-good, then the
// per-feature seed default, and fires the loud cache-miss alert. It NEVER
// returns a $0 silent miss and ALWAYS clamps to [0, MaxFeeBPS].
func (s *FeeConfigService) resolveFallback(feature, upperToken string, chainID *int64) (bps int, flatUSD float64, exempt bool, found bool) {
	feeCacheMissTotal.Add(1)

	// (3a) last-known-good — a previously successful replica load.
	s.lkgMu.RLock()
	row, ok := s.lookupLocked(s.lastKnownGood, feature, upperToken, chainID)
	s.lkgMu.RUnlock()
	if ok {
		log.Printf("[FeeConfig] ALERT fee_rate_cache_miss feature=%s token=%s chain=%s -> last-known-good (cache empty)",
			feature, upperToken, chainLabel(chainID))
		if row.MCGPExempt && upperToken == "MCGP" {
			return 0, 0, true, true
		}
		return clampBPS(row.BPS), row.FlatAmountUSD, false, true
	}

	// (3b) per-feature seed default (mirrors production constants). Resolved
	// without the chain dimension — the seed has no chain rows.
	dbps, dflat, dexempt, dfound := defaultRateFor(feature, upperToken)
	if dfound {
		log.Printf("[FeeConfig] ALERT fee_rate_cache_miss feature=%s token=%s chain=%s -> SEED DEFAULT (no replica, no last-known-good)",
			feature, upperToken, chainLabel(chainID))
		if dexempt {
			return 0, 0, true, true
		}
		return clampBPS(dbps), dflat, false, true
	}

	// No seed either: the feature is genuinely unknown. This is a real
	// found=false (not a configured-but-empty row); the caller decides. Still
	// not a silent $0 charge — found=false signals "no such fee".
	log.Printf("[FeeConfig] ALERT fee_rate_cache_miss feature=%s token=%s chain=%s -> NO ROW (unknown feature)",
		feature, upperToken, chainLabel(chainID))
	return 0, 0, false, false
}

// clampBPS bounds a resolved rate to [0, MaxFeeBPS] so a corrupt replica row
// can never charge unbounded (§11 step 4).
func clampBPS(bps int) int {
	if bps < 0 {
		return 0
	}
	if bps > MaxFeeBPS {
		return MaxFeeBPS
	}
	return bps
}

func chainLabel(chainID *int64) string {
	if chainID == nil {
		return "global"
	}
	return strconv.FormatInt(*chainID, 10)
}

// productFeeWildcardOnly reports whether (feature, token) violates the product
// fee wildcard-only invariant (design §5, finding #5b). The cart QUOTE is
// computed before the buyer picks a settlement token, so it resolves the product
// fee against a representative token (cartQuoteToken = "USDC"). For the quote to
// provably equal the eventual on-chain CHARGE (which uses the order's real
// token), the product/product_merchant fee MUST be wildcard-only — a per-token
// (non-"*") override would let the quote (resolved at USDC) diverge from a charge
// in a different token. We therefore REJECT any non-wildcard product fee row at
// seed/load/refresh. MCGP exemption is expressed via the wildcard row's
// mcgpExempt flag, NOT a per-token MCGP row, so this does not break MCGP.
func productFeeWildcardOnly(feature, token string) bool {
	switch feature {
	case models.FeatureProduct, models.FeatureProductMerchant:
		return strings.ToUpper(strings.TrimSpace(token)) != models.FeeTokenAny
	default:
		return false
	}
}

// errProductPerTokenOverride is returned by the write/load guards when a
// per-token product fee override is encountered (design §5, finding #5b).
func errProductPerTokenOverride(feature, token string) error {
	return fmt.Errorf(
		"fee_config: per-token override (%s) for %q is forbidden — the product fee must be wildcard-only (%q) so the token-agnostic cart quote provably equals the on-chain charge (design §5/#5b); express MCGP exemption via the wildcard row's mcgpExempt flag instead",
		token, feature, models.FeeTokenAny)
}

// RefreshFromConfirmedRate writes a CONFIRMED on-chain rate into the fee_config
// read replica and refreshes the in-memory cache + last-known-good for that key
// (configurable-FeeManager design §4/§5).
//
// This is the ONLY sanctioned writer of the replica from chain truth: task #43's
// /api/admin/fees/confirm endpoint calls it after it has verified a RateSet
// receipt (status==1) AND read back rateFor to confirm the on-chain value
// equals the intent. It is NOT wired to a RateSet event listener here — that
// subscription belongs to #43; this method only provides the write+invalidate
// primitive.
//
// Note the CQRS rule (design §2/§4): the replica is written from confirmed CHAIN
// truth only, never the moment an admin clicks save. The fee-config admin
// handler's "invalidate on save" was the quote-ahead-of-chain bug; the replica
// must lag chain, never lead it.
//
// chainID nil writes/refreshes the global row.
func (s *FeeConfigService) RefreshFromConfirmedRate(feature, token string, chainID *int64, bps uint16, flatUSD float64, mcgpExempt, active bool) error {
	if s.db == nil {
		return nil
	}
	if err := s.WriteReplicaRowTx(s.db, feature, token, chainID, bps, flatUSD, mcgpExempt, active); err != nil {
		return err
	}

	// Reload the whole cache from the now-updated replica (also refreshes
	// last-known-good on success). Replica written from confirmed chain truth →
	// cache invalidated. This is the correct invalidation point (post-confirm),
	// NOT on admin save.
	return s.Load()
}

// WriteReplicaRowTx upserts ONE confirmed (feature, token, chain) replica row
// using the supplied *gorm.DB — pass an open transaction handle to fold the
// replica write into a LARGER transaction (so it commits/rolls back atomically
// with the intent state advance — finding #3). It does NOT reload the in-memory
// cache: the caller MUST call Load() AFTER the outer transaction commits, so a
// rollback leaves the cache untouched (the cache would otherwise hold a rate the
// DB rolled back). RefreshFromConfirmedRate is the convenience wrapper that runs
// this against s.db and then reloads.
func (s *FeeConfigService) WriteReplicaRowTx(tx *gorm.DB, feature, token string, chainID *int64, bps uint16, flatUSD float64, mcgpExempt, active bool) error {
	if tx == nil {
		return fmt.Errorf("WriteReplicaRowTx: nil tx")
	}
	upperToken := strings.ToUpper(strings.TrimSpace(token))

	// #5b guard: a product fee must be wildcard-only so the token-agnostic cart
	// quote can never diverge from the per-token charge.
	if productFeeWildcardOnly(feature, upperToken) {
		return errProductPerTokenOverride(feature, upperToken)
	}

	// Upsert the (feature, token, chain) replica row.
	var existing models.FeeConfig
	q := tx.Where("feature = ? AND token_symbol = ?", feature, upperToken)
	if chainID == nil {
		q = q.Where("chain_id IS NULL")
	} else {
		q = q.Where("chain_id = ?", *chainID)
	}
	err := q.First(&existing).Error
	switch {
	case err == nil:
		updates := map[string]interface{}{
			"bps":             int(bps),
			"flat_amount_usd": flatUSD,
			"mcgp_exempt":     mcgpExempt,
			"active":          active,
		}
		if err := tx.Model(&models.FeeConfig{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("refresh replica update: %w", err)
		}
	case err == gorm.ErrRecordNotFound:
		row := models.FeeConfig{
			ID:            uuid.New(),
			Feature:       feature,
			TokenSymbol:   upperToken,
			ChainID:       chainID,
			BPS:           int(bps),
			FlatAmountUSD: flatUSD,
			MCGPExempt:    mcgpExempt,
			Active:        active,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("refresh replica create: %w", err)
		}
	default:
		return fmt.Errorf("refresh replica lookup: %w", err)
	}
	return nil
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

// FeeRate returns (bps, exempt) for a feature/token/chain, consulting the
// singleton (which runs the §11 fallback ladder) when available and the
// built-in seed defaults otherwise. chainID is nil for documented-global
// (backend-charged) call sites; per-chain call sites thread their chain.
func FeeRate(feature, tokenSymbol string, chainID *int64) (bps int, exempt bool) {
	if Fees != nil {
		b, _, ex, found := Fees.RateFor(feature, tokenSymbol, chainID)
		if found {
			return b, ex
		}
		// found=false means an unknown feature even after the ladder — fall
		// through to the seed (no-op for known features; harmless otherwise).
	}
	b, _, ex, _ := defaultRateFor(feature, tokenSymbol)
	return clampBPS(b), ex
}

// FeeFlatUSD returns (flatUSD, exempt) for a feature/token/chain.
func FeeFlatUSD(feature, tokenSymbol string, chainID *int64) (flatUSD float64, exempt bool) {
	if Fees != nil {
		_, flat, ex, found := Fees.RateFor(feature, tokenSymbol, chainID)
		if found {
			return flat, ex
		}
	}
	_, flat, ex, _ := defaultRateFor(feature, tokenSymbol)
	return flat, ex
}
