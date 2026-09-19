package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fxSnapshot builds a provider over a canned rates snapshot.
func fxSnapshot(t *testing.T, rates map[string]P2PRate) FxRateProvider {
	t.Helper()
	svc := NewP2PService("test-app-id", []string{"NGN", "GHS", "USD"}, []string{"NGN", "GHS"}, nil)
	svc.cache.Store("all", &p2pCacheEntry{
		rates:     rates,
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(time.Hour),
	})
	// The narrow constructor's result also satisfies FxRateProvider — that
	// dual-satisfaction is what lets IT take the richer view without a second
	// adapter, so the assertion belongs in the test too.
	return NewP2PFxProvider(svc).(FxRateProvider)
}

// TestRatePerUsd_ResolvesPerCurrency pins that the currency argument is actually
// used. A provider that ignored it and always returned naira would convert cedi
// amounts at ~1400 instead of ~12 — a ~100x mispricing on every Ghanaian trade.
func TestRatePerUsd_ResolvesPerCurrency(t *testing.T) {
	fx := fxSnapshot(t, map[string]P2PRate{
		"NGN": {Currency: "NGN", MidRate: 1_388.72, FloatAnchor: true},
		"GHS": {Currency: "GHS", MidRate: 11.79, FloatAnchor: true},
	})

	for _, c := range []struct {
		currency string
		want     float64
	}{
		{"NGN", 1_388.72},
		{"GHS", 11.79},
		{"ghs", 11.79}, // normalised
	} {
		got, _, err := fx.RatePerUsd(context.Background(), c.currency)
		if err != nil {
			t.Fatalf("RatePerUsd(%s): %v", c.currency, err)
		}
		if got != c.want {
			t.Errorf("RatePerUsd(%s) = %v, want %v", c.currency, got, c.want)
		}
	}
}

// TestRatePerUsd_RefusesUnanchoredCurrency is the guard that keeps an official
// rate out of a trade. USD/EUR/GBP carry OER's official cross with no market
// behind them; serving that as if it were transactable is a systematic error in
// whichever direction the user is trading, not a rounding difference.
func TestRatePerUsd_RefusesUnanchoredCurrency(t *testing.T) {
	fx := fxSnapshot(t, map[string]P2PRate{
		"NGN": {Currency: "NGN", MidRate: 1_388.72, FloatAnchor: true},
		"GBP": {Currency: "GBP", MidRate: 0.79, FloatAnchor: false},
	})

	if _, _, err := fx.RatePerUsd(context.Background(), "GBP"); !errors.Is(err, ErrNoMarketAnchor) {
		t.Errorf("RatePerUsd(GBP) = %v, want ErrNoMarketAnchor — an unanchored currency must "+
			"block the trade rather than be priced off the official feed", err)
	}
}

// TestRatePerUsd_NgnPerUsdDelegates confirms the narrow legacy interface is a
// view onto the same resolution, so bills keep working unchanged.
func TestRatePerUsd_NgnPerUsdDelegates(t *testing.T) {
	svc := NewP2PService("test-app-id", []string{"NGN"}, []string{"NGN"}, nil)
	svc.cache.Store("all", &p2pCacheEntry{
		rates:     map[string]P2PRate{"NGN": {Currency: "NGN", MidRate: 1_400, FloatAnchor: true}},
		cachedAt:  time.Now(),
		expiresAt: time.Now().Add(time.Hour),
	})

	legacy, _, err := NewP2PFxProvider(svc).NgnPerUsd(context.Background())
	if err != nil {
		t.Fatalf("NgnPerUsd: %v", err)
	}
	multi, _, err := NewP2PFxProvider(svc).(FxRateProvider).RatePerUsd(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("RatePerUsd: %v", err)
	}
	if legacy != multi {
		t.Errorf("NgnPerUsd=%v but RatePerUsd(NGN)=%v — they must be the same resolution", legacy, multi)
	}
}

// TestRatePerUsd_StalenessIsolation is the regression test for the "Rates are
// refreshing" bug: a Bybit failure for one currency (GHS) marked that row
// Stale, and toSnapshot propagated it as snapshot-level staleness, which
// RatePerUsd then ORed into every currency's stale flag — including NGN, whose
// own data was perfectly fresh. The fix is to use only the per-row Stale flag.
func TestRatePerUsd_StalenessIsolation(t *testing.T) {
	fx := fxSnapshot(t, map[string]P2PRate{
		"NGN": {Currency: "NGN", MidRate: 1_388.72, FloatAnchor: true, Stale: false},
		"GHS": {Currency: "GHS", MidRate: 11.79, FloatAnchor: true, Stale: true}, // Bybit failure
	})

	// NGN must NOT be reported stale — its own row is fresh.
	rate, stale, err := fx.RatePerUsd(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("RatePerUsd(NGN): %v", err)
	}
	if stale {
		t.Errorf("RatePerUsd(NGN) stale=%v — a fresh NGN row must not be poisoned "+
			"by an unrelated currency's staleness", stale)
	}
	if rate != 1_388.72 {
		t.Errorf("RatePerUsd(NGN) = %v, want 1388.72", rate)
	}

	// GHS itself MUST be stale — its own row says so.
	_, ghsStale, err := fx.RatePerUsd(context.Background(), "GHS")
	if err != nil {
		t.Fatalf("RatePerUsd(GHS): %v", err)
	}
	if !ghsStale {
		t.Errorf("RatePerUsd(GHS) stale=%v — a Stale=true row must report stale", ghsStale)
	}
}
