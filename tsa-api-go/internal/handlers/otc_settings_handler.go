package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// OTCSettingsHandler serves the OTC listing controls — the PM's per-direction
// kill-switches (buy/sell) plus listing metadata (logo, asset class, category)
// for the app's Easyswap "OTC" tab. The app reads GET /api/otc/settings and
// fails OPEN if unreachable; the admin dashboard writes via
// PUT /api/admin/otc/settings.
type OTCSettingsHandler struct {
	DB *gorm.DB
}

// NewOTCSettingsHandler creates an OTCSettingsHandler.
func NewOTCSettingsHandler(db *gorm.DB) *OTCSettingsHandler {
	return &OTCSettingsHandler{DB: db}
}

// loadOrSeed returns the singleton settings row, creating it with defaults
// (both directions enabled, category "direct") on first read.
func (h *OTCSettingsHandler) loadOrSeed() (*models.OTCSettings, error) {
	var row models.OTCSettings
	err := h.DB.Order("created_at ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// All-defaults seed — see the OTCSettings doc: flags must not be
		// created false via struct fields, and here they are true anyway.
		row = models.OTCSettings{
			BuyEnabled:  true,
			SellEnabled: true,
			Category:    models.OTCCategoryDirect,
		}
		if err := h.DB.Create(&row).Error; err != nil {
			return nil, err
		}
		return &row, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetOTCSettings handles GET /api/otc/settings (auth) — the app's read.
func (h *OTCSettingsHandler) GetOTCSettings(c *gin.Context) {
	row, err := h.loadOrSeed()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load OTC settings")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OTC settings", row)
}

// updateOTCSettingsRequest is the PUT body. All fields are optional pointers so
// a partial update only touches the fields present in the request.
type updateOTCSettingsRequest struct {
	BuyEnabled  *bool   `json:"buyEnabled"`
	SellEnabled *bool   `json:"sellEnabled"`
	LogoURL     *string `json:"logoUrl"`
	AssetClass  *string `json:"assetClass"`
	Category    *string `json:"category"`
}

// UpdateOTCSettings handles PUT /api/admin/otc/settings (AdminAuth +
// RequireP2PAdmin) — partial update of the singleton row. Updates go through a
// column map so a false flag persists (a struct write would drop the zero
// value and the DB default would flip it back to true).
func (h *OTCSettingsHandler) UpdateOTCSettings(c *gin.Context) {
	var req updateOTCSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.BuyEnabled != nil {
		updates["buy_enabled"] = *req.BuyEnabled
	}
	if req.SellEnabled != nil {
		updates["sell_enabled"] = *req.SellEnabled
	}
	if req.LogoURL != nil {
		updates["logo_url"] = strings.TrimSpace(*req.LogoURL)
	}
	if req.AssetClass != nil {
		updates["asset_class"] = strings.TrimSpace(*req.AssetClass)
	}
	if req.Category != nil {
		category := strings.ToLower(strings.TrimSpace(*req.Category))
		if !models.ValidOTCCategory(category) {
			utils.ErrorResponse(c, http.StatusBadRequest, "category must be 'direct' or 'presale'")
			return
		}
		updates["category"] = category
	}
	if len(updates) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "No updatable fields provided (buyEnabled, sellEnabled, logoUrl, assetClass, category)")
		return
	}

	row, err := h.loadOrSeed()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load OTC settings")
		return
	}
	if err := h.DB.Model(row).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update OTC settings")
		return
	}
	if err := h.DB.First(row, "id = ?", row.ID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reload OTC settings")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OTC settings updated", row)
}
