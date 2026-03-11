package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// GetMarketOverview returns an overview of market data.
// GET /api/market/overview — public, symbols query, prices + indices + trending.
func (h *Handlers) GetMarketOverview(c *gin.Context) {
	symbolsParam := c.DefaultQuery("symbols", "BTC,ETH,SOL,MATIC,USDT,BNB,MCGP")
	symbols := strings.Split(symbolsParam, ",")
	for i := range symbols {
		symbols[i] = strings.TrimSpace(strings.ToUpper(symbols[i]))
	}

	prices, err := h.PriceService.GetPrices(symbols)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch market data"})
		return
	}

	// Build market entries
	marketEntries := make([]gin.H, 0, len(prices))
	for sym, pd := range prices {
		marketEntries = append(marketEntries, gin.H{
			"symbol":    sym,
			"name":      getAssetName(sym),
			"price":     pd.USD,
			"change24h": pd.USD24hChange,
			"marketCap": pd.USDMarketCap,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Market overview retrieved successfully",
		"data": gin.H{
			"prices":   marketEntries,
			"indices":  getMarketIndices(),
			"trending": getTrendingAssets(),
		},
	})
}

// GetAssetPriceHistory returns price history for a symbol.
// GET /api/market/history/:symbol — public, days/interval params, sample data.
func (h *Handlers) GetAssetPriceHistory(c *gin.Context) {
	symbol := strings.ToUpper(c.Param("symbol"))
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	interval := c.DefaultQuery("interval", "1d")

	if days < 1 {
		days = 30
	}
	if days > 365 {
		days = 365
	}

	history, err := h.PriceService.GetHistoricalPrice(symbol, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch price history"})
		return
	}

	// Sample data based on interval
	sampled := sampleData(history, interval)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Price history retrieved successfully",
		"data": gin.H{
			"symbol":   symbol,
			"days":     days,
			"interval": interval,
			"history":  sampled,
		},
	})
}

// GetAssetMarketDetails returns detailed market data for a symbol.
// GET /api/market/assets/:symbol — public, market data + mock news + similar assets.
func (h *Handlers) GetAssetMarketDetails(c *gin.Context) {
	symbol := strings.ToUpper(c.Param("symbol"))

	marketData, err := h.PriceService.GetMarketData(symbol)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Asset not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Asset details retrieved successfully",
		"data": gin.H{
			"marketData": marketData,
			"news":       getAssetNews(symbol),
			"similar":    getSimilarAssets(symbol),
		},
	})
}

// SearchAssets searches for assets by query string.
// GET /api/market/search/:query — public, mock search from predefined list.
func (h *Handlers) SearchAssets(c *gin.Context) {
	query := strings.ToUpper(c.Param("query"))

	allAssets := []gin.H{
		{"symbol": "BTC", "name": "Bitcoin", "type": "token"},
		{"symbol": "ETH", "name": "Ethereum", "type": "token"},
		{"symbol": "SOL", "name": "Solana", "type": "token"},
		{"symbol": "MATIC", "name": "Polygon", "type": "token"},
		{"symbol": "USDT", "name": "Tether", "type": "stablecoin"},
		{"symbol": "USDC", "name": "USD Coin", "type": "stablecoin"},
		{"symbol": "BNB", "name": "BNB", "type": "token"},
		{"symbol": "XRP", "name": "XRP", "type": "token"},
		{"symbol": "ADA", "name": "Cardano", "type": "token"},
		{"symbol": "DOGE", "name": "Dogecoin", "type": "token"},
		{"symbol": "DOT", "name": "Polkadot", "type": "token"},
		{"symbol": "AVAX", "name": "Avalanche", "type": "token"},
		{"symbol": "LINK", "name": "Chainlink", "type": "token"},
		{"symbol": "UNI", "name": "Uniswap", "type": "token"},
		{"symbol": "MCGP", "name": "MCGP Token", "type": "token"},
		{"symbol": "DAI", "name": "Dai", "type": "stablecoin"},
		{"symbol": "PAXG", "name": "PAX Gold", "type": "gold-backed"},
		{"symbol": "XAUT", "name": "Tether Gold", "type": "gold-backed"},
	}

	results := make([]gin.H, 0)
	for _, a := range allAssets {
		sym := a["symbol"].(string)
		name := strings.ToUpper(a["name"].(string))
		if strings.Contains(sym, query) || strings.Contains(name, query) {
			results = append(results, a)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Search results retrieved successfully",
		"data": gin.H{
			"query":   query,
			"results": results,
			"count":   len(results),
		},
	})
}

// GetWatchlist returns the user's watchlist (user assets as watchlist).
// GET /api/market/watchlist — auth.
func (h *Handlers) GetWatchlist(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var assets []models.Asset
	if err := config.DB.Where("user_id = ?", user.ID).Find(&assets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch watchlist"})
		return
	}

	// Fetch current prices for watchlist assets
	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		symbols = append(symbols, a.Symbol)
	}

	watchlist := make([]gin.H, 0, len(assets))
	if len(symbols) > 0 {
		prices, _ := h.PriceService.GetPrices(symbols)
		for _, a := range assets {
			entry := gin.H{
				"symbol":  a.Symbol,
				"name":    a.Name,
				"balance": a.Balance,
			}
			if prices != nil {
				if pd, ok := prices[a.Symbol]; ok {
					entry["price"] = pd.USD
					entry["change24h"] = pd.USD24hChange
					entry["usdValue"] = a.Balance * pd.USD
				}
			}
			watchlist = append(watchlist, entry)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Watchlist retrieved successfully",
		"data": gin.H{
			"watchlist": watchlist,
			"count":     len(watchlist),
		},
	})
}

// AddToWatchlist adds an asset to the user's watchlist.
// POST /api/market/watchlist — auth.
func (h *Handlers) AddToWatchlist(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var body struct {
		Symbol string `json:"symbol" binding:"required"`
		Name   string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Symbol is required"})
		return
	}

	symbol := strings.ToUpper(body.Symbol)
	name := body.Name
	if name == "" {
		name = getAssetName(symbol)
	}

	// Check if already exists
	var count int64
	if err := config.DB.Model(&models.Asset{}).Where("user_id = ? AND symbol = ?", user.ID, symbol).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to check watchlist"})
		return
	}
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Asset already in watchlist"})
		return
	}

	now := time.Now()
	asset := models.Asset{
		ID:        uuid.New(),
		UserID:    user.ID,
		Symbol:    symbol,
		Name:      name,
		Balance:   0,
		USDValue:  0,
		CreatedAt: now,
		UpdatedAt: now,
	}
	asset.SetDetails(&models.AssetDetails{
		Type:  models.AssetTypeToken,
		Chain: models.ChainEthereum,
	})

	if err := config.DB.Create(&asset).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to add to watchlist"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Asset added to watchlist successfully",
		"data":    asset,
	})
}

// --- Helper functions ---

// getMarketIndices returns mock market index data.
func getMarketIndices() []gin.H {
	return []gin.H{
		{"name": "Crypto Market Cap", "value": 2.45e12, "change": 1.25},
		{"name": "BTC Dominance", "value": 52.3, "change": -0.15},
		{"name": "ETH Dominance", "value": 17.8, "change": 0.32},
		{"name": "DeFi TVL", "value": 89.5e9, "change": 2.1},
		{"name": "Fear & Greed Index", "value": 65, "change": 3},
	}
}

// getTrendingAssets returns mock trending asset data.
func getTrendingAssets() []gin.H {
	return []gin.H{
		{"symbol": "MCGP", "name": "MCGP Token", "change24h": 15.5, "volume": 1.2e9},
		{"symbol": "ETH", "name": "Ethereum", "change24h": 5.2, "volume": 18.5e9},
		{"symbol": "SOL", "name": "Solana", "change24h": 8.3, "volume": 3.2e9},
		{"symbol": "MATIC", "name": "Polygon", "change24h": 4.7, "volume": 890e6},
	}
}

// getAssetNews returns mock news for a symbol.
func getAssetNews(symbol string) []gin.H {
	return []gin.H{
		{
			"title":     symbol + " sees increased institutional adoption",
			"source":    "CryptoNews",
			"url":       "https://example.com/news/1",
			"timestamp": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			"sentiment": "positive",
		},
		{
			"title":     "Market analysis: " + symbol + " price prediction for 2025",
			"source":    "CoinDesk",
			"url":       "https://example.com/news/2",
			"timestamp": time.Now().Add(-5 * time.Hour).Format(time.RFC3339),
			"sentiment": "neutral",
		},
		{
			"title":     symbol + " network upgrade scheduled for next month",
			"source":    "The Block",
			"url":       "https://example.com/news/3",
			"timestamp": time.Now().Add(-12 * time.Hour).Format(time.RFC3339),
			"sentiment": "positive",
		},
	}
}

// getSimilarAssets returns a predefined list of similar assets for a given symbol.
func getSimilarAssets(symbol string) []gin.H {
	similarMap := map[string][]string{
		"BTC":   {"ETH", "SOL", "BNB"},
		"ETH":   {"BTC", "SOL", "AVAX"},
		"SOL":   {"ETH", "AVAX", "MATIC"},
		"MATIC": {"SOL", "AVAX", "DOT"},
		"USDT":  {"USDC", "DAI", "BUSD"},
		"USDC":  {"USDT", "DAI", "BUSD"},
		"BNB":   {"ETH", "SOL", "AVAX"},
		"MCGP":  {"ETH", "SOL", "MATIC"},
	}

	similar, ok := similarMap[symbol]
	if !ok {
		similar = []string{"BTC", "ETH", "SOL"}
	}

	result := make([]gin.H, 0, len(similar))
	for _, s := range similar {
		result = append(result, gin.H{
			"symbol": s,
			"name":   getAssetName(s),
		})
	}
	return result
}

// getAssetName maps a symbol to its full name.
func getAssetName(symbol string) string {
	names := map[string]string{
		"BTC":  "Bitcoin",
		"ETH":  "Ethereum",
		"SOL":  "Solana",
		"MATIC": "Polygon",
		"USDT": "Tether",
		"USDC": "USD Coin",
		"BNB":  "BNB",
		"XRP":  "XRP",
		"ADA":  "Cardano",
		"DOGE": "Dogecoin",
		"DOT":  "Polkadot",
		"AVAX": "Avalanche",
		"LINK": "Chainlink",
		"UNI":  "Uniswap",
		"MCGP": "MCGP Token",
		"DAI":  "Dai",
		"PAXG": "PAX Gold",
		"XAUT": "Tether Gold",
	}
	if name, ok := names[symbol]; ok {
		return name
	}
	return symbol
}

// sampleData downsamples price history based on the requested interval.
func sampleData(data []services.PricePoint, interval string) []services.PricePoint {
	if len(data) == 0 {
		return data
	}

	var step int
	switch interval {
	case "1h":
		step = 1
	case "4h":
		step = 4
	case "1d":
		step = 24
	case "1w":
		step = 168
	default:
		step = 1
	}

	if step <= 1 {
		return data
	}

	// Estimate points per hour — if data has fewer points than hours, adjust
	totalPoints := len(data)
	sampleSize := int(math.Max(1, float64(totalPoints)/float64(step)))

	if sampleSize >= totalPoints {
		return data
	}

	result := make([]services.PricePoint, 0, sampleSize)
	stepSize := float64(totalPoints) / float64(sampleSize)
	for i := 0; i < sampleSize; i++ {
		idx := int(float64(i) * stepSize)
		if idx >= totalPoints {
			idx = totalPoints - 1
		}
		result = append(result, data[idx])
	}

	// Always include the last point
	if len(result) > 0 && result[len(result)-1].Timestamp != data[totalPoints-1].Timestamp {
		result = append(result, data[totalPoints-1])
	}

	return result
}
