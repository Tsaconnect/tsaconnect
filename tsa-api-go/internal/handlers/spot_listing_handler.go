package handlers

import (
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
	Price     *float64 `json:"price"`
	Change24h *float64 `json:"change24h"`
}

// spotListingInput is the admin create/update body. Pointer fields make partial
// updates unambiguous (a missing field is left unchanged).
type spotListingInput struct {
	BaseSymbol   string `json:"baseSymbol"`
	QuoteSymbol  string `json:"quoteSymbol"`
	Chain        string `json:"chain"`
	Route        string `json:"route"`
	LogoURL      string `json:"logoUrl"`
	Enabled      *bool  `json:"enabled"`
	DisplayOrder *int   `json:"displayOrder"`
}

// enrichSpotListings attaches price + 24h change (best-effort) from PriceService.
// Price/Change24h are left nil when a symbol has no price (fetch failure or a
// symbol the price service doesn't know), so the UI can show "—" rather than $0.
func (h *Handlers) enrichSpotListings(rows []models.SpotListing) []spotListingResponse {
	seen := map[string]struct{}{}
	for _, r := range rows {
		seen[strings.ToUpper(r.BaseSymbol)] = struct{}{}
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
	out := make([]spotListingResponse, 0, len(rows))
	for _, r := range rows {
		resp := spotListingResponse{SpotListing: r}
		if pd, ok := prices[strings.ToUpper(r.BaseSymbol)]; ok {
			price, change := pd.USD, pd.USD24hChange
			resp.Price = &price
			resp.Change24h = &change
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
	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
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
	utils.SuccessResponse(c, http.StatusOK, "Spot listings", gin.H{"listings": h.enrichSpotListings(rows)})
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
	listing := models.SpotListing{
		BaseSymbol:  base,
		QuoteSymbol: quote,
		Chain:       chain,
		Route:       route,
		LogoURL:     strings.TrimSpace(in.LogoURL),
		Enabled:     true,
	}
	if in.Enabled != nil {
		listing.Enabled = *in.Enabled
	}
	if in.DisplayOrder != nil {
		listing.DisplayOrder = *in.DisplayOrder
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
	if s := strings.ToLower(strings.TrimSpace(in.Chain)); s != "" {
		if !models.ValidSpotChain(s) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported chain")
			return
		}
		updates["chain"] = s
	}
	if s := strings.ToLower(strings.TrimSpace(in.Route)); s != "" {
		if !models.ValidSpotRoute(s) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid route")
			return
		}
		updates["route"] = s
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
