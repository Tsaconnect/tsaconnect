package models

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Spot Buy/Sell routing targets. Route decides which EXISTING app flow a
// listing's Buy/Sell buttons open — no new swap/contract logic is introduced.
const (
	SpotRouteMCGP = "mcgp" // Contract B USDC OTC desk (/wallet/mcgp/buy|sell)
	SpotRouteLiFi = "lifi" // generic cross-chain swap (All Swap / LiFi)
	// SpotRouteAllSwap hands the pair to All Swap with each leg on its own
	// chain. Unlike the other two it needs QuoteChain: the desk cannot infer a
	// second chain, and resolving the quote leg on the base leg's chain is
	// exactly the failure this route exists to avoid.
	SpotRouteAllSwap = "allswap"
)

// ValidSpotRoute reports whether r is a supported Buy/Sell routing target.
func ValidSpotRoute(r string) bool {
	switch r {
	case SpotRouteMCGP, SpotRouteLiFi, SpotRouteAllSwap:
		return true
	default:
		return false
	}
}

// Spot listing taxonomy categories (admin-assigned, per PM asset-classification
// spec). Computed tabs (Hot/New/Gainers/Losers/Favorites) are derived client-side
// and are NOT categories. "spot" is the default bucket.
const (
	SpotCategorySpot      = "spot"
	SpotCategoryAlpha     = "alpha"
	SpotCategoryTStocks   = "tstocks"
	SpotCategoryTokenized = "tokenized" // Tokenized market
	SpotCategoryTradefi   = "tradefi"
	SpotCategoryRWA       = "rwa"
)

// ValidSpotCategory reports whether cat is a recognised taxonomy category.
func ValidSpotCategory(cat string) bool {
	switch cat {
	case SpotCategorySpot, SpotCategoryAlpha, SpotCategoryTStocks,
		SpotCategoryTokenized, SpotCategoryTradefi, SpotCategoryRWA:
		return true
	default:
		return false
	}
}

// SpotChains is the set of chain keys a listing may reference. Mirrors the
// NetworkConfig.Chains / SupportedToken chain-key space and the admin UI picker.
//
// Includes the non-EVM chains the app's Spot Trade desk can route on. Most of
// them carry both legs of a pair; "btc" does not — LiFi lists exactly one token
// on Bitcoin (BTC itself), so a BTC pair only exists across two chains and is
// legal only on the allswap route, which names the quote leg's chain separately
// (see NormalizeSpotLegs).
//
// "tron" joined once LiFi started routing Tron (chain 728126428, SunSwap among
// others; verified 2026-10-14). Its quote leg is USDT only — see
// SpotUSDTOnlyQuoteChains.
var SpotChains = map[string]bool{
	"sonic": true, "bsc": true, "ethereum": true, "polygon": true,
	"arbitrum": true, "base": true, "optimism": true, "avalanche": true,
	"linea": true, "robinhood": true,
	"solana": true, "sui": true, "btc": true, "tron": true,
}

// SpotUSDTOnlyQuoteChains are chains with no usable USDC, so a quote leg on them
// must be USDT. Circle discontinued USDC on Tron, and the only "USDC" LiFi
// lists there is an unverified 18-decimal token — a USDC-quoted Tron listing
// would price against the real coin and trade the impostor.
var SpotUSDTOnlyQuoteChains = map[string]bool{"tron": true}

// SpotSingleTokenChains maps each chain that holds exactly one tradeable token
// to that token's symbol, so no pair can be resolved with both legs on them.
// They are legal only on a route that names a second chain.
var SpotSingleTokenChains = map[string]string{"btc": "BTC"}

// SpotDefaultQuoteChains is where an auto listing's quote leg goes when its base
// chain cannot carry the quote asset. BSC is the chain All Swap already opens
// paying from; Base was the cheapest USDC → BTC route measured 2026-09-14.
var SpotDefaultQuoteChains = map[string]string{"USDT": "bsc", "USDC": "base"}

// DefaultSpotQuoteChain is the concrete quote chain served for an auto listing
// (empty QuoteChain on the allswap route): the base chain when it can carry the
// quote asset, else the asset's default. Apps that predate auto read quoteChain
// and nothing else, so an auto listing must still name a chain that quotes.
// quoteSymbol must already be uppercased.
func DefaultSpotQuoteChain(chain, quoteSymbol string) string {
	if _, single := SpotSingleTokenChains[chain]; !single {
		return chain
	}
	if c, ok := SpotDefaultQuoteChains[quoteSymbol]; ok {
		return c
	}
	return "bsc"
}

// ValidSpotChain reports whether chain is a recognised chain key (lowercase).
func ValidSpotChain(chain string) bool {
	return SpotChains[chain]
}

// NormalizeSpotLegs cross-checks the two legs of a listing and returns the
// quote chain to persist. Its inputs must already be trimmed and lowercased.
//
// Each rule here refuses a combination the admin form happily accepts but the
// trade desk can never quote: the listing renders, the user taps Buy, and the
// route lookup fails on a chain that has no such token. Catching it at write
// time is the difference between an admin seeing an error and a user seeing one.
//
// quoteSymbol must already be trimmed and uppercased.
func NormalizeSpotLegs(chain, route, quoteSymbol, quoteChain string) (string, error) {
	normalized, err := normalizeSpotLegs(chain, route, quoteSymbol, quoteChain)
	if err != nil {
		return "", err
	}
	// The chain the quote leg actually resolves on: the base chain when the
	// route keeps both legs there, else the stored quote chain, else what an
	// auto listing is served as (DefaultSpotQuoteChain).
	quoteLegChain := chain
	if route == SpotRouteAllSwap {
		quoteLegChain = normalized
		if quoteLegChain == "" {
			quoteLegChain = DefaultSpotQuoteChain(chain, quoteSymbol)
		}
	}
	if SpotUSDTOnlyQuoteChains[quoteLegChain] && quoteSymbol != "USDT" {
		return "", fmt.Errorf("Tron pairs must quote in USDT: there is no usable %s on %q", quoteSymbol, quoteLegChain)
	}
	return normalized, nil
}

func normalizeSpotLegs(chain, route, quoteSymbol, quoteChain string) (string, error) {
	if route != SpotRouteAllSwap {
		// quoteChain is dropped rather than refused. It describes a second leg
		// that only allswap has, so keeping it would leave the row claiming a
		// leg nothing routes; and refusing it would make moving a listing off
		// allswap impossible for any console that PUTs the whole object back.
		if _, single := SpotSingleTokenChains[chain]; single {
			return "", fmt.Errorf("Chain %q holds only one tradeable token, so both legs cannot live on it. Use route %q", chain, SpotRouteAllSwap)
		}
		return "", nil
	}
	// An empty quoteChain is "auto": the app pays from (or delivers to) the
	// chain where the user already holds the quote asset. Every LiFi route
	// quotes within about a point of the others, so which of the user's
	// balances gets spent matters more than which chain an admin guessed.
	if quoteChain == "" {
		return "", nil
	}
	if !ValidSpotChain(quoteChain) {
		return "", errors.New("Unsupported quoteChain")
	}
	if native, single := SpotSingleTokenChains[quoteChain]; single && quoteSymbol != native {
		return "", fmt.Errorf("%s does not exist on %q, which carries only %s. Pick the chain the %s is on, or leave quoteChain empty", quoteSymbol, quoteChain, native, quoteSymbol)
	}
	// A single-token chain cannot host BOTH legs, whatever the route. This is
	// the invariant the comment below names, and it was previously enforced
	// only on the non-allswap path — so btc/btc sailed through, saved, rendered
	// with a price, and then asked the user to pick a quote token on a chain
	// where none exists.
	//
	// Bitcoin on ONE leg is the entire point of allswap and stays legal; it is
	// only both legs that cannot work.
	_, baseSingle := SpotSingleTokenChains[chain]
	if _, quoteSingle := SpotSingleTokenChains[quoteChain]; baseSingle && quoteSingle {
		return "", fmt.Errorf("Chain %q holds only one tradeable token, so it cannot be both legs. Put the quote leg on another chain", chain)
	}
	// chain == quoteChain is otherwise deliberately allowed. All Swap is
	// cross-chain capable, not cross-chain only, so such a listing still quotes
	// and still trades; refusing it would reject a working pair to enforce a
	// preference about which route names it. The invariant worth defending is
	// that every leg names a chain that carries its token, not that the two
	// chains differ — which is exactly what the check above enforces.
	return quoteChain, nil
}

// SpotListing is an admin-curated spot-trade pair surfaced on the app's Spot
// Trade tab. It stores no price or on-chain address itself: price/24h-change are
// enriched at read time from PriceService, and the token resolves from
// (Chain, BaseSymbol) via the existing "chain:symbol" address map. Buy/Sell reuse
// existing flows selected by Route. BaseSymbol/QuoteSymbol are uppercase, Chain
// is a lowercase NetworkConfig chain key (e.g. "sonic"), matching SupportedToken.
// Chain is the BASE leg's chain; a listing whose quote leg lives elsewhere names
// that chain in QuoteChain and routes through All Swap.
type SpotListing struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BaseSymbol  string    `gorm:"not null;uniqueIndex:idx_spot_pair" json:"baseSymbol"`
	QuoteSymbol string    `gorm:"not null;uniqueIndex:idx_spot_pair" json:"quoteSymbol"`
	Chain       string    `gorm:"not null;uniqueIndex:idx_spot_pair" json:"chain"`
	Route       string    `gorm:"default:'lifi'" json:"route"`
	// QuoteChain is the chain the QUOTE leg lives on, for listings whose two
	// legs are on different chains (Route == allswap). Empty everywhere else,
	// where Chain carries both legs. No `omitempty`: the app branches on this
	// field, and a key that disappears when empty makes "same-chain listing"
	// and "old server that never heard of quote chains" look identical.
	QuoteChain string `gorm:"column:quote_chain" json:"quoteChain"`
	LogoURL    string `json:"logoUrl,omitempty"`
	// No `default:true` tag: GORM would then omit a zero-value (false) on insert
	// and fall back to the DB default, so creating a disabled listing would
	// silently persist as enabled. Handlers set Enabled explicitly instead.
	Enabled      bool `gorm:"not null" json:"enabled"`
	DisplayOrder int  `gorm:"column:display_order" json:"displayOrder"`
	// Category is the admin-assigned taxonomy bucket (ValidSpotCategory).
	// Handlers default it to "spot" explicitly, mirroring the Enabled note above.
	Category string `gorm:"default:'spot'" json:"category"`
	// PinnedPosition is a 1-based admin override in the ranked public list
	// (nil = unpinned; ranking then applies default pins / dynamic order).
	PinnedPosition *int      `gorm:"column:pinned_position" json:"pinnedPosition"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (SpotListing) TableName() string {
	return "spot_listings"
}

// BeforeCreate assigns a UUID when one isn't set, so inserts work regardless of
// the DB's default (Postgres gen_random_uuid() vs SQLite in tests / seeds).
func (s *SpotListing) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}
