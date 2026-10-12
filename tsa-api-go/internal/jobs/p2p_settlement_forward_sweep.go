package jobs

import (
	"context"
	"log"
	"time"
)

// SettlementForwardSweeper is the narrow forwarder surface this job drives.
// *services.P2PSettlementForwarder satisfies it; tests inject a fake.
type SettlementForwardSweeper interface {
	// RedrivePending re-attempts forwards that were held back before anything
	// was submitted, and returns how many it re-drove.
	RedrivePending(ctx context.Context) (int, error)

	// SweepUnconfirmed alerts on forwards submitted to the payout provider that
	// were never answered, and returns how many it newly alerted on.
	SweepUnconfirmed(ctx context.Context) (int, error)

	// RecordSettledFees reads the provider's fee for recently settled
	// forwards — here, off the disburse webhook's path — and returns how many
	// it recorded. It moves no money.
	RecordSettledFees(ctx context.Context) (int, error)
}

// P2PForwardSweepJob makes the silences audible, and re-drives the forwards
// that nothing else would.
//
// A merchant's naira is forwarded the instant their trade settles. Every way
// that transfer can fail LOUDLY — rejected at submission, or reported failed on
// the disburse webhook — already alerts at the moment it happens. Two cases
// have no event of their own, and this job owns both:
//
//   - A transfer the provider accepted and then never mentioned again: no
//     success webhook, no failure webhook, nothing to react to. Only a sweep
//     can notice it.
//   - A transfer that was never submitted at all, because the payout balance
//     could not be read or could not cover it. Nothing else ever calls Forward
//     again, so without the re-drive the merchant stays credited and unpaid
//     with no surface showing it.
//
// The merchant's balance is credited in both cases, so the money is not lost
// and the invariant holds. But "the merchant has not been paid and nobody
// knows" is not a state that should be able to persist quietly.
//
// The sweep moves no money and alerts once per forward, so a stuck row does not
// re-alert on every tick. The re-drive can move money — it is the transfer
// finally going out — and is bounded by the forwarder's own attempt ceiling.
type P2PForwardSweepJob struct {
	sweeper  SettlementForwardSweeper
	interval time.Duration
}

// NewP2PForwardSweepJob ticks every 5 minutes. The forwarder's own staleness
// window decides what counts as unanswered; this only controls how soon after
// that window an operator hears about it.
func NewP2PForwardSweepJob(s SettlementForwardSweeper) *P2PForwardSweepJob {
	return &P2PForwardSweepJob{sweeper: s, interval: 5 * time.Minute}
}

// WithInterval lets tests bypass the 5-minute cadence.
func (j *P2PForwardSweepJob) WithInterval(d time.Duration) *P2PForwardSweepJob {
	cp := *j
	cp.interval = d
	return &cp
}

func (j *P2PForwardSweepJob) Name() string            { return "p2p-forward-sweep" }
func (j *P2PForwardSweepJob) Interval() time.Duration { return j.interval }

// Run does both halves unconditionally. They answer different questions about
// different rows, so a re-drive that fails must not cost the tick its staleness
// alerting — an unpaid merchant should still surface through whichever half is
// working.
func (j *P2PForwardSweepJob) Run(ctx context.Context) error {
	redriven, redriveErr := j.sweeper.RedrivePending(ctx)
	if redriveErr != nil {
		log.Printf("[p2p-forward-sweep] re-drive: %v", redriveErr)
	} else if redriven > 0 {
		log.Printf("[p2p-forward-sweep] re-drove %d unsent settlement forward(s)", redriven)
	}

	stale, sweepErr := j.sweeper.SweepUnconfirmed(ctx)
	if sweepErr != nil {
		log.Printf("[p2p-forward-sweep] %v", sweepErr)
	} else if stale > 0 {
		log.Printf("[p2p-forward-sweep] alerted on %d unanswered settlement forward(s)", stale)
	}

	// The fee read is bookkeeping: its failure never fails the tick.
	if n, err := j.sweeper.RecordSettledFees(ctx); err != nil {
		log.Printf("[p2p-forward-sweep] fees: %v", err)
	} else if n > 0 {
		log.Printf("[p2p-forward-sweep] recorded the fee of %d settled forward(s)", n)
	}

	if redriveErr != nil {
		return redriveErr
	}
	return sweepErr
}
