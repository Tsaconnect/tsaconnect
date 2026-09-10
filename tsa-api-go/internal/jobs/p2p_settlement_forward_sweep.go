package jobs

import (
	"context"
	"log"
	"time"
)

// SettlementForwardSweeper is the narrow forwarder surface this job drives.
// *services.P2PSettlementForwarder satisfies it; tests inject a fake.
type SettlementForwardSweeper interface {
	// SweepUnconfirmed alerts on forwards submitted to the payout provider that
	// were never answered, and returns how many it newly alerted on.
	SweepUnconfirmed(ctx context.Context) (int, error)
}

// P2PForwardSweepJob makes one specific silence audible.
//
// A merchant's naira is forwarded the instant their trade settles. Every way
// that transfer can fail LOUDLY — rejected at submission, or reported failed on
// the disburse webhook — already alerts at the moment it happens. The one case
// with no event of its own is a transfer the provider accepted and then never
// mentioned again: no success webhook, no failure webhook, nothing to react to.
//
// The merchant's balance is still credited in that case, so the money is not
// lost and the invariant holds. But "the merchant has not been paid and nobody
// knows" is not a state that should be able to persist quietly, and only a
// sweep can notice it.
//
// It moves no money. Alerting is once per forward, so a stuck row does not
// re-alert on every tick.
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

func (j *P2PForwardSweepJob) Run(ctx context.Context) error {
	n, err := j.sweeper.SweepUnconfirmed(ctx)
	if err != nil {
		log.Printf("[p2p-forward-sweep] %v", err)
		return err
	}
	if n > 0 {
		log.Printf("[p2p-forward-sweep] alerted on %d unanswered settlement forward(s)", n)
	}
	return nil
}
