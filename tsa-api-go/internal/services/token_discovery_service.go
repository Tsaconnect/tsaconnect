// Package services — token discovery via third-party indexers.
//
// Why an indexer instead of RPC scans?
//
// "All ERC-20 tokens an address holds" can't be answered by an RPC node
// alone — the node knows balances per (token, address) pair, not which
// tokens are interesting to a given address. Scanning Transfer logs to
// build that index is too expensive to do per-request, and L2s with cheap
// gas (Polygon, Arbitrum, Base) often hit airdrop / spam tokens that we'd
// rather filter out. Indexers (Alchemy, Moralis, Covalent) maintain that
// (address → tokens-held) index for us with built-in spam filtering.
//
// We use Alchemy as the primary because (1) the most generous free tier
// (300M Compute Units/month is a few orders of magnitude over our needs),
// (2) the cleanest "balances + metadata in one call" API
// (`alchemy_getTokensForOwner`), and (3) coverage of every L2 in our chain
// registry except Sonic, BSC, Avalanche. Moralis covers those gaps.
//
// If neither key is set the service is a no-op: every chain returns an
// empty list and the caller surfaces "auto-discovery unavailable" rather
// than failing the request.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
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
	Source          string  `json:"source"` // "alchemy" | "moralis"
}

// TokenDiscoveryService aggregates ERC-20 holdings across chains using
// third-party indexers. Configure with NewTokenDiscoveryService.
type TokenDiscoveryService struct {
	httpClient   *http.Client
	alchemyKey   string
	moralisKey   string
	priceService *PriceService
}

// alchemyChainSlug maps our internal chain key → Alchemy URL slug.
// See https://docs.alchemy.com/reference/network-string for the canonical
// list. Chains not in this map fall through to Moralis (or skip).
var alchemyChainSlug = map[string]string{
	"ethereum": "eth-mainnet",
	"polygon":  "polygon-mainnet",
	"arbitrum": "arb-mainnet",
	"base":     "base-mainnet",
	"optimism": "opt-mainnet",
	"linea":    "linea-mainnet",
}

// moralisChainSlug maps our internal chain key → Moralis chain string.
// Used as a fallback for chains Alchemy doesn't cover.
var moralisChainSlug = map[string]string{
	"bsc":       "bsc",
	"avalanche": "avalanche",
	"sonic":     "sonic", // Moralis added Sonic in 2025
}

// NewTokenDiscoveryService reads ALCHEMY_API_KEY and MORALIS_API_KEY from
// the environment. Either, both, or neither may be set. The service is
// always usable; it just returns an empty list for chains it can't reach.
func NewTokenDiscoveryService(priceService *PriceService) *TokenDiscoveryService {
	return &TokenDiscoveryService{
		httpClient: &http.Client{
			// 10s per-chain budget. Indexers are usually <1s; this just
			// keeps a single chain's flakiness from stalling the whole
			// discovery response.
			Timeout: 10 * time.Second,
		},
		alchemyKey:   os.Getenv("ALCHEMY_API_KEY"),
		moralisKey:   os.Getenv("MORALIS_API_KEY"),
		priceService: priceService,
	}
}

// DiscoverTokens queries every supported chain in parallel and returns a
// flattened list of non-zero ERC-20 holdings. `chainKeys` constrains which
// chains to query — pass nil/empty to query everything.
//
// Errors from individual chains are logged but do not abort the call: the
// goal is "best-effort surfacing of held tokens", so a single chain's
// indexer hiccup just means that chain returns nothing in the response.
func (s *TokenDiscoveryService) DiscoverTokens(ctx context.Context, address string, chainKeys []string) []DiscoveredToken {
	if address == "" {
		return nil
	}

	// If chainKeys is empty, query every chain we have coverage for.
	if len(chainKeys) == 0 {
		for k := range alchemyChainSlug {
			chainKeys = append(chainKeys, k)
		}
		for k := range moralisChainSlug {
			chainKeys = append(chainKeys, k)
		}
	}

	var (
		mu      sync.Mutex
		results []DiscoveredToken
		wg      sync.WaitGroup
	)

	for _, chainKey := range chainKeys {
		chainKey := chainKey // capture for goroutine

		// Pick provider. Alchemy first; Moralis if Alchemy doesn't cover
		// this chain. If neither knows it, skip.
		var fetch func(ctx context.Context, address string) ([]DiscoveredToken, error)
		if _, ok := alchemyChainSlug[chainKey]; ok && s.alchemyKey != "" {
			fetch = func(ctx context.Context, addr string) ([]DiscoveredToken, error) {
				return s.discoverViaAlchemy(ctx, chainKey, addr)
			}
		} else if _, ok := moralisChainSlug[chainKey]; ok && s.moralisKey != "" {
			fetch = func(ctx context.Context, addr string) ([]DiscoveredToken, error) {
				return s.discoverViaMoralis(ctx, chainKey, addr)
			}
		} else {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens, err := fetch(ctx, address)
			if err != nil {
				log.Printf("[discovery] %s: %v", chainKey, err)
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
	s.enrichWithPrices(results)
	return results
}

// HasAnyProvider returns true iff at least one indexer key is configured.
// Callers use this to surface a clear "auto-discovery is offline" message
// instead of an ambiguous empty list.
func (s *TokenDiscoveryService) HasAnyProvider() bool {
	return s.alchemyKey != "" || s.moralisKey != ""
}

// ── Alchemy ─────────────────────────────────────────────────────────────

// alchemyGetTokensForOwnerResponse is the shape of `alchemy_getTokensForOwner`.
// It returns balances and metadata in a single call, which beats the older
// getTokenBalances→getTokenMetadata two-step both for latency and CU cost.
type alchemyGetTokensForOwnerResponse struct {
	Result struct {
		Owner   string `json:"owner"`
		Tokens  []struct {
			ContractAddress string  `json:"contractAddress"`
			RawBalance      string  `json:"rawBalance"`
			Decimals        *int    `json:"decimals"`
			Name            *string `json:"name"`
			Symbol          *string `json:"symbol"`
			Logo            *string `json:"logo"`
			Error           *string `json:"error"`
		} `json:"tokens"`
		PageKey *string `json:"pageKey"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *TokenDiscoveryService) discoverViaAlchemy(ctx context.Context, chainKey, address string) ([]DiscoveredToken, error) {
	slug := alchemyChainSlug[chainKey]
	endpoint := fmt.Sprintf("https://%s.g.alchemy.com/v2/%s", slug, s.alchemyKey)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"alchemy_getTokensForOwner","params":[{"owner":"` + address + `","withMetadata":true}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alchemy request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("alchemy http %d: %s", resp.StatusCode, string(raw))
	}

	var parsed alchemyGetTokensForOwnerResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("alchemy decode: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("alchemy rpc error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}

	out := make([]DiscoveredToken, 0, len(parsed.Result.Tokens))
	for _, t := range parsed.Result.Tokens {
		// Skip rows where the indexer flagged a metadata read error or
		// where the symbol is empty — these are usually exotic / broken
		// tokens that we don't want to surface.
		if t.Error != nil && *t.Error != "" {
			continue
		}
		if t.Symbol == nil || *t.Symbol == "" {
			continue
		}
		// Skip dust / zero balances. RawBalance comes back as a 0x-prefixed
		// hex string; isNonZeroHex handles "0x0", "0x", and missing values.
		if !isNonZeroHex(t.RawBalance) {
			continue
		}
		decimals := 18
		if t.Decimals != nil {
			decimals = *t.Decimals
		}
		balance := hexToDecimalString(t.RawBalance)
		name := ""
		if t.Name != nil {
			name = *t.Name
		}
		out = append(out, DiscoveredToken{
			Chain:           chainKey,
			ContractAddress: strings.ToLower(t.ContractAddress),
			Symbol:          *t.Symbol,
			Name:            name,
			Decimals:        decimals,
			Balance:         balance,
			Source:          "alchemy",
		})
	}
	return out, nil
}

// ── Moralis ─────────────────────────────────────────────────────────────

type moralisErc20Token struct {
	TokenAddress string `json:"token_address"`
	Symbol       string `json:"symbol"`
	Name         string `json:"name"`
	Decimals     int    `json:"decimals"`
	Balance      string `json:"balance"`        // raw integer string
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

func isNonZeroHex(h string) bool {
	if h == "" || h == "0x" || h == "0x0" {
		return false
	}
	v, ok := new(big.Int).SetString(strings.TrimPrefix(h, "0x"), 16)
	return ok && v.Sign() > 0
}

func hexToDecimalString(h string) string {
	v, ok := new(big.Int).SetString(strings.TrimPrefix(h, "0x"), 16)
	if !ok {
		return "0"
	}
	return v.String()
}

// enrichWithPrices populates UsdValue for tokens whose symbol the price
// service recognizes. Tokens we don't have a price for keep UsdValue=0;
// the FE renders that as "—" rather than "$0.00" so users don't think
// they're holding nothing of value.
func (s *TokenDiscoveryService) enrichWithPrices(tokens []DiscoveredToken) {
	if s.priceService == nil || len(tokens) == 0 {
		return
	}
	symbolSet := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		symbolSet[strings.ToUpper(t.Symbol)] = true
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
		pd, ok := prices[strings.ToUpper(tokens[i].Symbol)]
		if !ok || pd.USD == 0 {
			continue
		}
		// balance is in smallest units; convert to whole-units float for
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
