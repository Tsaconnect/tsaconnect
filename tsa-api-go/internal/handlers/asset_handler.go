package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// GetAssets returns all assets for the authenticated user.
// GET /api/assets/ — auth, showHidden query param, fetch real-time prices, calculate totals.
func (h *Handlers) GetAssets(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	showHidden := c.Query("showHidden") == "true"

	query := config.DB.Where("user_id = ?", user.ID)
	if !showHidden {
		query = query.Where("is_hidden = ?", false)
	}

	var assets []models.Asset
	if err := query.Find(&assets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}

	// Fetch real-time prices
	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		symbols = append(symbols, a.Symbol)
	}

	var totalValue, totalChange float64
	if len(symbols) > 0 {
		prices, err := h.PriceService.GetPrices(symbols)
		if err == nil {
			for i := range assets {
				if pd, ok := prices[assets[i].Symbol]; ok {
					assets[i].USDValue = assets[i].Balance * pd.USD
					totalValue += assets[i].USDValue
					totalChange += pd.USD24hChange
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Assets retrieved successfully",
		"data": gin.H{
			"assets":      assets,
			"totalValue":  totalValue,
			"totalChange": totalChange,
			"count":       len(assets),
		},
	})
}

// GetAssetByID returns details for a specific asset.
// GET /api/assets/:id — auth, market data + 30-day history.
func (h *Handlers) GetAssetByID(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	var asset models.Asset
	if err := config.DB.Where("id = ? AND user_id = ?", assetID, user.ID).First(&asset).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Fetch market data
	marketData, _ := h.PriceService.GetMarketData(asset.Symbol)

	// Fetch 30-day price history
	history, _ := h.PriceService.GetHistoricalPrice(asset.Symbol, 30)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset details retrieved successfully",
		"data": gin.H{
			"asset":      asset,
			"marketData": marketData,
			"history":    history,
		},
	})
}

// SelectAsset selects an asset as active and unselects others.
// POST /api/assets/select — auth, unselect others, select new, update wallet.
func (h *Handlers) SelectAsset(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		AssetID string `json:"assetId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Asset ID is required"})
		return
	}

	assetID, err := uuid.Parse(body.AssetID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	// Unselect all user assets
	if err := config.DB.Model(&models.Asset{}).Where("user_id = ?", user.ID).Updates(map[string]interface{}{
		"is_selected": false,
		"updated_at":  time.Now(),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to unselect assets"})
		return
	}

	// Select the target asset
	var asset models.Asset
	if err := config.DB.Where("id = ? AND user_id = ?", assetID, user.ID).First(&asset).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	if err := config.DB.Model(&asset).Updates(map[string]interface{}{
		"is_selected": true,
		"updated_at":  time.Now(),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to select asset"})
		return
	}

	// Update wallet selected asset — upsert
	var wallet models.Wallet
	result := config.DB.Where("user_id = ?", user.ID).First(&wallet)
	if result.Error != nil {
		// Create wallet
		wallet = models.Wallet{
			ID:            uuid.New(),
			UserID:        user.ID,
			SelectedAsset: asset.Symbol,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		config.DB.Create(&wallet)
	} else {
		config.DB.Model(&wallet).Updates(map[string]interface{}{
			"selected_asset": asset.Symbol,
			"updated_at":     time.Now(),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset selected successfully",
		"data":    asset,
	})
}

// ToggleAssetVisibility toggles the hidden state of an asset.
// PUT /api/assets/:id/visibility — auth.
func (h *Handlers) ToggleAssetVisibility(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	var asset models.Asset
	if err := config.DB.Where("id = ? AND user_id = ?", assetID, user.ID).First(&asset).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	newHidden := !asset.IsHidden
	if err := config.DB.Model(&asset).Updates(map[string]interface{}{
		"is_hidden":  newHidden,
		"updated_at": time.Now(),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to update visibility"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset visibility updated successfully",
		"data": gin.H{
			"assetId":  assetID.String(),
			"isHidden": newHidden,
		},
	})
}

// RefreshAssetPrices refreshes prices for user assets.
// POST /api/assets/refresh — auth, optional assetId query, update portfolio.
func (h *Handlers) RefreshAssetPrices(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	query := config.DB.Where("user_id = ?", user.ID)

	specificAssetID := c.Query("assetId")
	if specificAssetID != "" {
		oid, err := uuid.Parse(specificAssetID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
			return
		}
		query = query.Where("id = ?", oid)
	}

	var assets []models.Asset
	if err := query.Find(&assets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}

	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		symbols = append(symbols, a.Symbol)
	}

	if len(symbols) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "No assets to refresh",
			"data":    gin.H{"updated": 0},
		})
		return
	}

	prices, err := h.PriceService.GetPrices(symbols)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch prices"})
		return
	}

	now := time.Now()
	var totalValue float64
	updated := 0
	for _, asset := range assets {
		if pd, ok := prices[asset.Symbol]; ok {
			usdValue := asset.Balance * pd.USD
			totalValue += usdValue
			err := config.DB.Model(&models.Asset{}).Where("id = ?", asset.ID).Updates(map[string]interface{}{
				"usd_value":   usdValue,
				"last_synced": now,
				"updated_at":  now,
			}).Error
			if err == nil {
				updated++
			}
		}
	}

	// Update portfolio total — upsert
	totalValueJSON, _ := json.Marshal(&models.PortfolioTotalValue{Current: totalValue})
	var portfolio models.Portfolio
	result := config.DB.Where("user_id = ?", user.ID).First(&portfolio)
	if result.Error != nil {
		portfolio = models.Portfolio{
			ID:         uuid.New(),
			UserID:     user.ID,
			TotalValue: totalValueJSON,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		config.DB.Create(&portfolio)
	} else {
		config.DB.Model(&portfolio).Updates(map[string]interface{}{
			"total_value": totalValueJSON,
			"updated_at":  now,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset prices refreshed successfully",
		"data": gin.H{
			"updated":    updated,
			"totalValue": totalValue,
		},
	})
}

// CreateAsset adds a new asset for the user.
// POST /api/assets/ — auth, check duplicate, get current price, create.
func (h *Handlers) CreateAsset(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		Symbol  string  `json:"symbol" binding:"required"`
		Name    string  `json:"name" binding:"required"`
		Balance float64 `json:"balance"`
		Type    string  `json:"type"`
		Chain   string  `json:"chain"`
		Icon    string  `json:"icon"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Symbol and name are required"})
		return
	}

	// Check duplicate
	var count int64
	if err := config.DB.Model(&models.Asset{}).Where("user_id = ? AND symbol = ?", user.ID, body.Symbol).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to check for duplicates"})
		return
	}
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Asset already exists in your portfolio"})
		return
	}

	// Get current price
	var usdValue float64
	prices, err := h.PriceService.GetPrices([]string{body.Symbol})
	if err == nil {
		if pd, ok := prices[body.Symbol]; ok {
			usdValue = body.Balance * pd.USD
		}
	}

	assetType := body.Type
	if assetType == "" {
		assetType = models.AssetTypeToken
	}
	chain := body.Chain
	if chain == "" {
		chain = models.ChainEthereum
	}

	now := time.Now()
	asset := models.Asset{
		ID:         uuid.New(),
		UserID:     user.ID,
		Symbol:     body.Symbol,
		Name:       body.Name,
		Balance:    body.Balance,
		USDValue:   usdValue,
		IsSelected: false,
		IsHidden:   false,
		LastSynced: &now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	asset.SetDetails(&models.AssetDetails{
		Type:    assetType,
		Chain:   chain,
		IconURL: body.Icon,
	})

	if err := config.DB.Create(&asset).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create asset"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Asset added successfully",
		"data":    asset,
	})
}

// UpdateAssetBalance updates the balance of an asset.
// PUT /api/assets/:id/balance — auth, validate >= 0, update USD value.
func (h *Handlers) UpdateAssetBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	var body struct {
		Balance float64 `json:"balance"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Balance is required"})
		return
	}
	if body.Balance < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Balance must be >= 0"})
		return
	}

	var asset models.Asset
	if err := config.DB.Where("id = ? AND user_id = ?", assetID, user.ID).First(&asset).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Calculate new USD value
	var usdValue float64
	prices, err := h.PriceService.GetPrices([]string{asset.Symbol})
	if err == nil {
		if pd, ok := prices[asset.Symbol]; ok {
			usdValue = body.Balance * pd.USD
		}
	}

	now := time.Now()
	if err := config.DB.Model(&asset).Updates(map[string]interface{}{
		"balance":    body.Balance,
		"usd_value":  usdValue,
		"updated_at": now,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to update balance"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset balance updated successfully",
		"data": gin.H{
			"assetId":  assetID.String(),
			"balance":  body.Balance,
			"usdValue": usdValue,
		},
	})
}

// GetAssetPerformance returns performance metrics for an asset.
// GET /api/assets/:id/performance — auth, 90-day history, calculate daily/weekly/monthly/allTime.
func (h *Handlers) GetAssetPerformance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	var asset models.Asset
	if err := config.DB.Where("id = ? AND user_id = ?", assetID, user.ID).First(&asset).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Get 90-day price history
	history, err := h.PriceService.GetHistoricalPrice(asset.Symbol, 90)
	if err != nil || len(history) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "Performance data retrieved",
			"data": gin.H{
				"symbol":      asset.Symbol,
				"performance": gin.H{"daily": 0, "weekly": 0, "monthly": 0, "allTime": 0},
				"history":     []interface{}{},
			},
		})
		return
	}

	currentPrice := history[len(history)-1].Price

	calcChange := func(oldPrice float64) float64 {
		if oldPrice == 0 {
			return 0
		}
		return ((currentPrice - oldPrice) / oldPrice) * 100
	}

	var daily, weekly, monthly, allTime float64

	if len(history) >= 2 {
		daily = calcChange(history[len(history)-2].Price)
	}
	if len(history) >= 7 {
		weekly = calcChange(history[len(history)-7].Price)
	}
	if len(history) >= 30 {
		monthly = calcChange(history[len(history)-30].Price)
	}
	allTime = calcChange(history[0].Price)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Performance data retrieved successfully",
		"data": gin.H{
			"symbol": asset.Symbol,
			"performance": gin.H{
				"daily":   daily,
				"weekly":  weekly,
				"monthly": monthly,
				"allTime": allTime,
			},
			"history": history,
		},
	})
}
