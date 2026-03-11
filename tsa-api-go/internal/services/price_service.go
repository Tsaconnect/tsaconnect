package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PriceData holds current price information for a coin.
type PriceData struct {
	USD            float64 `json:"usd"`
	USD24hChange   float64 `json:"usd_24h_change"`
	USDMarketCap   float64 `json:"usd_market_cap"`
	LastUpdatedAt  int64   `json:"last_updated_at"`
}

// PricePoint represents a single historical price data point.
type PricePoint struct {
	Timestamp int64   `json:"timestamp"`
	Price     float64 `json:"price"`
}

// MarketData holds comprehensive market data for a coin.
type MarketData struct {
	Symbol                   string  `json:"symbol"`
	Name                     string  `json:"name"`
	CurrentPrice             float64 `json:"current_price"`
	PriceChange24h           float64 `json:"price_change_24h"`
	PriceChangePercentage24h float64 `json:"price_change_percentage_24h"`
	MarketCap                float64 `json:"market_cap"`
	TotalVolume              float64 `json:"total_volume"`
	High24h                  float64 `json:"high_24h"`
	Low24h                   float64 `json:"low_24h"`
	CirculatingSupply        float64 `json:"circulating_supply"`
	TotalSupply              float64 `json:"total_supply"`
	MaxSupply                float64 `json:"max_supply"`
	ATH                      float64 `json:"ath"`
	ATHChangePercentage      float64 `json:"ath_change_percentage"`
	ATHDate                  string  `json:"ath_date"`
	ATL                      float64 `json:"atl"`
	ATLChangePercentage      float64 `json:"atl_change_percentage"`
	ATLDate                  string  `json:"atl_date"`
	LastUpdated              string  `json:"last_updated"`
}

type cacheEntry struct {
	data      map[string]PriceData
	expiresAt time.Time
}

// PriceService fetches cryptocurrency price data from CoinGecko.
type PriceService struct {
	coinIDs    map[string]string
	cache      sync.Map
	httpClient *http.Client
	cacheTTL   time.Duration
}

// NewPriceService creates a new PriceService with default configuration.
func NewPriceService() *PriceService {
	return &PriceService{
		coinIDs: map[string]string{
			"MCGP":  "maticgold-pro",
			"USDT":  "tether",
			"USDC":  "usd-coin",
			"ETH":   "ethereum",
			"BTC":   "bitcoin",
			"SOL":   "solana",
			"MATIC": "matic-network",
			"BNB":   "binancecoin",
			"PAXG":  "pax-gold",
			"XAUT":  "tether-gold",
			"GOLD":  "gold",
			"DAI":   "dai",
			"BUSD":  "binance-usd",
			"AVAX":  "avalanche-2",
			"ADA":   "cardano",
			"XRP":   "ripple",
		},
		httpClient: &http.Client{Timeout: 10 * time.Second},
		cacheTTL:   60 * time.Second,
	}
}

// GetPrices fetches current prices for the given symbols from CoinGecko,
// using a 60-second TTL cache.
func (ps *PriceService) GetPrices(symbols []string) (map[string]PriceData, error) {
	// Build cache key from sorted symbols
	cacheKey := strings.Join(symbols, ",")

	// Check cache
	if entry, ok := ps.cache.Load(cacheKey); ok {
		ce := entry.(*cacheEntry)
		if time.Now().Before(ce.expiresAt) {
			return ce.data, nil
		}
		ps.cache.Delete(cacheKey)
	}

	// Resolve CoinGecko IDs
	var ids []string
	symbolToID := make(map[string]string)
	for _, sym := range symbols {
		upper := strings.ToUpper(sym)
		if id, ok := ps.coinIDs[upper]; ok {
			ids = append(ids, id)
			symbolToID[upper] = id
		}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("no valid symbols provided")
	}

	url := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=usd&include_24hr_change=true&include_market_cap=true&include_last_updated_at=true",
		strings.Join(ids, ","),
	)

	resp, err := ps.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch prices: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinGecko API returned status %d", resp.StatusCode)
	}

	var raw map[string]map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode price response: %w", err)
	}

	// Map back to symbols
	result := make(map[string]PriceData)
	for sym, coinID := range symbolToID {
		if data, ok := raw[coinID]; ok {
			pd := PriceData{}
			if v, ok := data["usd"].(float64); ok {
				pd.USD = v
			}
			if v, ok := data["usd_24h_change"].(float64); ok {
				pd.USD24hChange = v
			}
			if v, ok := data["usd_market_cap"].(float64); ok {
				pd.USDMarketCap = v
			}
			if v, ok := data["last_updated_at"].(float64); ok {
				pd.LastUpdatedAt = int64(v)
			}
			result[sym] = pd
		}
	}

	// Store in cache
	ps.cache.Store(cacheKey, &cacheEntry{
		data:      result,
		expiresAt: time.Now().Add(ps.cacheTTL),
	})

	return result, nil
}

// GetHistoricalPrice fetches historical price data for a symbol over the specified number of days.
func (ps *PriceService) GetHistoricalPrice(symbol string, days int) ([]PricePoint, error) {
	upper := strings.ToUpper(symbol)
	coinID, ok := ps.coinIDs[upper]
	if !ok {
		return nil, fmt.Errorf("unknown symbol: %s", symbol)
	}

	url := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/%s/market_chart?vs_currency=usd&days=%d",
		coinID, days,
	)

	resp, err := ps.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch historical prices: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinGecko API returned status %d", resp.StatusCode)
	}

	var raw struct {
		Prices [][]float64 `json:"prices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode historical price response: %w", err)
	}

	points := make([]PricePoint, 0, len(raw.Prices))
	for _, p := range raw.Prices {
		if len(p) >= 2 {
			points = append(points, PricePoint{
				Timestamp: int64(p[0]),
				Price:     p[1],
			})
		}
	}

	return points, nil
}

// GetMarketData fetches comprehensive market data for a symbol.
func (ps *PriceService) GetMarketData(symbol string) (*MarketData, error) {
	upper := strings.ToUpper(symbol)
	coinID, ok := ps.coinIDs[upper]
	if !ok {
		return nil, fmt.Errorf("unknown symbol: %s", symbol)
	}

	url := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/markets?vs_currency=usd&ids=%s&order=market_cap_desc&sparkline=false",
		coinID,
	)

	resp, err := ps.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch market data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinGecko API returned status %d", resp.StatusCode)
	}

	var raw []struct {
		Symbol                   string  `json:"symbol"`
		Name                     string  `json:"name"`
		CurrentPrice             float64 `json:"current_price"`
		PriceChange24h           float64 `json:"price_change_24h"`
		PriceChangePercentage24h float64 `json:"price_change_percentage_24h"`
		MarketCap                float64 `json:"market_cap"`
		TotalVolume              float64 `json:"total_volume"`
		High24h                  float64 `json:"high_24h"`
		Low24h                   float64 `json:"low_24h"`
		CirculatingSupply        float64 `json:"circulating_supply"`
		TotalSupply              float64 `json:"total_supply"`
		MaxSupply               float64 `json:"max_supply"`
		ATH                      float64 `json:"ath"`
		ATHChangePercentage      float64 `json:"ath_change_percentage"`
		ATHDate                  string  `json:"ath_date"`
		ATL                      float64 `json:"atl"`
		ATLChangePercentage      float64 `json:"atl_change_percentage"`
		ATLDate                  string  `json:"atl_date"`
		LastUpdated              string  `json:"last_updated"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode market data response: %w", err)
	}

	if len(raw) == 0 {
		return nil, fmt.Errorf("no market data found for %s", symbol)
	}

	d := raw[0]
	return &MarketData{
		Symbol:                   d.Symbol,
		Name:                     d.Name,
		CurrentPrice:             d.CurrentPrice,
		PriceChange24h:           d.PriceChange24h,
		PriceChangePercentage24h: d.PriceChangePercentage24h,
		MarketCap:                d.MarketCap,
		TotalVolume:              d.TotalVolume,
		High24h:                  d.High24h,
		Low24h:                   d.Low24h,
		CirculatingSupply:        d.CirculatingSupply,
		TotalSupply:              d.TotalSupply,
		MaxSupply:                d.MaxSupply,
		ATH:                      d.ATH,
		ATHChangePercentage:      d.ATHChangePercentage,
		ATHDate:                  d.ATHDate,
		ATL:                      d.ATL,
		ATLChangePercentage:      d.ATLChangePercentage,
		ATLDate:                  d.ATLDate,
		LastUpdated:              d.LastUpdated,
	}, nil
}
