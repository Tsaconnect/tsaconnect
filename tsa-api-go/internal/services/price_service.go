package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// PriceData holds current price information for a coin.
type PriceData struct {
	USD            float64 `json:"usd"`
	USD24hChange   float64 `json:"usd_24h_change"`
	USDMarketCap   float64 `json:"usd_market_cap"`
	USD24hVol      float64 `json:"usd_24h_vol"`
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

// Candle is one OHLC bar. Timestamp is the bar's close time in ms, as CoinGecko reports it.
type Candle struct {
	Timestamp int64   `json:"timestamp"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
}

// ErrUnknownSymbol means the symbol has no CoinGecko mapping, so there is no data to fetch.
var ErrUnknownSymbol = errors.New("unknown symbol")

const coinGeckoBaseURL = "https://api.coingecko.com/api/v3"

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
	baseURL    string

	// Chart series (history, OHLC) change slowly and every token screen asks for
	// them, so they are cached per symbol+range to stay inside CoinGecko's free tier.
	seriesCache  sync.Map
	seriesFlight singleflight.Group // one upstream call per key, however many requests miss at once
	now          func() time.Time
}

type seriesEntry struct {
	value     any
	fetchedAt time.Time
}

// seriesTTL is how long a chart series is served before CoinGecko is asked
// again. Longer ranges move less per minute, so they can be cached longer.
func seriesTTL(days int) time.Duration {
	switch {
	case days <= 1:
		return 5 * time.Minute
	case days <= 7:
		return 15 * time.Minute
	case days <= 30:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

// staleFactor bounds how long an expired series may stand in while CoinGecko
// is failing (throttling, outage): 12 TTLs is an hour for 24H, a day for 1Y.
const staleFactor = 12

// NewPriceService creates a new PriceService with default configuration.
func NewPriceService() *PriceService {
	return &PriceService{
		coinIDs: map[string]string{
			"S":     "sonic-3",
			"tBNB":  "binancecoin",
			"USDT":  "tether",
			"USDC":  "usd-coin",
			"USDG":  "global-dollar",
			"ETH":   "ethereum",
			"BTC":   "bitcoin",
			"SOL":   "solana",
			"TRX":   "tron",
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
			"POL":   "polygon-ecosystem-token",
			"SUI":   "sui",
			"WBTC":  "wrapped-bitcoin",
			"WETH":  "weth",
		},
		httpClient: &http.Client{Timeout: 10 * time.Second},
		cacheTTL:   60 * time.Second,
		baseURL:    coinGeckoBaseURL,
		now:        time.Now,
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
		"%s/simple/price?ids=%s&vs_currencies=usd&include_24hr_change=true&include_market_cap=true&include_24hr_vol=true&include_last_updated_at=true",
		ps.baseURL, strings.Join(ids, ","),
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
			if v, ok := data["usd_24h_vol"].(float64); ok {
				pd.USD24hVol = v
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

// SeedPriceForTest pre-loads the price cache for a single symbol so test code
// (and offline/dev paths) can resolve a deterministic price without a network
// call. The cache key mirrors GetPrices([]string{symbol}) — the lone symbol —
// and the entry is far-future so it never expires mid-test. Test-only helper.
func (ps *PriceService) SeedPriceForTest(symbol string, pd PriceData) {
	ps.cache.Store(symbol, &cacheEntry{
		data:      map[string]PriceData{symbol: pd},
		expiresAt: time.Now().Add(24 * time.Hour),
	})
}

// SetBaseURLForTest points the service at a fake CoinGecko. Test-only helper.
func (ps *PriceService) SetBaseURLForTest(url string) {
	ps.baseURL = url
}

// GetHistoricalPrice fetches historical price data for a symbol. days is snapped
// up to a chart range (see SnapChartDays) so the cache has a handful of keys per
// coin rather than 365 — the endpoint is public and each miss costs a CoinGecko call.
func (ps *PriceService) GetHistoricalPrice(symbol string, days int) ([]PricePoint, error) {
	coinID, err := ps.coinID(symbol)
	if err != nil {
		return nil, err
	}
	days = SnapChartDays(days)
	key := fmt.Sprintf("history:%s:%d", coinID, days)
	return cachedSeries(ps, key, seriesTTL(days), func() ([]PricePoint, error) {
		var raw struct {
			Prices [][]float64 `json:"prices"`
		}
		url := fmt.Sprintf("%s/coins/%s/market_chart?vs_currency=usd&days=%d", ps.baseURL, coinID, days)
		if err := ps.getJSON(url, &raw); err != nil {
			return nil, fmt.Errorf("failed to fetch historical prices: %w", err)
		}
		points := make([]PricePoint, 0, len(raw.Prices))
		for _, p := range raw.Prices {
			if len(p) >= 2 {
				points = append(points, PricePoint{Timestamp: int64(p[0]), Price: p[1]})
			}
		}
		return points, nil
	})
}

// GetOHLC fetches OHLC candles for a symbol. CoinGecko only serves fixed ranges,
// so days is snapped up to the nearest one (see SnapChartDays). Bar width is set
// by CoinGecko: 30 min up to 2 days, 4 h up to 30 days, 4 days beyond.
func (ps *PriceService) GetOHLC(symbol string, days int) ([]Candle, error) {
	coinID, err := ps.coinID(symbol)
	if err != nil {
		return nil, err
	}
	days = SnapChartDays(days)
	key := fmt.Sprintf("ohlc:%s:%d", coinID, days)
	return cachedSeries(ps, key, seriesTTL(days), func() ([]Candle, error) {
		var raw [][]float64
		url := fmt.Sprintf("%s/coins/%s/ohlc?vs_currency=usd&days=%d", ps.baseURL, coinID, days)
		if err := ps.getJSON(url, &raw); err != nil {
			return nil, fmt.Errorf("failed to fetch OHLC: %w", err)
		}
		candles := make([]Candle, 0, len(raw))
		for _, r := range raw {
			if len(r) >= 5 {
				candles = append(candles, Candle{Timestamp: int64(r[0]), Open: r[1], High: r[2], Low: r[3], Close: r[4]})
			}
		}
		return candles, nil
	})
}

// chartDayBuckets are the only `days` values CoinGecko's /ohlc endpoint accepts;
// history uses the same set so both series share a small, bounded cache key space.
var chartDayBuckets = []int{1, 7, 14, 30, 90, 180, 365}

// SnapChartDays rounds days up to the nearest chart range, capped at a year.
func SnapChartDays(days int) int {
	for _, b := range chartDayBuckets {
		if days <= b {
			return b
		}
	}
	return chartDayBuckets[len(chartDayBuckets)-1]
}

// AggregateCandles merges consecutive candles so at most maxBars remain. Groups
// are counted back from the newest bar, so only the oldest bar can be partial.
func AggregateCandles(candles []Candle, maxBars int) []Candle {
	n := len(candles)
	if maxBars <= 0 || n <= maxBars {
		return candles
	}
	size := (n + maxBars - 1) / maxBars
	out := make([]Candle, 0, maxBars)
	lead := n % size
	if lead > 0 {
		out = append(out, mergeCandles(candles[:lead]))
	}
	for lo := lead; lo < n; lo += size {
		out = append(out, mergeCandles(candles[lo:lo+size]))
	}
	return out
}

func mergeCandles(group []Candle) Candle {
	m := Candle{Timestamp: group[len(group)-1].Timestamp, Open: group[0].Open, High: group[0].High, Low: group[0].Low, Close: group[len(group)-1].Close}
	for _, c := range group[1:] {
		m.High = math.Max(m.High, c.High)
		m.Low = math.Min(m.Low, c.Low)
	}
	return m
}

func (ps *PriceService) coinID(symbol string) (string, error) {
	id, ok := ps.coinIDs[strings.ToUpper(symbol)]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownSymbol, symbol)
	}
	return id, nil
}

func (ps *PriceService) getJSON(url string, out any) error {
	resp, err := ps.httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CoinGecko API returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// cachedSeries returns the cached value for key, or fetches and caches it.
// Concurrent misses on one key share a single fetch. Failures are not cached;
// while CoinGecko is failing, the last good value is served for up to
// staleFactor TTLs so a throttled server still draws charts.
func cachedSeries[T any](ps *PriceService, key string, ttl time.Duration, fetch func() (T, error)) (T, error) {
	var stale *seriesEntry
	if entry, ok := ps.seriesCache.Load(key); ok {
		stale = entry.(*seriesEntry)
		if ps.now().Before(stale.fetchedAt.Add(ttl)) {
			return stale.value.(T), nil
		}
	}
	v, err, _ := ps.seriesFlight.Do(key, func() (any, error) {
		v, err := fetch()
		if err == nil {
			ps.seriesCache.Store(key, &seriesEntry{value: v, fetchedAt: ps.now()})
			return v, nil
		}
		if stale != nil && ps.now().Before(stale.fetchedAt.Add(staleFactor*ttl)) {
			log.Printf("[price] %s: CoinGecko failed, serving copy from %s ago: %v",
				key, ps.now().Sub(stale.fetchedAt).Round(time.Second), err)
			return stale.value, nil
		}
		return nil, err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// GetMarketData fetches comprehensive market data for a symbol.
func (ps *PriceService) GetMarketData(symbol string) (*MarketData, error) {
	upper := strings.ToUpper(symbol)
	coinID, ok := ps.coinIDs[upper]
	if !ok {
		return nil, fmt.Errorf("unknown symbol: %s", symbol)
	}

	url := fmt.Sprintf(
		"%s/coins/markets?vs_currency=usd&ids=%s&order=market_cap_desc&sparkline=false",
		ps.baseURL, coinID,
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
