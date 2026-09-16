package jobs

import (
	"context"
	"time"
)

// buyOrderExpirer is the slice of ITService this worker needs.
type buyOrderExpirer interface {
	ExpireStaleBuyOrders(ctx context.Context) (int64, error)
}

// BuyOrderExpiry closes unpaid MCGP buy orders that the settlement path has
// already stopped honouring.
//
// Settlement ignores an order past its claim window, but nothing closed one:
// this leg has no user-facing cancel and a new order only supersedes the
// previous one when the user opens another MCGP buy. The row therefore stayed
// in the hub reading "awaiting transfer" for an order the system had decided
// not to fulfil — the kind of disagreement between what a screen says and what
// the server believes that turns into a support ticket.
//
// Hourly against a 24h window: the sweep is a tidy-up, not a deadline, and a
// row closed up to an hour late costs nothing. ExpireStaleBuyOrders is a single
// guarded UPDATE and idempotent, so a missed tick is recovered by the next one.
type BuyOrderExpiry struct {
	it       buyOrderExpirer
	interval time.Duration
}

func NewBuyOrderExpiry(it buyOrderExpirer, interval time.Duration) *BuyOrderExpiry {
	if interval <= 0 {
		interval = time.Hour
	}
	return &BuyOrderExpiry{it: it, interval: interval}
}

func (w *BuyOrderExpiry) Name() string            { return "it-buy-order-expiry" }
func (w *BuyOrderExpiry) Interval() time.Duration { return w.interval }

func (w *BuyOrderExpiry) Run(ctx context.Context) error {
	_, err := w.it.ExpireStaleBuyOrders(ctx)
	return err
}
