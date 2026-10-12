package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// StalePaidAlerter is the slice of P2PTradeService the worker depends on.
type StalePaidAlerter interface {
	AlertStalePaidTrades(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}

// P2PPaidUnreleasedAlerter pages operators about trades the buyer marked paid
// that the seller has not released. The service dedupes per trade.
type P2PPaidUnreleasedAlerter struct {
	svc   StalePaidAlerter
	after time.Duration
}

func NewP2PPaidUnreleasedAlerter(svc StalePaidAlerter, after time.Duration) *P2PPaidUnreleasedAlerter {
	if after <= 0 {
		after = config.DefaultP2PPaidUnreleasedAlertAfter
	}
	return &P2PPaidUnreleasedAlerter{svc: svc, after: after}
}

func (w *P2PPaidUnreleasedAlerter) Name() string            { return "p2p-paid-unreleased-alerter" }
func (w *P2PPaidUnreleasedAlerter) Interval() time.Duration { return 5 * time.Minute }

func (w *P2PPaidUnreleasedAlerter) Run(ctx context.Context) error {
	n, err := w.svc.AlertStalePaidTrades(ctx, w.after, 50)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("[p2p-paid-unreleased] alerted %d trades", n)
	}
	return nil
}
