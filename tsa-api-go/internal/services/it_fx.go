package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoMarketAnchor is returned when a currency has no live market feed behind
// it and pricing must not silently fall back to an official rate.
//
// The two are NOT interchangeable. For a currency with a parallel market the
// official rate can sit far from what a user can actually transact at, so
// pricing one currency off a P2P feed and another off Open Exchange Rates puts
// a systematic error into every trade in the second — in a direction that
// depends on which side of the trade the user is on. Refusing is the correct
// behaviour: the transactional stale guard already knows how to block on an
// unavailable rate, and a blocked trade is recoverable where a mispriced one is
// not.
var ErrNoMarketAnchor = errors.New("currency has no market-anchored rate")

// FxRateProvider supplies the fiat-per-USD rate for ANY supported currency.
//
// This is deliberately a separate interface from FxProvider rather than an extra
// method on it. FxProvider is consumed by bill_service.go, which is explicitly
// out of scope for multi-currency work and whose test fakes implement it —
// widening it would break both for no benefit. p2pFxProvider satisfies both, so
// IT can take the richer one while bills keeps the narrow one it needs.
type FxRateProvider interface {
	RatePerUsd(ctx context.Context, currency string) (rate float64, stale bool, err error)
}

// NewP2PFxProvider adapts the P2P exchange-rate service to the IT FxProvider so
// Instant Transfer prices MCGP against the same NGN/USD value other P2P assets
// quote against. It uses the display mid rate and propagates staleness; when the
// feed is beyond its serve-age ceiling GetSnapshot errors and IT prices block.
func NewP2PFxProvider(p2p *P2PService) FxProvider { return p2pFxProvider{p2p: p2p} }

// NewP2PFxRateProvider is the multi-currency view of the same adapter.
func NewP2PFxRateProvider(p2p *P2PService) FxRateProvider { return p2pFxProvider{p2p: p2p} }

type p2pFxProvider struct{ p2p *P2PService }

func (a p2pFxProvider) NgnPerUsd(ctx context.Context) (float64, bool, error) {
	return a.RatePerUsd(ctx, "NGN")
}

// RatePerUsd returns how many units of currency one USD buys.
//
// Anchored currencies (those routed through the P2P feed) resolve to the live
// market rate. Everything else currently resolves to nothing: see
// ErrNoMarketAnchor for why an OER fallback is refused rather than served. When
// an unanchored currency needs to be priceable, the decision to make is a
// product one — an admin-set spread over the official rate — not a silent
// substitution here.
func (a p2pFxProvider) RatePerUsd(ctx context.Context, currency string) (float64, bool, error) {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if code == "" {
		return 0, false, fmt.Errorf("%w: (empty)", ErrNoMarketAnchor)
	}

	snap, err := a.p2p.GetSnapshot()
	if err != nil {
		return 0, false, err
	}

	row, ok := snap.Rates[code]
	if !ok || row.MidRate <= 0 {
		return 0, false, fmt.Errorf("%s rate unavailable", code)
	}

	// FloatAnchor marks a currency as having a live P2P market behind it. A row
	// without one carries an official rate, which must not be used to price a
	// trade — see ErrNoMarketAnchor.
	if !row.FloatAnchor {
		return 0, false, fmt.Errorf("%w: %s is priced from the official feed, not a market", ErrNoMarketAnchor, code)
	}

	return row.MidRate, snap.IsStale() || row.Stale, nil
}
