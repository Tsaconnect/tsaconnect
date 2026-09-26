package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
// GET /api/market/history/:symbol — public. `points` caps the series evenly
// (the chart asks for this); without it the legacy `interval` sampling applies,
// which installed app builds rely on — it returns the same point count as before.
func (h *Handlers) GetAssetPriceHistory(c *gin.Context) {
	symbol := strings.ToUpper(c.Param("symbol"))
	days := services.SnapChartDays(clampQueryInt(c, "days", 30, 1, 365))

	history, err := h.PriceService.GetHistoricalPrice(symbol, days)
	if err != nil {
		respondPriceSeriesError(c, err, "Price history is temporarily unavailable")
		return
	}

	data := gin.H{"symbol": symbol, "days": days}
	if c.Query("points") != "" {
		points := clampQueryInt(c, "points", 120, 2, 500)
		data["points"] = points
		data["history"] = evenlySample(history, points)
	} else {
		interval := c.DefaultQuery("interval", "1d")
		data["interval"] = interval
		data["history"] = sampleData(history, interval)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Price history retrieved successfully",
		"data":    data,
	})
}

// GetAssetOHLC returns OHLC candles for a symbol.
// GET /api/market/ohlc/:symbol?days=&candles= — public. days is snapped to a
// range CoinGecko serves; candles caps the bar count by merging neighbours.
func (h *Handlers) GetAssetOHLC(c *gin.Context) {
	symbol := strings.ToUpper(c.Param("symbol"))
	days := services.SnapChartDays(clampQueryInt(c, "days", 1, 1, 365))
	maxCandles := clampQueryInt(c, "candles", 60, 10, 200)

	candles, err := h.PriceService.GetOHLC(symbol, days)
	if err != nil {
		respondPriceSeriesError(c, err, "Candles are temporarily unavailable")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Candles retrieved successfully",
		"data": gin.H{
			"symbol":  symbol,
			"days":    days,
			"candles": services.AggregateCandles(candles, maxCandles),
		},
	})
}

func respondPriceSeriesError(c *gin.Context, err error, message string) {
	if errors.Is(err, services.ErrUnknownSymbol) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "No price data for this asset"})
		return
	}
	// Anything else is CoinGecko failing (throttling, outage) with no usable cached
	// copy: temporary, so 503 tells the app to offer a retry rather than "no data".
	c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": message})
}

// clampQueryInt reads an int query param. Absent, unparseable or below-lo values
// fall back to def (so ?days=0 still means the 30-day default); above-hi values
// are capped at hi.
func clampQueryInt(c *gin.Context, key string, def, lo, hi int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil || v < lo {
		return def
	}
	return min(v, hi)
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
	var step int
	switch interval {
	case "4h":
		step = 4
	case "1d":
		step = 24
	case "1w":
		step = 168
	default:
		return data
	}
	return evenlySample(data, max(1, len(data)/step)+1)
}

// evenlySample picks n points spread evenly across data, always keeping the
// first and last so a chart spans the whole requested range.
func evenlySample(data []services.PricePoint, n int) []services.PricePoint {
	if n <= 1 || len(data) <= n {
		return data
	}
	out := make([]services.PricePoint, n)
	last := len(data) - 1
	for i := range out {
		out[i] = data[i*last/(n-1)]
	}
	return out
}
