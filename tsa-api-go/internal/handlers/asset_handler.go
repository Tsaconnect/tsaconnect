package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AssetHandler handles asset-related HTTP requests.
type AssetHandler struct {
	priceService *services.PriceService
}

// NewAssetHandler creates a new AssetHandler.
func NewAssetHandler(ps *services.PriceService) *AssetHandler {
	return &AssetHandler{priceService: ps}
}

// GetUserAssets returns all assets for the authenticated user.
// GET /api/assets/ — auth, showHidden query param, fetch real-time prices, calculate totals.
func (h *AssetHandler) GetUserAssets(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	showHidden := c.Query("showHidden") == "true"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("assets")
	filter := bson.M{"userId": user.ID}
	if !showHidden {
		filter["isHidden"] = bson.M{"$ne": true}
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}
	defer cursor.Close(ctx)

	var assets []models.Asset
	if err := cursor.All(ctx, &assets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode assets"})
		return
	}

	// Fetch real-time prices
	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		symbols = append(symbols, a.Symbol)
	}

	var totalValue, totalChange float64
	if len(symbols) > 0 {
		prices, err := h.priceService.GetPrices(symbols)
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

// GetAssetDetails returns details for a specific asset.
// GET /api/assets/:assetId — auth, market data + 30-day history.
func (h *AssetHandler) GetAssetDetails(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := primitive.ObjectIDFromHex(c.Param("assetId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("assets")
	var asset models.Asset
	err = collection.FindOne(ctx, bson.M{"_id": assetID, "userId": user.ID}).Decode(&asset)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Fetch market data
	marketData, _ := h.priceService.GetMarketData(asset.Symbol)

	// Fetch 30-day price history
	history, _ := h.priceService.GetHistoricalPrice(asset.Symbol, 30)

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
func (h *AssetHandler) SelectAsset(c *gin.Context) {
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

	assetID, err := primitive.ObjectIDFromHex(body.AssetID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assetsCol := config.GetCollection("assets")

	// Unselect all user assets
	_, err = assetsCol.UpdateMany(ctx,
		bson.M{"userId": user.ID},
		bson.M{"$set": bson.M{"isSelected": false, "updatedAt": time.Now()}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to unselect assets"})
		return
	}

	// Select the target asset
	result := assetsCol.FindOneAndUpdate(ctx,
		bson.M{"_id": assetID, "userId": user.ID},
		bson.M{"$set": bson.M{"isSelected": true, "updatedAt": time.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var asset models.Asset
	if err := result.Decode(&asset); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Update wallet selected asset
	walletsCol := config.GetCollection("wallets")
	_, _ = walletsCol.UpdateOne(ctx,
		bson.M{"userId": user.ID},
		bson.M{"$set": bson.M{"selectedAsset": asset.Symbol, "updatedAt": time.Now()}},
		options.Update().SetUpsert(true),
	)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset selected successfully",
		"data":    asset,
	})
}

// ToggleAssetVisibility toggles the hidden state of an asset.
// PUT /api/assets/:assetId/visibility — auth.
func (h *AssetHandler) ToggleAssetVisibility(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := primitive.ObjectIDFromHex(c.Param("assetId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("assets")

	var asset models.Asset
	err = collection.FindOne(ctx, bson.M{"_id": assetID, "userId": user.ID}).Decode(&asset)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	newHidden := !asset.IsHidden
	_, err = collection.UpdateOne(ctx,
		bson.M{"_id": assetID},
		bson.M{"$set": bson.M{"isHidden": newHidden, "updatedAt": time.Now()}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to update visibility"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset visibility updated successfully",
		"data": gin.H{
			"assetId":  assetID.Hex(),
			"isHidden": newHidden,
		},
	})
}

// RefreshAssetPrices refreshes prices for user assets.
// POST /api/assets/refresh — auth, optional assetId query, update portfolio.
func (h *AssetHandler) RefreshAssetPrices(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assetsCol := config.GetCollection("assets")
	filter := bson.M{"userId": user.ID}

	specificAssetID := c.Query("assetId")
	if specificAssetID != "" {
		oid, err := primitive.ObjectIDFromHex(specificAssetID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
			return
		}
		filter["_id"] = oid
	}

	cursor, err := assetsCol.Find(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch assets"})
		return
	}
	defer cursor.Close(ctx)

	var assets []models.Asset
	if err := cursor.All(ctx, &assets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to decode assets"})
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

	prices, err := h.priceService.GetPrices(symbols)
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
			_, err := assetsCol.UpdateOne(ctx,
				bson.M{"_id": asset.ID},
				bson.M{"$set": bson.M{
					"usdValue":   usdValue,
					"lastSynced": now,
					"updatedAt":  now,
				}},
			)
			if err == nil {
				updated++
			}
		}
	}

	// Update portfolio total
	portfolioCol := config.GetCollection("portfolios")
	_, _ = portfolioCol.UpdateOne(ctx,
		bson.M{"userId": user.ID},
		bson.M{"$set": bson.M{"totalValue": totalValue, "updatedAt": now}},
		options.Update().SetUpsert(true),
	)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset prices refreshed successfully",
		"data": gin.H{
			"updated":    updated,
			"totalValue": totalValue,
		},
	})
}

// AddAsset adds a new asset for the user.
// POST /api/assets/ — auth, check duplicate, get current price, create.
func (h *AssetHandler) AddAsset(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("assets")

	// Check duplicate
	count, err := collection.CountDocuments(ctx, bson.M{"userId": user.ID, "symbol": body.Symbol})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to check for duplicates"})
		return
	}
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Asset already exists in your portfolio"})
		return
	}

	// Get current price
	var usdValue float64
	prices, err := h.priceService.GetPrices([]string{body.Symbol})
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
		ID:         primitive.NewObjectID(),
		UserID:     user.ID,
		Symbol:     body.Symbol,
		Name:       body.Name,
		Balance:    body.Balance,
		USDValue:   usdValue,
		IsSelected: false,
		IsHidden:   false,
		Details: &models.AssetDetails{
			Type:    assetType,
			Chain:   chain,
			IconURL: body.Icon,
		},
		LastSynced: &now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	_, err = collection.InsertOne(ctx, asset)
	if err != nil {
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
// PUT /api/assets/:assetId/balance — auth, validate >= 0, update USD value.
func (h *AssetHandler) UpdateAssetBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := primitive.ObjectIDFromHex(c.Param("assetId"))
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("assets")

	var asset models.Asset
	err = collection.FindOne(ctx, bson.M{"_id": assetID, "userId": user.ID}).Decode(&asset)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Calculate new USD value
	var usdValue float64
	prices, err := h.priceService.GetPrices([]string{asset.Symbol})
	if err == nil {
		if pd, ok := prices[asset.Symbol]; ok {
			usdValue = body.Balance * pd.USD
		}
	}

	now := time.Now()
	_, err = collection.UpdateOne(ctx,
		bson.M{"_id": assetID},
		bson.M{"$set": bson.M{
			"balance":   body.Balance,
			"usdValue":  usdValue,
			"updatedAt": now,
		}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to update balance"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset balance updated successfully",
		"data": gin.H{
			"assetId":  assetID.Hex(),
			"balance":  body.Balance,
			"usdValue": usdValue,
		},
	})
}

// GetAssetPerformance returns performance metrics for an asset.
// GET /api/assets/:assetId/performance — auth, 90-day history, calculate daily/weekly/monthly/allTime.
func (h *AssetHandler) GetAssetPerformance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	assetID, err := primitive.ObjectIDFromHex(c.Param("assetId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid asset ID"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("assets")
	var asset models.Asset
	err = collection.FindOne(ctx, bson.M{"_id": assetID, "userId": user.ID}).Decode(&asset)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	// Get 90-day price history
	history, err := h.priceService.GetHistoricalPrice(asset.Symbol, 90)
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
