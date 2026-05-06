//go:build manual
// +build manual

package services

import (
	"net/http"
	"testing"
	"time"
)

// TestLiveBybitNGN hits the real Bybit P2P endpoint. Skipped in CI; run manually with:
//   go test ./internal/services -tags manual -run TestLiveBybitNGN -v
func TestLiveBybitNGN(t *testing.T) {
	svc := &P2PService{
		HTTPClient:          &http.Client{Timeout: 15 * time.Second},
		SupportedCurrencies: []string{"NGN"},
		bybitCurrencies:     map[string]bool{"NGN": true},
	}

	rate, err := svc.fetchBybitRate("NGN")
	if err != nil {
		t.Fatalf("live Bybit fetch failed: %v", err)
	}
	t.Logf("LIVE NGN: buy=%.2f sell=%.2f mid=%.2f spread=%.2f",
		rate.BinanceBuy, rate.BinanceSell, rate.MidRate,
		rate.BinanceBuy-rate.BinanceSell)

	if rate.BinanceBuy < 1000 || rate.BinanceBuy > 5000 {
		t.Errorf("BinanceBuy %v outside sanity range [1000, 5000]", rate.BinanceBuy)
	}
	if rate.BinanceSell < 1000 || rate.BinanceSell > 5000 {
		t.Errorf("BinanceSell %v outside sanity range [1000, 5000]", rate.BinanceSell)
	}
}
