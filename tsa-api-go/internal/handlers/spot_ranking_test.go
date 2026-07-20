package handlers

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// rankRow builds a spotListingResponse for ranking tests. mcap/vol <= 0 → nil
// (symbol has no market data).
func rankRow(symbol string, displayOrder int, pin *int, mcap, vol float64) spotListingResponse {
	r := spotListingResponse{
		SpotListing: models.SpotListing{
			BaseSymbol:     symbol,
			QuoteSymbol:    "USDC",
			Chain:          "sonic",
			DisplayOrder:   displayOrder,
			PinnedPosition: pin,
		},
	}
	if mcap > 0 {
		r.MarketCap = &mcap
	}
	if vol > 0 {
		r.Volume24h = &vol
	}
	return r
}

func pin(p int) *int { return &p }

func symbolsOf(rows []spotListingResponse) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.BaseSymbol)
	}
	return out
}

func assertOrder(t *testing.T, got []spotListingResponse, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got %v", len(got), len(want), symbolsOf(got))
	}
	for i, sym := range want {
		if got[i].BaseSymbol != sym {
			t.Fatalf("position %d = %s, want %s; full order %v", i+1, got[i].BaseSymbol, sym, symbolsOf(got))
		}
	}
}

func TestRankSpotListings_DefaultPinsVolumeGroupAndDynamicRest(t *testing.T) {
	rows := []spotListingResponse{
		rankRow("ADA", 1, nil, 30, 5),
		rankRow("AVAX", 2, nil, 50, 8),
		rankRow("BNB", 3, nil, 900, 100),
		rankRow("USDT", 4, nil, 800, 500),
		rankRow("SOL", 5, nil, 700, 300),
		rankRow("XRP", 6, nil, 600, 200),
		rankRow("ETH", 7, nil, 2000, 900),
		rankRow("BTC", 8, nil, 3000, 950),
		rankRow("MCGP", 9, nil, 0, 0), // no market data — pinned regardless
	}
	got := rankSpotListings(rows)
	// MCGP/BTC/ETH default pins; {BNB,USDT,SOL,XRP} by volume desc; rest by mcap desc.
	assertOrder(t, got, []string{"MCGP", "BTC", "ETH", "USDT", "SOL", "XRP", "BNB", "AVAX", "ADA"})
}

func TestRankSpotListings_ExplicitPinDisplacesDefault(t *testing.T) {
	rows := []spotListingResponse{
		rankRow("MCGP", 1, nil, 0, 0),
		rankRow("BTC", 2, nil, 3000, 950), // default slot 2 taken by ADA → BTC joins dynamic rest
		rankRow("ETH", 3, nil, 2000, 900),
		rankRow("USDT", 4, nil, 800, 500),
		rankRow("SOL", 5, nil, 700, 300),
		rankRow("XRP", 6, nil, 600, 200),
		rankRow("BNB", 7, nil, 900, 100),
		rankRow("AVAX", 8, nil, 50, 8),
		rankRow("ADA", 9, pin(2), 30, 5),
	}
	got := rankSpotListings(rows)
	assertOrder(t, got, []string{"MCGP", "ADA", "ETH", "USDT", "SOL", "XRP", "BNB", "BTC", "AVAX"})
}

func TestRankSpotListings_PinConflictLowerDisplayOrderWins(t *testing.T) {
	rows := []spotListingResponse{
		rankRow("AVAX", 2, pin(1), 50, 8),
		rankRow("ADA", 1, pin(1), 30, 5), // same pin, lower display_order → wins slot 1
		rankRow("BTC", 3, nil, 3000, 950),
	}
	got := rankSpotListings(rows)
	// ADA takes slot 1; AVAX falls to the dynamic pool; BTC default-pins to 2.
	assertOrder(t, got, []string{"ADA", "BTC", "AVAX"})
}

func TestRankSpotListings_NoMarketDataFallsBackToDisplayOrder(t *testing.T) {
	rows := []spotListingResponse{
		rankRow("SOL", 20, nil, 0, 0),
		rankRow("BNB", 10, nil, 0, 0),
		rankRow("PEPE", 2, nil, 0, 0),
		rankRow("DOGE", 1, nil, 0, 0),
		rankRow("MCGP", 30, nil, 0, 0),
	}
	got := rankSpotListings(rows)
	// MCGP pinned; volume group (no data) by display_order; rest by display_order.
	assertOrder(t, got, []string{"MCGP", "BNB", "SOL", "DOGE", "PEPE"})
}

func TestRankSpotListings_PinBeyondListLengthAppends(t *testing.T) {
	rows := []spotListingResponse{
		rankRow("ADA", 1, pin(50), 30, 5),
		rankRow("AVAX", 2, nil, 50, 8),
	}
	got := rankSpotListings(rows)
	assertOrder(t, got, []string{"AVAX", "ADA"})
}
