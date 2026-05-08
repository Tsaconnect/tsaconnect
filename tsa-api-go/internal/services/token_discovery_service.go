// Package services — token discovery via third-party indexers.
//
// Why an indexer instead of RPC scans?
//
// "All ERC-20 tokens an address holds" can't be answered by an RPC node
// alone — the node knows balances per (token, address) pair, not which
// tokens are interesting to a given address. Scanning Transfer logs to
// build that index is too expensive to do per-request, and L2s with cheap
// gas (Polygon, Arbitrum, Base) often hit airdrop / spam tokens that we'd
// rather filter out. Indexers maintain that (address → tokens-held) index
// for us with built-in spam filtering.
//
// We use Ankr Advanced API as the primary because (1) `ankr_getAccountBalance`
// is multichain in a single RPC call (pass an array of blockchain slugs and
// get every chain's holdings back at once), (2) USD prices are returned
// inline so we skip a follow-up price round-trip, and (3) `onlyWhitelisted`
// filters spam at the source. Coverage spans every chain in our registry
// except Sonic. Moralis fills that gap.
//
// If neither key is set the service is a no-op: every chain returns an
// empty list and the caller surfaces "auto-discovery unavailable" rather
// than failing the request.
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DiscoveredToken represents a single ERC-20 holding surfaced by an indexer.
// Balance is the raw on-chain value (smallest unit). Decimals lets the
// caller format it; UsdValue is computed if a price feed knows the symbol,
// otherwise zero.
type DiscoveredToken struct {
	Chain           string  `json:"chain"`
	ChainID         int64   `json:"chainId"`
	ContractAddress string  `json:"contractAddress"`
	Symbol          string  `json:"symbol"`
	Name            string  `json:"name"`
	Decimals        int     `json:"decimals"`
	Balance         string  `json:"balance"`
	UsdValue        float64 `json:"usdValue"`
	Source          string  `json:"source"` // "ankr" | "moralis"
}

// TokenDiscoveryService aggregates ERC-20 holdings across chains using
// third-party indexers. Configure with NewTokenDiscoveryService.
//
// Discovery hits paid third-party APIs (Ankr, Moralis), so the service
// caches results per (address, chains) for cacheTTL and uses singleflight
// to coalesce concurrent identical fetches. With a 5-minute TTL and a
// user opening the wallet 20×/day, this collapses ~20 upstream calls/user
// into ~3, comfortably inside both providers' free tiers.
type TokenDiscoveryService struct {
	httpClient   *http.Client
	ankrKey      string
	moralisKey   string
	priceService *PriceService

	// cache: keyed by cacheKey(address, chainKeys). Read-heavy, so the
	// RWMutex pays for itself; entries expire on read (no janitor — at
	// ~150 bytes/entry the leak from idle-then-abandoned addresses is
	// bounded enough to ignore until proven otherwise).
	cacheMu  sync.RWMutex
	cache    map[string]discoveryCacheEntry
	cacheTTL time.Duration

	// sf coalesces concurrent fetches for the same key. Two users
	// hitting the same shared/treasury address in the same instant fire
	// one upstream call between them.
	sf singleflight.Group
}

type discoveryCacheEntry struct {
	tokens    []DiscoveredToken
	expiresAt time.Time
}

// ankrChainSlug maps our internal chain key → Ankr blockchain identifier.
// See https://api-docs.ankr.com/reference/post_ankr-getaccountbalance —
// the `blockchain` param accepts an array of these slugs and the service
// returns assets across all requested chains in a single response.
var ankrChainSlug = map[string]string{
	"ethereum":  "eth",
	"polygon":   "polygon",
	"arbitrum":  "arbitrum",
	"base":      "base",
	"optimism":  "optimism",
	"linea":     "linea",
	"bsc":       "bsc",
	"avalanche": "avalanche",
}

// ankrSlugToChainKey is the reverse of ankrChainSlug — used when parsing
// Ankr's response to map a row's `"blockchain": "eth"` back to our
// internal "ethereum" key.
var ankrSlugToChainKey = map[string]string{
	"eth":       "ethereum",
	"polygon":   "polygon",
	"arbitrum":  "arbitrum",
	"base":      "base",
	"optimism":  "optimism",
	"linea":     "linea",
	"bsc":       "bsc",
	"avalanche": "avalanche",
}

// moralisChainSlug covers chains Ankr doesn't support. As of 2026 only
// Sonic falls in this bucket.
var moralisChainSlug = map[string]string{
	"sonic": "sonic",
}

// NewTokenDiscoveryService reads ANKR_API_KEY and MORALIS_API_KEY from
// the environment. Either, both, or neither may be set. The service is
// always usable; it just returns an empty list for chains it can't reach.
//
// Cache TTL defaults to 5 minutes; override via TOKEN_DISCOVERY_CACHE_TTL_SECONDS.
// Set to 0 to disable caching (useful in tests; not recommended in prod —
// you'll burn through provider quotas).
func NewTokenDiscoveryService(priceService *PriceService) *TokenDiscoveryService {
	ttl := 5 * time.Minute
	if v := os.Getenv("TOKEN_DISCOVERY_CACHE_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			ttl = time.Duration(n) * time.Second
		}
	}
	return &TokenDiscoveryService{
		httpClient: &http.Client{
			// 15s budget. Ankr's multichain call typically completes in
			// 1–3s even when fanning across 8 chains server-side; the
			// margin protects against tail latency.
			Timeout: 15 * time.Second,
		},
		ankrKey:      os.Getenv("ANKR_API_KEY"),
		moralisKey:   os.Getenv("MORALIS_API_KEY"),
		priceService: priceService,
		cache:        make(map[string]discoveryCacheEntry),
		cacheTTL:     ttl,
	}
}

// cacheKey builds a stable key from address (case-insensitive) and the
// chain set (sorted to make key order-independent). Two callers asking
// for ["polygon","base"] and ["base","polygon"] hit the same entry.
func (s *TokenDiscoveryService) cacheKey(address string, chainKeys []string) string {
	sorted := make([]string, len(chainKeys))
	copy(sorted, chainKeys)
	sort.Strings(sorted)
	return strings.ToLower(address) + "|" + strings.Join(sorted, ",")
}

// DiscoverTokens returns the merged list of non-zero ERC-20 holdings for
// `address` across `chainKeys` (pass nil/empty to query everything).
//
// Cached for cacheTTL; concurrent fetches for the same key are coalesced
// via singleflight. Cache lookups happen before any upstream I/O, so cache
// hits cost ~0 (just a map read).
//
// Errors from individual providers are logged but do not abort the call:
// the goal is "best-effort surfacing of held tokens", so a provider hiccup
// just means those chains return nothing in this response. The (possibly
// partial) result is still cached — see fetchUncached for the rationale.
func (s *TokenDiscoveryService) DiscoverTokens(ctx context.Context, address string, chainKeys []string) []DiscoveredToken {
	if address == "" {
		return nil
	}

	// If chainKeys is empty, query every chain we have coverage for.
	if len(chainKeys) == 0 {
		for k := range ankrChainSlug {
			chainKeys = append(chainKeys, k)
		}
		for k := range moralisChainSlug {
			chainKeys = append(chainKeys, k)
		}
	}

	// Cache disabled (TTL=0) → always fetch live.
	if s.cacheTTL <= 0 {
		return s.fetchUncached(ctx, address, chainKeys)
	}

	key := s.cacheKey(address, chainKeys)

	// Fast path: serve cached result without taking the singleflight slot.
	s.cacheMu.RLock()
	if entry, ok := s.cache[key]; ok && time.Now().Before(entry.expiresAt) {
		s.cacheMu.RUnlock()
		return entry.tokens
	}
	s.cacheMu.RUnlock()

	// Coalesce: only one goroutine per key fans out upstream; others wait.
	v, _, _ := s.sf.Do(key, func() (any, error) {
		// Re-check cache: another goroutine may have populated it
		// between our miss and entering the singleflight.
		s.cacheMu.RLock()
		if entry, ok := s.cache[key]; ok && time.Now().Before(entry.expiresAt) {
			s.cacheMu.RUnlock()
			return entry.tokens, nil
		}
		s.cacheMu.RUnlock()

		tokens := s.fetchUncached(ctx, address, chainKeys)
		s.cacheMu.Lock()
		s.cache[key] = discoveryCacheEntry{
			tokens:    tokens,
			expiresAt: time.Now().Add(s.cacheTTL),
		}
		s.cacheMu.Unlock()
		return tokens, nil
	})
	return v.([]DiscoveredToken)
}

// fetchUncached fans out to Ankr (one multichain call) and Moralis (per
// chain) and merges the results. Always cached upstream by DiscoverTokens.
//
// Empty results are cached the same as full results: an address that
// genuinely holds nothing on Ankr-covered chains is a legitimate answer,
// and a 5-minute window of stale-empty after a transient indexer blip is
// an acceptable trade for the cost saving.
func (s *TokenDiscoveryService) fetchUncached(ctx context.Context, address string, chainKeys []string) []DiscoveredToken {
	// Split requested chains by provider.
	var ankrChains, moralisChains []string
	for _, k := range chainKeys {
		if _, ok := ankrChainSlug[k]; ok && s.ankrKey != "" {
			ankrChains = append(ankrChains, k)
		} else if _, ok := moralisChainSlug[k]; ok && s.moralisKey != "" {
			moralisChains = append(moralisChains, k)
		}
	}

	var (
		mu      sync.Mutex
		results []DiscoveredToken
		wg      sync.WaitGroup
	)

	// Ankr: one call covering everything it supports.
	if len(ankrChains) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens, err := s.discoverViaAnkr(ctx, ankrChains, address)
			if err != nil {
				log.Printf("[discovery] ankr: %v", err)
				return
			}
			if len(tokens) == 0 {
				return
			}
			mu.Lock()
			results = append(results, tokens...)
			mu.Unlock()
		}()
	}

	// Moralis: per-chain fan-out (only Sonic today, but the loop keeps
	// the door open for future Ankr-uncovered chains).
	for _, chainKey := range moralisChains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens, err := s.discoverViaMoralis(ctx, chainKey, address)
			if err != nil {
				log.Printf("[discovery] moralis %s: %v", chainKey, err)
				return
			}
			if len(tokens) == 0 {
				return
			}
			mu.Lock()
			results = append(results, tokens...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	// Ankr returns USD prices inline; only Moralis rows (and any Ankr
	// row whose price came back as 0) need a fallback enrichment pass.
	s.enrichWithPrices(results)
	return results
}

// HasAnyProvider returns true iff at least one indexer key is configured.
// Callers use this to surface a clear "auto-discovery is offline" message
// instead of an ambiguous empty list.
func (s *TokenDiscoveryService) HasAnyProvider() bool {
	return s.ankrKey != "" || s.moralisKey != ""
}

// ── Ankr ────────────────────────────────────────────────────────────────

// ankrAsset is a single row in `ankr_getAccountBalance`'s `result.assets`.
// Both raw-integer and human-readable balance fields are returned; we use
// the raw field to keep precision and let the FE format it.
type ankrAsset struct {
	Blockchain        string `json:"blockchain"`
	TokenName         string `json:"tokenName"`
	TokenSymbol       string `json:"tokenSymbol"`
	TokenDecimals     int    `json:"tokenDecimals"`
	TokenType         string `json:"tokenType"` // "NATIVE" | "ERC20"
	ContractAddress   string `json:"contractAddress"`
	HolderAddress     string `json:"holderAddress"`
	Balance           string `json:"balance"`
	BalanceRawInteger string `json:"balanceRawInteger"`
	BalanceUsd        string `json:"balanceUsd"`
	TokenPrice        string `json:"tokenPrice"`
	Thumbnail         string `json:"thumbnail"`
}

type ankrAccountBalanceResponse struct {
	Result struct {
		TotalBalanceUsd string      `json:"totalBalanceUsd"`
		Assets          []ankrAsset `json:"assets"`
		NextPageToken   string      `json:"nextPageToken"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// discoverViaAnkr makes one ankr_getAccountBalance call covering every
// chain in chainKeys. Walks `nextPageToken` if Ankr indicates more
// results; capped at 5 pages (~500 assets) to bound work on whale wallets.
func (s *TokenDiscoveryService) discoverViaAnkr(ctx context.Context, chainKeys []string, address string) ([]DiscoveredToken, error) {
	endpoint := "https://rpc.ankr.com/multichain/" + s.ankrKey

	// Translate our chain keys to Ankr slugs.
	blockchains := make([]string, 0, len(chainKeys))
	for _, k := range chainKeys {
		if slug, ok := ankrChainSlug[k]; ok {
			blockchains = append(blockchains, slug)
		}
	}
	if len(blockchains) == 0 {
		return nil, nil
	}

	const maxPages = 5
	var (
		out       []DiscoveredToken
		pageToken string
	)

	for range maxPages {
		params := map[string]any{
			"blockchain":      blockchains,
			"walletAddress":   address,
			"onlyWhitelisted": true, // server-side spam filter
		}
		if pageToken != "" {
			params["pageToken"] = pageToken
		}
		reqBody, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "ankr_getAccountBalance",
			"params":  params,
		})
		if err != nil {
			return out, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return out, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return out, fmt.Errorf("ankr request failed: %w", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return out, fmt.Errorf("ankr http %d: %s", resp.StatusCode, string(raw))
		}

		var parsed ankrAccountBalanceResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return out, fmt.Errorf("ankr decode: %w", err)
		}
		if parsed.Error != nil {
			return out, fmt.Errorf("ankr rpc error %d: %s", parsed.Error.Code, parsed.Error.Message)
		}

		for _, a := range parsed.Result.Assets {
			// Skip native rows — the wallet screen renders the native
			// gas token from supported_tokens with canonical metadata,
			// so an Ankr "NATIVE" entry would duplicate that row.
			if a.TokenType != "ERC20" {
				continue
			}
			if a.TokenSymbol == "" || a.BalanceRawInteger == "" || a.BalanceRawInteger == "0" {
				continue
			}
			chainKey, ok := ankrSlugToChainKey[a.Blockchain]
			if !ok {
				continue
			}
			usd, _ := strconv.ParseFloat(a.BalanceUsd, 64)
			out = append(out, DiscoveredToken{
				Chain:           chainKey,
				ContractAddress: strings.ToLower(a.ContractAddress),
				Symbol:          a.TokenSymbol,
				Name:            a.TokenName,
				Decimals:        a.TokenDecimals,
				Balance:         a.BalanceRawInteger,
				UsdValue:        usd,
				Source:          "ankr",
			})
		}

		if parsed.Result.NextPageToken == "" {
			break
		}
		pageToken = parsed.Result.NextPageToken
	}

	return out, nil
}

// ── Moralis ─────────────────────────────────────────────────────────────

type moralisErc20Token struct {
	TokenAddress string `json:"token_address"`
	Symbol       string `json:"symbol"`
	Name         string `json:"name"`
	Decimals     int    `json:"decimals"`
	Balance      string `json:"balance"` // raw integer string
	PossibleSpam bool   `json:"possible_spam"`
	Verified     bool   `json:"verified_contract"`
}

func (s *TokenDiscoveryService) discoverViaMoralis(ctx context.Context, chainKey, address string) ([]DiscoveredToken, error) {
	slug := moralisChainSlug[chainKey]
	endpoint := fmt.Sprintf("https://deep-index.moralis.io/api/v2.2/%s/erc20?chain=%s", address, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", s.moralisKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moralis request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("moralis http %d: %s", resp.StatusCode, string(raw))
	}

	var rows []moralisErc20Token
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("moralis decode: %w", err)
	}

	out := make([]DiscoveredToken, 0, len(rows))
	for _, t := range rows {
		// Moralis flags spam separately; honor it. We also drop rows with
		// no symbol since they wouldn't render usefully in the wallet.
		if t.PossibleSpam || t.Symbol == "" || t.Balance == "" || t.Balance == "0" {
			continue
		}
		out = append(out, DiscoveredToken{
			Chain:           chainKey,
			ContractAddress: strings.ToLower(t.TokenAddress),
			Symbol:          t.Symbol,
			Name:            t.Name,
			Decimals:        t.Decimals,
			Balance:         t.Balance,
			Source:          "moralis",
		})
	}
	return out, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

// enrichWithPrices populates UsdValue for tokens whose UsdValue came back
// as 0 from the indexer (Moralis always; Ankr when the token isn't priced
// upstream). Tokens we still don't have a price for keep UsdValue=0; the
// FE renders that as "—" rather than "$0.00" so users don't think they're
// holding nothing of value.
func (s *TokenDiscoveryService) enrichWithPrices(tokens []DiscoveredToken) {
	if s.priceService == nil || len(tokens) == 0 {
		return
	}
	// Only collect symbols for rows the indexer didn't already price —
	// Ankr's prices are authoritative and we don't want to second-guess.
	symbolSet := make(map[string]bool)
	for _, t := range tokens {
		if t.UsdValue > 0 {
			continue
		}
		symbolSet[strings.ToUpper(t.Symbol)] = true
	}
	if len(symbolSet) == 0 {
		return
	}
	syms := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		syms = append(syms, s)
	}
	prices, err := s.priceService.GetPrices(syms)
	if err != nil {
		return
	}
	for i := range tokens {
		if tokens[i].UsdValue > 0 {
			continue
		}
		pd, ok := prices[strings.ToUpper(tokens[i].Symbol)]
		if !ok || pd.USD == 0 {
			continue
		}
		// Balance is in smallest units; convert to whole-units float for
		// the USD multiplication. Big-int math overflows on tokens with
		// large decimals + large balances, so use big.Float here.
		raw, ok := new(big.Float).SetString(tokens[i].Balance)
		if !ok {
			continue
		}
		divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tokens[i].Decimals)), nil))
		whole, _ := new(big.Float).Quo(raw, divisor).Float64()
		tokens[i].UsdValue = whole * pd.USD
	}
}
