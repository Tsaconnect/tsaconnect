package services

import (
	"context"
	"fmt"
)

// NewP2PFxProvider adapts the P2P exchange-rate service to the IT FxProvider so
// Instant Transfer prices MCGP against the same NGN/USD value other P2P assets
// quote against. It uses the display mid rate and propagates staleness; when the
// feed is beyond its serve-age ceiling GetSnapshot errors and IT prices block.
func NewP2PFxProvider(p2p *P2PService) FxProvider { return p2pFxProvider{p2p: p2p} }

type p2pFxProvider struct{ p2p *P2PService }

func (a p2pFxProvider) NgnPerUsd(ctx context.Context) (float64, bool, error) {
	snap, err := a.p2p.GetSnapshot()
	if err != nil {
		return 0, false, err
	}
	row, ok := snap.Rates["NGN"]
	if !ok || row.MidRate <= 0 {
		return 0, false, fmt.Errorf("NGN rate unavailable")
	}
	return row.MidRate, snap.IsStale() || row.Stale, nil
}
