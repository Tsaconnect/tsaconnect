package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Spot Buy/Sell routing targets. Route decides which EXISTING app flow a
// listing's Buy/Sell buttons open — no new swap/contract logic is introduced.
const (
	SpotRouteMCGP = "mcgp" // Contract B USDC OTC desk (/wallet/mcgp/buy|sell)
	SpotRouteLiFi = "lifi" // generic cross-chain swap (All Swap / LiFi)
)

// ValidSpotRoute reports whether r is a supported Buy/Sell routing target.
func ValidSpotRoute(r string) bool {
	switch r {
	case SpotRouteMCGP, SpotRouteLiFi:
		return true
	default:
		return false
	}
}

// SpotChains is the set of chain keys a listing may reference. Mirrors the
// NetworkConfig.Chains / SupportedToken chain-key space and the admin UI picker.
var SpotChains = map[string]bool{
	"sonic": true, "bsc": true, "ethereum": true, "polygon": true,
	"arbitrum": true, "base": true, "optimism": true, "avalanche": true, "linea": true,
}

// ValidSpotChain reports whether chain is a recognised chain key (lowercase).
func ValidSpotChain(chain string) bool {
	return SpotChains[chain]
}

// SpotListing is an admin-curated spot-trade pair surfaced on the app's Spot
// Trade tab. It stores no price or on-chain address itself: price/24h-change are
// enriched at read time from PriceService, and the token resolves from
// (Chain, BaseSymbol) via the existing "chain:symbol" address map. Buy/Sell reuse
// existing flows selected by Route. BaseSymbol/QuoteSymbol are uppercase, Chain
// is a lowercase NetworkConfig chain key (e.g. "sonic"), matching SupportedToken.
type SpotListing struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BaseSymbol   string    `gorm:"not null;uniqueIndex:idx_spot_pair" json:"baseSymbol"`
	QuoteSymbol  string    `gorm:"not null;uniqueIndex:idx_spot_pair" json:"quoteSymbol"`
	Chain        string    `gorm:"not null;uniqueIndex:idx_spot_pair" json:"chain"`
	Route        string    `gorm:"default:'lifi'" json:"route"`
	LogoURL string `json:"logoUrl,omitempty"`
	// No `default:true` tag: GORM would then omit a zero-value (false) on insert
	// and fall back to the DB default, so creating a disabled listing would
	// silently persist as enabled. Handlers set Enabled explicitly instead.
	Enabled      bool `gorm:"not null" json:"enabled"`
	DisplayOrder int  `gorm:"column:display_order" json:"displayOrder"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
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
