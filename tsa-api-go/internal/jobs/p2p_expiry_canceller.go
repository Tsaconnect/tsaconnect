package jobs

import (
	"context"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// ExpiryRunner is services.ExpiryCanceller.
type ExpiryRunner interface {
	RunOnce(ctx context.Context) (services.ExpiryTick, error)
}

// P2PExpiryCanceller runs the expiry canceller every minute (spec §14, S24:
// always on, no flag) and keeps the sustained p2p_expiry_cancel_failed alert.
type P2PExpiryCanceller struct {
	r        ExpiryRunner
	interval time.Duration
	failing  *sustainedAlert
}

func NewP2PExpiryCanceller(r ExpiryRunner, notifier WorkerFailureNotifier) *P2PExpiryCanceller {
	return &P2PExpiryCanceller{r: r, interval: time.Minute, failing: &sustainedAlert{
		worker: "p2p-expiry-canceller", metric: "p2p_expiry_cancel_failed",
		what: "%d expired trades could not be cancelled on-chain", after: 15 * time.Minute, every: time.Hour, notifier: notifier,
	}}
}

func (j *P2PExpiryCanceller) Name() string            { return "p2p-expiry-canceller" }
func (j *P2PExpiryCanceller) Interval() time.Duration { return j.interval }

func (j *P2PExpiryCanceller) Run(ctx context.Context) error {
	tick, err := j.r.RunOnce(ctx)
	j.failing.track(ctx, tick.Failed, time.Now())
	return err
}
