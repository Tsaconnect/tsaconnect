package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrUnsupportedCurrency is returned by GetRate when the requested code is not
// in the rate map. Handlers should map this to 400 BadRequest; *other* errors
// from GetRate (upstream outage, OER unconfigured, budget exceeded) are
// 500-class and must not be conflated.
var ErrUnsupportedCurrency = errors.New("unsupported currency")

// RateSource identifies which upstream produced a rate.
type RateSource string

const (
	SourceBybit RateSource = "bybit"
	SourceOER   RateSource = "oer"
)

// P2PRate holds the exchange rate plus display metadata for a single fiat currency.
//
// The BinanceBuy / BinanceSell field names are legacy from a previous Binance
// scraper; the JSON tags are part of the public API contract and must not be
// renamed without a coordinated frontend release.
type P2PRate struct {
	Currency    string     `json:"currency"`
	Symbol      string     `json:"symbol"`
	Name        string     `json:"name"`
	Flag        string     `json:"flag"`
	BinanceBuy  float64    `json:"binanceBuy"`  // rate when user buys USDT (NGN→USDT)
	BinanceSell float64    `json:"binanceSell"` // rate when user sells USDT (USDT→NGN)
	MidRate     float64    `json:"midRate"`     // average of buy/sell, used for display
	Source      RateSource `json:"source"`
	// Stale is true when this row was served from a fallback path (Bybit failure
	// → OER mid-market, or expired cache served because upstream is down).
	// Frontends MUST refuse to quote money against Stale=true rows.
	Stale     bool  `json:"stale"`
	UpdatedAt int64 `json:"updatedAt"`
}

// P2PRatesResponse is the API response shape.
type P2PRatesResponse struct {
	Base      string             `json:"base"` // "USD"
	Rates     map[string]P2PRate `json:"rates"`
	UpdatedAt int64              `json:"updatedAt"`
	// StaleSeconds is the age of this snapshot in seconds when it was served
	// from a fallback path (expired-cache rescue). Absent (0) on fresh data.
	StaleSeconds int64 `json:"staleSeconds,omitempty"`
}

// RatesSnapshot is the internal handle the service returns to callers — it
// pairs the rate map with metadata about how it was sourced. Money-handling
// callers should consult Snapshot.IsStale (or per-row Stale) before quoting.
type RatesSnapshot struct {
	Rates        map[string]P2PRate
	CachedAt     time.Time
	StaleSeconds int64 // > 0 only when this snapshot was served from a fallback
}

// IsStale reports whether the snapshot was served from any fallback path.
// Per-row Stale flags carry the same information at currency granularity.
func (s RatesSnapshot) IsStale() bool { return s.StaleSeconds > 0 }

// Cache TTLs are public so handlers/tests can override.
const (
	defaultCacheTTL    = 1 * time.Hour       // healthy response
	degradedCacheTTL   = 60 * time.Second    // any Bybit failure → retry quickly
	maxStaleServeAge   = 6 * time.Hour       // refuse to serve cache older than this
	bybitRequestTimeout = 15 * time.Second
)

// P2PService fetches exchange rates from Bybit P2P and Open Exchange Rates.
//
// Both upstreams share a single in-memory cache entry keyed "all". Healthy
// responses are cached for defaultCacheTTL; degraded responses (any Bybit
// fallback to OER) for degradedCacheTTL so we retry Bybit promptly.
type P2PService struct {
	HTTPClient          *http.Client
	cache               sync.Map
	cacheTTL            time.Duration
	degradedTTL         time.Duration
	maxStaleAge         time.Duration
	AppID               string
	SupportedCurrencies []string
	bybitCurrencies     map[string]bool

	sf singleflight.Group

	// OER monthly request budget (best-effort, in-memory).
	mu             sync.Mutex
	reqCount       int
	reqCountResets time.Time
}

// NewP2PService creates a new P2PService.
//
//	appID               — Open Exchange Rates app_id
//	supportedCurrencies — list of currency codes to include, or "*" for all known fiat
//	bybitCurrencies     — currencies to fetch from Bybit P2P instead of OER (e.g. ["NGN"])
//
// Codes are uppercased and trimmed. Bybit currency codes that are not present
// in the metadata table are dropped with a warning at construction time —
// otherwise a config typo would silently produce garbage display rows.
func NewP2PService(appID string, supportedCurrencies, bybitCurrencies []string) *P2PService {
	supported := make([]string, 0, len(supportedCurrencies))
	for _, c := range supportedCurrencies {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" {
			supported = append(supported, c)
		}
	}

	bybitSet := make(map[string]bool, len(bybitCurrencies))
	for _, c := range bybitCurrencies {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if !isKnownCurrency(c) {
			log.Printf("[P2PService] WARNING: BYBIT_P2P_CURRENCIES contains unknown code %q; ignoring", c)
			continue
		}
		bybitSet[c] = true
	}

	return &P2PService{
		HTTPClient:          &http.Client{Timeout: bybitRequestTimeout},
		cacheTTL:            defaultCacheTTL,
		degradedTTL:         degradedCacheTTL,
		maxStaleAge:         maxStaleServeAge,
		AppID:               appID,
		SupportedCurrencies: supported,
		bybitCurrencies:     bybitSet,
		reqCountResets:      time.Now(),
	}
}

// GetAllRates returns the rate map for all supported currencies.
// This is a convenience wrapper over GetSnapshot for callers that don't care
// about staleness; consider using GetSnapshot directly for money-handling code.
func (ps *P2PService) GetAllRates() (map[string]P2PRate, error) {
	snap, err := ps.GetSnapshot()
	if err != nil {
		return nil, err
	}
	return snap.Rates, nil
}

// GetSnapshot returns the rate map plus metadata describing how the data was
// sourced. When StaleSeconds > 0 the caller must refuse to quote money or
// surface the staleness to the user.
//
// Concurrent callers share a single upstream fetch via singleflight so a
// thundering herd on cache miss can't blow the OER monthly budget.
func (ps *P2PService) GetSnapshot() (*RatesSnapshot, error) {
	if entry := ps.loadFreshCache(); entry != nil {
		return entry.toSnapshot(time.Now(), false), nil
	}

	v, err, _ := ps.sf.Do("all", func() (any, error) {
		// Re-check inside the singleflight group — another goroutine may have
		// populated the cache while we were waiting.
		if entry := ps.loadFreshCache(); entry != nil {
			return entry.toSnapshot(time.Now(), false), nil
		}
		return ps.fetchAndCache()
	})
	if err != nil {
		return nil, err
	}
	return v.(*RatesSnapshot), nil
}

func (ps *P2PService) loadFreshCache() *p2pCacheEntry {
	entry, ok := ps.cache.Load("all")
	if !ok {
		return nil
	}
	ce := entry.(*p2pCacheEntry)
	if time.Now().Before(ce.expiresAt) {
		return ce
	}
	return nil
}

// fetchAndCache fetches a fresh rate map. On OER failure it serves the most
// recent cached rates (capped at maxStaleAge) marked Stale=true and writes
// them back into the cache with degradedTTL so subsequent calls within that
// window short-circuit on loadFreshCache (avoids hammering OER during outages).
// On Bybit failure it keeps the OER row but marks that currency Stale=true.
func (ps *P2PService) fetchAndCache() (*RatesSnapshot, error) {
	// Capture the prior entry *before* attempting the fetch so we can still
	// serve it on OER failure even when it has technically expired.
	var priorEntry *p2pCacheEntry
	if e, ok := ps.cache.Load("all"); ok {
		priorEntry = e.(*p2pCacheEntry)
	}

	rates, err := ps.fetchOERates()
	if err != nil {
		if priorEntry != nil {
			age := time.Since(priorEntry.cachedAt)
			if age <= ps.maxStaleAge {
				log.Printf("[P2PService] OER fetch failed (%v); serving %s-old cache marked Stale", err, age.Truncate(time.Second))
				staleRates := markAllStale(priorEntry.rates)
				now := time.Now()
				// Cache the stale snapshot so subsequent calls within degradedTTL
				// don't each retry OER. cachedAt preserves original freshness so
				// the maxStaleAge ceiling continues to track real data age.
				ps.cache.Store("all", &p2pCacheEntry{
					rates:     staleRates,
					cachedAt:  priorEntry.cachedAt,
					expiresAt: now.Add(ps.degradedTTL),
				})
				ageSec := int64(age.Seconds())
				if ageSec < 1 {
					ageSec = 1 // staleness signal must always be > 0 when serving from fallback
				}
				return &RatesSnapshot{
					Rates:        staleRates,
					CachedAt:     priorEntry.cachedAt,
					StaleSeconds: ageSec,
				}, nil
			}
			log.Printf("[P2PService] OER fetch failed (%v) and cache is %s old (> %s max); refusing to serve", err, age, ps.maxStaleAge)
		}
		return nil, fmt.Errorf("open exchange rates: %w", err)
	}

	// Override Bybit-routed currencies with live P2P data; on Bybit failure,
	// keep the OER value but mark it Stale so callers can refuse to quote.
	degraded := false
	for cur := range ps.bybitCurrencies {
		bybitRate, ferr := ps.fetchBybitRate(cur)
		if ferr != nil {
			log.Printf("[P2PService] Bybit fetch for %s failed (%v); falling back to OER mid-market and marking stale", cur, ferr)
			if oerRow, ok := rates[cur]; ok {
				oerRow.Stale = true
				rates[cur] = oerRow
			}
			degraded = true
			continue
		}
		rates[cur] = bybitRate
	}

	ttl := ps.cacheTTL
	if degraded {
		ttl = ps.degradedTTL
	}
	now := time.Now()
	ps.cache.Store("all", &p2pCacheEntry{
		rates:     rates,
		cachedAt:  now,
		expiresAt: now.Add(ttl),
	})

	return &RatesSnapshot{
		Rates:        rates,
		CachedAt:     now,
		StaleSeconds: 0,
	}, nil
}

// markAllStale returns a shallow copy of the rate map with every row marked Stale=true.
// We copy rather than mutate so the cached entry remains untouched.
//
// UpdatedAt is left as the original fetch time (not bumped). The Stale flag is
// the source of truth for freshness; UpdatedAt indicates when the data was
// originally fetched and is useful for debugging the staleness window.
func markAllStale(in map[string]P2PRate) map[string]P2PRate {
	out := make(map[string]P2PRate, len(in))
	for k, v := range in {
		v.Stale = true
		out[k] = v
	}
	return out
}

// GetRate returns the rate for a single currency.
//
// Returns ErrUnsupportedCurrency when the code isn't in the rate map (callers
// should map this to 400). Any other error indicates an upstream / internal
// problem and should be treated as 500.
func (ps *P2PService) GetRate(currency string) (*P2PRate, error) {
	allRates, err := ps.GetAllRates()
	if err != nil {
		return nil, err
	}
	rate, ok := allRates[strings.ToUpper(currency)]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCurrency, currency)
	}
	return &rate, nil
}

// ConvertUSD converts a USD amount to the target currency using the mid rate.
// Callers performing money-bearing operations should check the returned
// P2PRate's Stale flag (via GetRate) before using this result.
func (ps *P2PService) ConvertUSD(usdAmount float64, toCurrency string) (float64, error) {
	rate, err := ps.GetRate(toCurrency)
	if err != nil {
		return 0, err
	}
	return usdAmount * rate.MidRate, nil
}

// ──────────────────────────────────────────────
// Open Exchange Rates API
// ──────────────────────────────────────────────

type oerResponse struct {
	Disclaimer string             `json:"disclaimer"`
	License    string             `json:"license"`
	Timestamp  int64              `json:"timestamp"`
	Base       string             `json:"base"`
	Rates      map[string]float64 `json:"rates"`
}

func (ps *P2PService) fetchOERates() (map[string]P2PRate, error) {
	if ps.AppID == "" {
		return nil, fmt.Errorf("Open Exchange Rates API key not configured")
	}
	if !ps.canMakeRequest() {
		return nil, fmt.Errorf("monthly OER budget exceeded")
	}

	url := fmt.Sprintf("https://openexchangerates.org/api/latest.json?app_id=%s", ps.AppID)
	resp, err := ps.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	ps.trackRequest()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result oerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Rates) == 0 {
		return nil, fmt.Errorf("empty rates from API")
	}

	now := time.Now().Unix()
	rates := make(map[string]P2PRate, len(result.Rates)+1)

	usdMeta := currencyMeta("USD")
	rates["USD"] = P2PRate{
		Currency:    "USD",
		Symbol:      usdMeta.Symbol,
		Name:        usdMeta.Name,
		Flag:        usdMeta.Flag,
		BinanceBuy:  1.0,
		BinanceSell: 1.0,
		MidRate:     1.0,
		Source:      SourceOER,
		UpdatedAt:   now,
	}

	wantAll := ps.includesAllCurrencies()
	for currency, rate := range result.Rates {
		if currency == "USD" || rate <= 0 {
			continue
		}
		// Drop codes we don't have FE-renderable metadata for (precious metals XAU/XAG, etc).
		if !isKnownCurrency(currency) {
			continue
		}
		if !wantAll && !ps.isExplicitlySupported(currency) {
			continue
		}
		rate = roundRate(rate)
		meta := currencyMeta(currency)
		rates[currency] = P2PRate{
			Currency:    currency,
			Symbol:      meta.Symbol,
			Name:        meta.Name,
			Flag:        meta.Flag,
			BinanceBuy:  rate,
			BinanceSell: rate,
			MidRate:     rate,
			Source:      SourceOER,
			UpdatedAt:   now,
		}
	}

	return rates, nil
}

// includesAllCurrencies reports whether the configured supported list is the
// wildcard "*", meaning "return every fiat currency we have metadata for".
func (ps *P2PService) includesAllCurrencies() bool {
	for _, c := range ps.SupportedCurrencies {
		if c == "*" {
			return true
		}
	}
	return false
}

// isExplicitlySupported reports whether the given (uppercase) code appears in
// the configured supported list.
func (ps *P2PService) isExplicitlySupported(code string) bool {
	for _, c := range ps.SupportedCurrencies {
		if c == code {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────
// Bybit P2P API
// ──────────────────────────────────────────────

const (
	bybitP2PURL    = "https://api2.bybit.com/fiat/otc/item/online"
	bybitUserAgent = "tsaconnect/1.0 (+https://tsaconnectworld.com)"
)

type bybitItem struct {
	Price             string `json:"price"`
	PriceType         int    `json:"priceType"` // 0 = fixed, 1 = floating
	FinishNum         int    `json:"finishNum"`
	RecentExecuteRate int    `json:"recentExecuteRate"`
}

type bybitResponse struct {
	RetCode int    `json:"ret_code"`
	RetMsg  string `json:"ret_msg"`
	Result  struct {
		Count int         `json:"count"`
		Items []bybitItem `json:"items"`
	} `json:"result"`
}

// fetchBybitRate aggregates top-of-book Bybit P2P USDT ads for the given currency.
// Buy and sell sides are fetched separately so the spread reflects real market depth.
//
// Note: under healthy P2P conditions BinanceBuy ≥ BinanceSell because asks
// (sell ads) sit above bids (buy ads). Thin/volatile markets can produce a
// crossed book; we accept that as-is — a slightly inverted live rate is a
// better quote than a stale or OER mid-market substitute.
func (ps *P2PService) fetchBybitRate(currency string) (P2PRate, error) {
	// side=1 (SELL ads, traders selling USDT): use to derive the rate when our user *buys* USDT
	// side=0 (BUY ads, traders buying USDT):   use to derive the rate when our user *sells* USDT
	buyPrice, err := ps.fetchBybitSide(currency, "1")
	if err != nil {
		return P2PRate{}, fmt.Errorf("bybit %s buy side: %w", currency, err)
	}
	sellPrice, err := ps.fetchBybitSide(currency, "0")
	if err != nil {
		return P2PRate{}, fmt.Errorf("bybit %s sell side: %w", currency, err)
	}

	mid := roundRate((buyPrice + sellPrice) / 2)
	meta := currencyMeta(currency)

	return P2PRate{
		Currency:    currency,
		Symbol:      meta.Symbol,
		Name:        meta.Name,
		Flag:        meta.Flag,
		BinanceBuy:  buyPrice,
		BinanceSell: sellPrice,
		MidRate:     mid,
		Source:      SourceBybit,
		UpdatedAt:   time.Now().Unix(),
	}, nil
}

// fetchBybitSide returns the median qualifying-ad price for one side of the
// book. Filters: priceType==0 (fixed price), recentExecuteRate≥90, finishNum≥50
// to skip new/unreliable traders. Median of top 5 absorbs single-ad outliers.
func (ps *P2PService) fetchBybitSide(currency, side string) (float64, error) {
	body, err := json.Marshal(map[string]any{
		"userId":     "",
		"tokenId":    "USDT",
		"currencyId": currency,
		"payment":    []string{},
		"side":       side,
		"size":       "20",
		"page":       "1",
	})
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, bybitP2PURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", bybitUserAgent)

	resp, err := ps.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	rawBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}
	// Bybit occasionally embeds raw control chars in user-supplied remarks/nicknames,
	// which Go's strict JSON decoder rejects. Strip them before decoding.
	sanitized := stripControlChars(rawBytes)
	var raw bybitResponse
	if err := json.Unmarshal(sanitized, &raw); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	if raw.RetCode != 0 {
		return 0, fmt.Errorf("bybit retcode %d: %s", raw.RetCode, raw.RetMsg)
	}

	prices := filterBybitItems(raw.Result.Items)
	if len(prices) == 0 {
		return 0, fmt.Errorf("no qualifying ads (returned=%d)", len(raw.Result.Items))
	}

	// Bybit returns ads best-price-first; filterBybitItems preserves that order.
	// Take the top 5 *qualifying* ads, then median.
	top := prices
	if len(top) > 5 {
		top = top[:5]
	}
	return median(top), nil
}

// stripControlChars removes ASCII control bytes (0x00–0x1F) except \t, \n, \r.
//
// Operates byte-wise, but ASCII control bytes can never appear inside a valid
// UTF-8 multi-byte sequence (continuation bytes are ≥ 0x80), so multi-byte
// glyphs in user remarks are preserved intact.
//
// The function is allocation-free when no control chars are present (returns
// the original slice). When stripping is needed, a fresh buffer is allocated
// so the caller's bytes are never mutated.
func stripControlChars(b []byte) []byte {
	// Fast path: scan first to see if anything needs stripping.
	stripIdx := -1
	for i, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			stripIdx = i
			break
		}
	}
	if stripIdx == -1 {
		return b
	}
	out := make([]byte, stripIdx, len(b))
	copy(out, b[:stripIdx])
	stripped := 0
	for _, c := range b[stripIdx:] {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			stripped++
			continue
		}
		out = append(out, c)
	}
	if stripped > 0 {
		log.Printf("[P2PService] stripped %d control char(s) from upstream JSON body", stripped)
	}
	return out
}

// filterBybitItems keeps only fixed-price ads from established traders and
// returns their prices in Bybit's original (best-price-first) order.
func filterBybitItems(items []bybitItem) []float64 {
	out := make([]float64, 0, len(items))
	for _, it := range items {
		if it.PriceType != 0 {
			continue
		}
		if it.RecentExecuteRate < 90 {
			continue
		}
		if it.FinishNum < 50 {
			continue
		}
		p, ok := parsePrice(it.Price)
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out
}

// median returns the median of a non-empty slice. Slice is copied to avoid
// mutating caller data. Panics if values is empty (callers must check len).
func median(values []float64) float64 {
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return roundRate(v[n/2])
	}
	return roundRate((v[n/2-1] + v[n/2]) / 2)
}

// ──────────────────────────────────────────────
// Request budget tracking (OER only)
// ──────────────────────────────────────────────

func (ps *P2PService) canMakeRequest() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	now := time.Now()
	if now.Year() != ps.reqCountResets.Year() || now.Month() != ps.reqCountResets.Month() {
		ps.reqCount = 0
		ps.reqCountResets = now
	}
	return ps.reqCount < 950
}

func (ps *P2PService) trackRequest() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.reqCount++
	log.Printf("[P2PService] OER request %d this month", ps.reqCount)
}

// ──────────────────────────────────────────────
// Cache
// ──────────────────────────────────────────────

type p2pCacheEntry struct {
	rates     map[string]P2PRate
	cachedAt  time.Time
	expiresAt time.Time
}

// toSnapshot converts the cache entry into a RatesSnapshot, computing
// StaleSeconds when forceStale is true OR any individual row is marked Stale
// (e.g. because we wrote back a stale-fallback map and a later caller hits it).
func (ce *p2pCacheEntry) toSnapshot(now time.Time, forceStale bool) *RatesSnapshot {
	stale := forceStale
	if !stale {
		for _, r := range ce.rates {
			if r.Stale {
				stale = true
				break
			}
		}
	}
	snap := &RatesSnapshot{Rates: ce.rates, CachedAt: ce.cachedAt}
	if stale {
		ageSec := int64(now.Sub(ce.cachedAt).Seconds())
		if ageSec < 1 {
			ageSec = 1
		}
		snap.StaleSeconds = ageSec
	}
	return snap
}

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

// roundRate rounds a fiat rate to a sensible precision: 4 decimals for rates
// below 10 (KWD, BHD, OMR, GBP, EUR…) where 2 decimals would lose ~1.5%, and
// 2 decimals for everything else (NGN, JPY, IDR, …).
func roundRate(rate float64) float64 {
	if rate < 10 {
		return math.Round(rate*10000) / 10000
	}
	return math.Round(rate*100) / 100
}

// parsePrice converts a Bybit price string to float64. Returns (price, true)
// on success; on any rejection (parse error, non-finite, zero, negative) logs
// the bad input and returns (0, false) so the caller drops the ad rather than
// silently treating it as zero — which would otherwise poison medians on
// schema drift or upstream bugs.
func parsePrice(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		log.Printf("[P2PService] price parse failed for %q: %v", s, err)
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		log.Printf("[P2PService] price rejected (non-finite): %q -> %v", s, v)
		return 0, false
	}
	if v <= 0 {
		log.Printf("[P2PService] price rejected (non-positive): %q -> %v", s, v)
		return 0, false
	}
	return v, true
}
