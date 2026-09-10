package jobs

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// InboundStatementIngestor is the narrow slice of services.InboundIngestor
// this worker needs. *services.InboundIngestor satisfies it; tests inject a
// fake.
type InboundStatementIngestor interface {
	RunOnce(ctx context.Context) (services.IngestResult, error)
}

// InboundCreditMatcher is the seam the attribution step lands on (design §3).
//
// It is deliberately a separate interface, wired separately and allowed to be
// nil: ingestion must keep recording money whether or not anything can yet
// attribute it, and a matcher that panics, hangs or is simply not built must
// not be able to stop the record being written. MatchPending returns how many
// credits it decided on.
type InboundCreditMatcher interface {
	MatchPending(ctx context.Context, limit int) (int, error)
}

// InboundCreditPoller is the only thing that notices money arriving in the
// central account.
//
// Flutterwave fires NO webhook for wallet funding — that is measured, not
// assumed — so unlike a reconciler this worker is not a safety net over some
// other mechanism: it IS the mechanism. If it stops ticking, credits go
// unseen and users who have paid see nothing happen. Hence the 30-second tick
// (design decision 8) rather than the minutes a checker would use.
//
// Each tick does two independent things in order: ingest (durable record) and
// then match (attribution), with the second allowed to be absent. An ingest
// failure does not skip matching, and a matching failure cannot unwrite an
// ingested row — that separation is the whole point of the design.
type InboundCreditPoller struct {
	ingestor InboundStatementIngestor
	matcher  InboundCreditMatcher
	interval time.Duration
	// batch bounds one tick's matching so a backlog cannot make a tick outrun
	// the interval — the scheduler skips rather than queues, so a slow tick
	// silently halves the effective poll rate.
	batch int
}

// NewInboundCreditPoller builds the worker. A non-positive interval falls back
// to 30s so the scheduler (which panics on a non-positive interval at
// Register) always gets a sane period.
func NewInboundCreditPoller(ingestor InboundStatementIngestor, interval time.Duration) *InboundCreditPoller {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &InboundCreditPoller{ingestor: ingestor, interval: interval, batch: 100}
}

// SetMatcher wires the attribution step. Until it is called the poller ingests
// only, which is a valid production state: rows pile up as `unmatched` and are
// attributed the moment a matcher exists, because nothing about them expired.
func (w *InboundCreditPoller) SetMatcher(m InboundCreditMatcher) { w.matcher = m }

func (w *InboundCreditPoller) Name() string            { return "inbound-credit-poller" }
func (w *InboundCreditPoller) Interval() time.Duration { return w.interval }

func (w *InboundCreditPoller) Run(ctx context.Context) error {
	var errs []error

	res, err := w.ingestor.RunOnce(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	// Only log a tick that did something. At 30s the quiet ticks vastly
	// outnumber the interesting ones and logging every one buries them.
	if res.Inserted > 0 || res.Errors > 0 || res.Unparsed > 0 {
		log.Printf("[inbound-credit-poller] rows=%d funding=%d inserted=%d dup=%d unparsed=%d errors=%d",
			res.Rows, res.Funding, res.Inserted, res.Duplicates, res.Unparsed, res.Errors)
	}

	// Matching runs even when the fetch failed: pending credits from earlier
	// ticks are still attributable while the provider is down.
	if w.matcher != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		decided, merr := w.matcher.MatchPending(ctx, w.batch)
		if merr != nil {
			errs = append(errs, merr)
		}
		if decided > 0 {
			log.Printf("[inbound-credit-poller] matched=%d", decided)
		}
	}

	return errors.Join(errs...)
}
