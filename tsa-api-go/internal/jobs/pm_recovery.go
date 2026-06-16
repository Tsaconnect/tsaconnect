package jobs

import (
	"context"
	"log"
	"time"
)

// PMRecoverer is the narrow PM-automation surface the recovery job drives. The
// real *services.PMAutomationService satisfies it; tests inject a fake.
type PMRecoverer interface {
	// RecoverStuckTrades re-drives PM trades wedged at `paid` with no release
	// (markPaid landed, auto-release then failed) older than olderThan.
	RecoverStuckTrades(ctx context.Context, olderThan time.Duration) (int, error)
	// RetryFailedWebhooks re-dispatches Nomba webhook_events left `failed` (or
	// `pending` orphaned past staleAfter), recovering transient dispatch errors.
	RetryFailedWebhooks(ctx context.Context, staleAfter time.Duration) (int, error)
}

// PMRecoveryJob closes the M2 liveness gap: it periodically sweeps for the two
// classes of PM trades that have no other automated recovery —
//
//  1. paid-orphans: a buy auto-settle that did SystemMarkPaid (pending→paid)
//     then failed AutoRelease BEFORE the release tx landed on-chain. The chain
//     reconciler only recovers these if the tx actually landed; if the
//     broadcast never landed nothing re-drives them. RecoverStuckTrades
//     re-calls AutoRelease (idempotent + money-invariant-gated → safe).
//  2. failed/stale webhook events: a dispatch that errored (recorded `failed`)
//     or a `pending` row orphaned by a crash mid-dispatch. RetryFailedWebhooks
//     re-dispatches them while the underlying trade is still actionable.
//
// Idempotent by construction (both sub-tasks no-op on already-settled state), so
// a missed tick (restart) is recovered on the next one.
type PMRecoveryJob struct {
	rec        PMRecoverer
	interval   time.Duration
	stuckAfter time.Duration
	staleAfter time.Duration
}

// NewPMRecoveryJob constructs the job with sensible defaults: tick every 2m,
// recover paid-orphans older than 5m, retry webhooks stale past 5m.
func NewPMRecoveryJob(rec PMRecoverer) *PMRecoveryJob {
	return &PMRecoveryJob{
		rec:        rec,
		interval:   2 * time.Minute,
		stuckAfter: 5 * time.Minute,
		staleAfter: 5 * time.Minute,
	}
}

// WithInterval lets tests bypass the 2-minute cadence.
func (j *PMRecoveryJob) WithInterval(d time.Duration) *PMRecoveryJob {
	cp := *j
	cp.interval = d
	return &cp
}

func (j *PMRecoveryJob) Name() string            { return "pm-recovery" }
func (j *PMRecoveryJob) Interval() time.Duration { return j.interval }

// Run sweeps both recovery paths. It runs BOTH even if the first errors (one
// stuck sub-task must not skip the other) and returns the first error so the
// scheduler logs it; the scheduler tolerates errors and fires again next tick.
func (j *PMRecoveryJob) Run(ctx context.Context) error {
	var firstErr error

	recovered, err := j.rec.RecoverStuckTrades(ctx, j.stuckAfter)
	if err != nil {
		log.Printf("[pm-recovery] recover stuck trades: %v", err)
		firstErr = err
	} else if recovered > 0 {
		log.Printf("[pm-recovery] re-drove %d paid-orphan trade(s)", recovered)
	}

	retried, err := j.rec.RetryFailedWebhooks(ctx, j.staleAfter)
	if err != nil {
		log.Printf("[pm-recovery] retry failed webhooks: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	} else if retried > 0 {
		log.Printf("[pm-recovery] re-dispatched %d failed/stale webhook event(s)", retried)
	}

	return firstErr
}
