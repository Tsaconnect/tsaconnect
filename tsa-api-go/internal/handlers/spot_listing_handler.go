package handlers

import (
	"math/big"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// spot_listing_handler.go — admin-curated Spot Trade listings. Reads are enriched
// with live price + 24h change from PriceService; Buy/Sell reuse existing app
// flows (chosen by SpotListing.Route). No new contract or swap logic here.

// spotListingResponse is a SpotListing enriched with live price + 24h change.
// Price/Change24h are pointers so a symbol with no available price serializes as
// null (UI shows "—") rather than a misleading $0 / +0.00%.
type spotListingResponse struct {
	models.SpotListing
	// QuoteChainAuto marks an allswap listing the admin left on auto. Its
	// QuoteChain is then filled with models.DefaultSpotQuoteChain, because apps
	// that predate auto read QuoteChain alone and would otherwise ask for the
	// quote asset on the base chain (USDT on Bitcoin). Newer apps read this
	// flag and pick the chain the user holds the quote asset on.
	QuoteChainAuto bool     `json:"quoteChainAuto"`
	Price          *float64 `json:"price"`
	Change24h      *float64 `json:"change24h"`
	MarketCap      *float64 `json:"marketCap"`
	Volume24h      *float64 `json:"volume24h"`
}

// spotListingInput is the admin create/update body. Pointer fields make partial
// updates unambiguous (a missing field is left unchanged).
type spotListingInput struct {
	BaseSymbol  string `json:"baseSymbol"`
	QuoteSymbol string `json:"quoteSymbol"`
	Chain       string `json:"chain"`
	// QuoteChain is only read for the allswap route; see models.NormalizeSpotLegs.
	// A pointer because "" is a real value there (auto), distinct from absent.
	QuoteChain   *string `json:"quoteChain"`
	Route        string  `json:"route"`
	LogoURL      string  `json:"logoUrl"`
	Enabled      *bool   `json:"enabled"`
	DisplayOrder *int    `json:"displayOrder"`
	Category     string  `json:"category"`
	// PinnedPosition <= 0 clears the pin (stored as NULL).
	PinnedPosition *int `json:"pinnedPosition"`
}

// oneMCGP is 1 MCGP in base units (18 decimals).
var oneMCGP = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// mcgpUsdPrice returns MCGP's USD price. MCGP isn't on CoinGecko, so its price
// comes from the platform: primarily Contract B's on-chain quote for buying
// 1 MCGP (the same price the Buy flow at /wallet/mcgp/buy pays), falling back to
// the admin-set IT rate. USDC and IT micro-dollars are both 6dp. Returns nil when
// neither source is available.
func (h *Handlers) mcgpUsdPrice() *float64 {
	// Primary: the on-chain premium-buy quote (USDC for 1 MCGP).
	if h.MCGPSwap != nil && h.MCGPSwap.IsConfigured() {
		if usdc, err := h.MCGPSwap.Quote(services.PriceKindPremiumBuy, oneMCGP); err == nil && usdc != nil && usdc.Sign() > 0 {
			p, _ := new(big.Float).Quo(new(big.Float).SetInt(usdc), big.NewFloat(1e6)).Float64()
			return &p
		}
	}
	// Fallback: the admin-set IT rate (BuyPriceUsdMicros, e.g. 20000 = $0.02).
	var r models.ITRate
	if err := config.DB.Where("active = ?", true).Order("created_at DESC").First(&r).Error; err == nil && r.BuyPriceUsdMicros > 0 {
		p := float64(r.BuyPriceUsdMicros) / 1_000_000
		return &p
	}
	return nil
}

// enrichSpotListings attaches price + 24h change. MCGP is priced from the
// platform's IT rate (it isn't on CoinGecko); every other symbol comes from
// PriceService. Fields are left nil when no price is available so the UI shows
// "—" rather than a misleading $0 / +0.00%.
func (h *Handlers) enrichSpotListings(rows []models.SpotListing) []spotListingResponse {
	seen := map[string]struct{}{}
	for _, r := range rows {
		sym := strings.ToUpper(r.BaseSymbol)
		if sym != "MCGP" { // MCGP isn't a CoinGecko symbol; priced separately
			seen[sym] = struct{}{}
		}
	}
	symbols := make([]string, 0, len(seen))
	for s := range seen {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols) // stable order → stable PriceService cache key
	var prices map[string]services.PriceData
	if h.PriceService != nil && len(symbols) > 0 {
		prices, _ = h.PriceService.GetPrices(symbols) // best-effort; nil map on error
	}

	// Resolve MCGP once (only if any row needs it), not per row.
	var mcgp *float64
	mcgpResolved := false

	out := make([]spotListingResponse, 0, len(rows))
	for _, r := range rows {
		resp := spotListingResponse{SpotListing: r}
		if r.Route == models.SpotRouteAllSwap && r.QuoteChain == "" {
			resp.QuoteChainAuto = true
			resp.QuoteChain = models.DefaultSpotQuoteChain(r.Chain, strings.ToUpper(r.QuoteSymbol))
		}
		if strings.ToUpper(r.BaseSymbol) == "MCGP" {
			if !mcgpResolved {
				mcgp = h.mcgpUsdPrice()
				mcgpResolved = true
			}
			resp.Price = mcgp // no 24h change for an admin-set OTC price
		} else if pd, ok := prices[strings.ToUpper(r.BaseSymbol)]; ok {
			price, change := pd.USD, pd.USD24hChange
			resp.Price = &price
			resp.Change24h = &change
			if pd.USDMarketCap > 0 {
				mcap := pd.USDMarketCap
				resp.MarketCap = &mcap
			}
			if pd.USD24hVol > 0 {
				vol := pd.USD24hVol
				resp.Volume24h = &vol
			}
		}
		out = append(out, resp)
	}
	return out
}

// spotRequireAdmin re-checks the caller is an admin/super_admin (the route is
// already behind adminAuth, which also admits merchant/support). Returns nil and
// writes the error response when not authorized.
func (h *Handlers) spotRequireAdmin(c *gin.Context) *models.User {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil
	}
	if !hasAdminPrivilege(c, user) {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return nil
	}
	return user
}

// GetSpotListings — GET /api/spot-listings (auth). Enabled rows enriched with price.
func (h *Handlers) GetSpotListings(c *gin.Context) {
	var rows []models.SpotListing
	if err := config.DB.Where("enabled = ?", true).
		Order("display_order ASC, base_symbol ASC").Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load spot listings")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Spot listings", gin.H{"listings": rankSpotListings(h.enrichSpotListings(rows))})
}

// ListSpotListingsAdmin — GET /api/admin/spot-listings. All rows incl. disabled.
func (h *Handlers) ListSpotListingsAdmin(c *gin.Context) {
	if h.spotRequireAdmin(c) == nil {
		return
	}
	var rows []models.SpotListing
	if err := config.DB.Order("display_order ASC, base_symbol ASC").Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load spot listings")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Spot listings", gin.H{"listings": rows})
}

// CreateSpotListing — POST /api/admin/spot-listings.
func (h *Handlers) CreateSpotListing(c *gin.Context) {
	if h.spotRequireAdmin(c) == nil {
		return
	}
	var in spotListingInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	base := strings.ToUpper(strings.TrimSpace(in.BaseSymbol))
	quote := strings.ToUpper(strings.TrimSpace(in.QuoteSymbol))
	chain := strings.ToLower(strings.TrimSpace(in.Chain))
	if base == "" || quote == "" || chain == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "baseSymbol, quoteSymbol and chain are required")
		return
	}
	if !models.ValidSpotChain(chain) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain")
		return
	}
	route := strings.ToLower(strings.TrimSpace(in.Route))
	if !models.ValidSpotRoute(route) {
		route = models.SpotRouteLiFi
	}
	quoteChainIn := ""
	if in.QuoteChain != nil {
		quoteChainIn = strings.ToLower(strings.TrimSpace(*in.QuoteChain))
	}
	quoteChain, err := models.NormalizeSpotLegs(chain, route, base, quote, quoteChainIn)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	category := strings.ToLower(strings.TrimSpace(in.Category))
	if category == "" {
		category = models.SpotCategorySpot
	}
	if !models.ValidSpotCategory(category) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category")
		return
	}
	listing := models.SpotListing{
		BaseSymbol:  base,
		QuoteSymbol: quote,
		Chain:       chain,
		QuoteChain:  quoteChain,
		Route:       route,
		LogoURL:     strings.TrimSpace(in.LogoURL),
		Enabled:     true,
		Category:    category,
	}
	if in.Enabled != nil {
		listing.Enabled = *in.Enabled
	}
	if in.DisplayOrder != nil {
		listing.DisplayOrder = *in.DisplayOrder
	}
	if in.PinnedPosition != nil && *in.PinnedPosition > 0 {
		listing.PinnedPosition = in.PinnedPosition
	}
	if err := config.DB.Create(&listing).Error; err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Failed to create listing (pair may already exist)")
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Spot listing created", listing)
}

// UpdateSpotListing — PUT /api/admin/spot-listings/:id. Partial update.
func (h *Handlers) UpdateSpotListing(c *gin.Context) {
	if h.spotRequireAdmin(c) == nil {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	var listing models.SpotListing
	if err := config.DB.First(&listing, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Listing not found")
		return
	}
	var in spotListingInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	updates := map[string]interface{}{}
	if s := strings.ToUpper(strings.TrimSpace(in.BaseSymbol)); s != "" {
		updates["base_symbol"] = s
	}
	if s := strings.ToUpper(strings.TrimSpace(in.QuoteSymbol)); s != "" {
		updates["quote_symbol"] = s
	}
	// chain, route and quoteChain are cross-checked as a set against the row as
	// it will look AFTER the update: a PUT that changes only the route can still
	// strand a stored btc chain, and validating each field on its own would let
	// it through. Empty means "unchanged", matching every other field here.
	chain, route := listing.Chain, listing.Route
	if s := strings.ToLower(strings.TrimSpace(in.Chain)); s != "" {
		if !models.ValidSpotChain(s) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain")
			return
		}
		chain = s
		updates["chain"] = s
	}
	if s := strings.ToLower(strings.TrimSpace(in.Route)); s != "" {
		if !models.ValidSpotRoute(s) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid route")
			return
		}
		route = s
		updates["route"] = s
	}
	// Unlike the fields above, an empty quoteChain is not "unchanged": it is how
	// an admin moves a listing back to auto. Only an absent one is left alone.
	quoteChain := listing.QuoteChain
	if in.QuoteChain != nil {
		quoteChain = strings.ToLower(strings.TrimSpace(*in.QuoteChain))
	}
	quoteSymbol := listing.QuoteSymbol
	if s, ok := updates["quote_symbol"].(string); ok {
		quoteSymbol = s
	}
	baseSymbol := listing.BaseSymbol
	if s, ok := updates["base_symbol"].(string); ok {
		baseSymbol = s
	}
	quoteChain, err = models.NormalizeSpotLegs(chain, route, baseSymbol, quoteSymbol, quoteChain)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if quoteChain != listing.QuoteChain {
		updates["quote_chain"] = quoteChain
	}
	if s := strings.TrimSpace(in.LogoURL); s != "" {
		updates["logo_url"] = s
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	if in.DisplayOrder != nil {
		updates["display_order"] = *in.DisplayOrder
	}
	if s := strings.ToLower(strings.TrimSpace(in.Category)); s != "" {
		if !models.ValidSpotCategory(s) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid category")
			return
		}
		updates["category"] = s
	}
	if in.PinnedPosition != nil {
		if *in.PinnedPosition > 0 {
			updates["pinned_position"] = *in.PinnedPosition
		} else {
			updates["pinned_position"] = nil // <= 0 clears the pin
		}
	}
	if len(updates) > 0 {
		if err := config.DB.Model(&listing).Updates(updates).Error; err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Failed to update listing (pair may already exist)")
			return
		}
	}
	config.DB.First(&listing, "id = ?", id)
	utils.SuccessResponse(c, http.StatusOK, "Spot listing updated", listing)
}

// DeleteSpotListing — DELETE /api/admin/spot-listings/:id.
func (h *Handlers) DeleteSpotListing(c *gin.Context) {
	if h.spotRequireAdmin(c) == nil {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	if err := config.DB.Delete(&models.SpotListing{}, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete listing")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Spot listing deleted", nil)
}

// ReorderSpotListings — POST /api/admin/spot-listings/reorder. Bulk display_order.
func (h *Handlers) ReorderSpotListings(c *gin.Context) {
	if h.spotRequireAdmin(c) == nil {
		return
	}
	var in struct {
		Items []struct {
			ID           uuid.UUID `json:"id"`
			DisplayOrder int       `json:"displayOrder"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		for _, it := range in.Items {
			if e := tx.Model(&models.SpotListing{}).
				Where("id = ?", it.ID).
				Update("display_order", it.DisplayOrder).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reorder listings")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Spot listings reordered", nil)
}
