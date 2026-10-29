package jobs

import (
	"context"
	"log"
	"time"
)

// UnresolvedDisputeAlerter is the slice of P2PTradeService the worker depends on.
type UnresolvedDisputeAlerter interface {
	AlertUnresolvedDisputes(ctx context.Context, limit int) (int, error)
}

// P2PDisputeBackstopAlerter pages operators about disputes left unresolved, and again before the
// escrow's backstop lets anyone return the crypto to the seller. The service dedupes per trade.
type P2PDisputeBackstopAlerter struct{ svc UnresolvedDisputeAlerter }

func NewP2PDisputeBackstopAlerter(svc UnresolvedDisputeAlerter) *P2PDisputeBackstopAlerter {
	return &P2PDisputeBackstopAlerter{svc: svc}
}

func (w *P2PDisputeBackstopAlerter) Name() string            { return "p2p-dispute-backstop-alerter" }
func (w *P2PDisputeBackstopAlerter) Interval() time.Duration { return time.Hour }

func (w *P2PDisputeBackstopAlerter) Run(ctx context.Context) error {
	n, err := w.svc.AlertUnresolvedDisputes(ctx, 50)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("[p2p-dispute-backstop] alerted %d disputes", n)
	}
	return nil
}
