package jobs

import (
	"context"
	"time"
)

// unpaidOrderExpirer is the slice of UnpaidOrderExpiryService this worker needs.
type unpaidOrderExpirer interface {
	ExpireUnpaidOrders(ctx context.Context) (int64, error)
}

// UnpaidOrderExpiry cancels marketplace orders nobody paid for and returns
// their stock (see services.UnpaidOrderExpiryService). Every 10 minutes: a
// held 1-in-stock listing is unbuyable for everyone else while it waits, so
// this runs well inside the one-hour TTL. Each pass is idempotent.
type UnpaidOrderExpiry struct {
	svc      unpaidOrderExpirer
	interval time.Duration
}

func NewUnpaidOrderExpiry(svc unpaidOrderExpirer, interval time.Duration) *UnpaidOrderExpiry {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &UnpaidOrderExpiry{svc: svc, interval: interval}
}

func (w *UnpaidOrderExpiry) Name() string            { return "unpaid-order-expiry" }
func (w *UnpaidOrderExpiry) Interval() time.Duration { return w.interval }

func (w *UnpaidOrderExpiry) Run(ctx context.Context) error {
	_, err := w.svc.ExpireUnpaidOrders(ctx)
	return err
}
