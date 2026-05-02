package services

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────
// Data types
// ──────────────────────────────────────────────

// P2PRate holds the exchange rate for a single fiat currency.
// Field names kept for backward compatibility with frontend.
type P2PRate struct {
	Currency   string  `json:"currency"`
	Symbol     string  `json:"symbol"`
	BinanceBuy float64 `json:"binanceBuy"`    // Rate from Open Exchange Rates
	BinanceSell float64 `json:"binanceSell"`  // Same as buy (mid-market rate)
	MidRate    float64 `json:"midRate"`       // Rate for display / conversion
	UpdatedAt  int64   `json:"updatedAt"`
}

// P2PRatesResponse is the API response shape.
type P2PRatesResponse struct {
	Base      string             `json:"base"`      // "USD"
	Rates     map[string]P2PRate `json:"rates"`
	UpdatedAt int64              `json:"updatedAt"`
}

// ──────────────────────────────────────────────
// Open Exchange Rates API response
// ──────────────────────────────────────────────

type oerResponse struct {
	Disclaimer string             `json:"disclaimer"`
	License    string             `json:"license"`
	Timestamp  int64              `json:"timestamp"`
	Base       string             `json:"base"`
	Rates      map[string]float64 `json:"rates"`
}

// ──────────────────────────────────────────────
// Service
// ──────────────────────────────────────────────

// P2PService fetches exchange rates from Open Exchange Rates API.
// Results are cached for 1 hour to stay within the free tier's 1,000 req/month limit.
type P2PService struct {
	HTTPClient          *http.Client
	cache               sync.Map
	cacheTTL            time.Duration
	AppID               string
	SupportedCurrencies []string

	// Monthly request tracking (best-effort, in-memory)
	mu          sync.Mutex
	reqCount    int
	reqCountResets time.Time
}

// NewP2PService creates a new P2PService.
//
//	AppID               — Open Exchange Rates app_id
//	supportedCurrencies — list of currency codes to include (e.g. ["NGN","GHS","KES"])
func NewP2PService(appID string, supportedCurrencies []string) *P2PService {
	return &P2PService{
		HTTPClient:          &http.Client{Timeout: 15 * time.Second},
		cacheTTL:            1 * time.Hour, // ~24 reqs/day = ~720/month, well under 1k
	AppID:                appID,
		SupportedCurrencies: supportedCurrencies,
		reqCountResets:      time.Now(),
	}
}

// ──────────────────────────────────────────────
// Public API
// ──────────────────────────────────────────────

// GetAllRates fetches exchange rates for all supported currencies.
// Results are cached for 1 hour. Returns the full rates map.
func (ps *P2PService) GetAllRates() (map[string]P2PRate, error) {
	// Check cache first
	if entry, ok := ps.cache.Load("all"); ok {
		ce := entry.(*p2pCacheEntry)
		if time.Now().Before(ce.expiresAt) {
			return ce.rates, nil
		}
		ps.cache.Delete("all")
	}

	// Check monthly budget before fetching
	if !ps.canMakeRequest() {
		log.Printf("[P2PService] Monthly request budget exceeded (~%d used), serving cached or fallback", ps.reqCount)
		// Try cache even if expired
		if entry, ok := ps.cache.Load("all"); ok {
			return entry.(*p2pCacheEntry).rates, nil
		}
		return nil, fmt.Errorf("monthly API request budget exceeded and no cached data available")
	}

	rates, err := ps.fetchOERates()
	if err != nil {
		return nil, fmt.Errorf("open exchange rates: %w", err)
	}

	// Cache and return
	ps.cache.Store("all", &p2pCacheEntry{
		rates:     rates,
		expiresAt: time.Now().Add(ps.cacheTTL),
	})

	return rates, nil
}

// GetRate returns the rate for a single currency.
func (ps *P2PService) GetRate(currency string) (*P2PRate, error) {
	allRates, err := ps.GetAllRates()
	if err != nil {
		return nil, err
	}
	rate, ok := allRates[strings.ToUpper(currency)]
	if !ok {
		return nil, fmt.Errorf("unsupported currency: %s", currency)
	}
	return &rate, nil
}

// ConvertUSD converts a USD amount to the target currency.
func (ps *P2PService) ConvertUSD(usdAmount float64, toCurrency string) (float64, error) {
	rate, err := ps.GetRate(toCurrency)
	if err != nil {
		return 0, err
	}
	return usdAmount * rate.MidRate, nil
}

// ──────────────────────────────────────────────
// Open Exchange Rates API
// ──────────────────────────────────────────────

func (ps *P2PService) fetchOERates() (map[string]P2PRate, error) {
	if ps.AppID == "" {
		return nil, fmt.Errorf("Open Exchange Rates API key not configured")
	}

	url := fmt.Sprintf("https://openexchangerates.org/api/latest.json?app_id=%s", ps.AppID)

	resp, err := ps.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	ps.trackRequest()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result oerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Rates) == 0 {
		return nil, fmt.Errorf("empty rates from API")
	}

	now := time.Now().Unix()
	rates := make(map[string]P2PRate, len(ps.SupportedCurrencies)+1)

	// Always include USD at 1:1
	rates["USD"] = P2PRate{
		Currency:   "USD",
		Symbol:     "$",
		BinanceBuy: 1.0,
		BinanceSell: 1.0,
		MidRate:    1.0,
		UpdatedAt:  now,
	}

	for _, currency := range ps.SupportedCurrencies {
		if currency == "USD" {
			continue
		}

		rate, ok := result.Rates[currency]
		if !ok || rate <= 0 {
			log.Printf("[P2PService] No rate for %s from Open Exchange Rates", currency)
			continue
		}

		// Round to 2 decimal places for display
		rate = math.Round(rate*100) / 100

		rates[currency] = P2PRate{
			Currency:    currency,
			Symbol:      currencySymbol(currency),
			BinanceBuy:  rate,
			BinanceSell: rate,
			MidRate:     rate,
			UpdatedAt:   now,
		}
	}

	return rates, nil
}

// ──────────────────────────────────────────────
// Request budget tracking
// ──────────────────────────────────────────────

// canMakeRequest checks if we're within the monthly budget.
// Resets the counter on the 1st of each month.
func (ps *P2PService) canMakeRequest() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Reset counter on new month
	now := time.Now()
	if now.Year() != ps.reqCountResets.Year() || now.Month() != ps.reqCountResets.Month() {
		ps.reqCount = 0
		ps.reqCountResets = now
	}

	return ps.reqCount < 950 // Leave headroom for tests/manual refreshes
}

// trackRequest increments the monthly request counter.
func (ps *P2PService) trackRequest() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.reqCount++
	log.Printf("[P2PService] Monthly API request %d (resets at month end)", ps.reqCount)
}

// ──────────────────────────────────────────────
// Cache
// ──────────────────────────────────────────────

type p2pCacheEntry struct {
	rates     map[string]P2PRate
	expiresAt time.Time
}

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

// currencySymbol returns the common symbol for a currency code.
func currencySymbol(code string) string {
	symbols := map[string]string{
		"USD": "$",
		"NGN": "₦",
		"GHS": "GH₵",
		"KES": "KSh",
		"ZAR": "R",
		"UGX": "USh",
		"TZS": "TSh",
		"RWF": "FRw",
		"XOF": "CFA",
		"XAF": "FCFA",
		"EUR": "€",
		"GBP": "£",
		"CAD": "C$",
		"AUD": "A$",
	}
	if sym, ok := symbols[code]; ok {
		return sym
	}
	return "" // unknown — will use code prefix
}

// parseFloatSafe converts a string to float64, returning 0 on failure.
// Kept for backward compatibility (used in tests, may be useful elsewhere).
func parseFloatSafe(s string) float64 {
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0
	}
	return f
}
