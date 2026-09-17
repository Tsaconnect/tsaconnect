package jobs

import (
	"context"
	"errors"
	"log"
	"sync"
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

	// mu serialises scheduled ticks with user-triggered checks so the two never
	// fetch the statement or match concurrently.
	mu      sync.Mutex
	lastRun time.Time
	// checkCooldown is how soon after any tick a user-triggered check may fetch
	// again. Every tap across every user shares it, so a crowd pressing the
	// button cannot outrun Flutterwave's statement rate limit.
	checkCooldown time.Duration
	now           func() time.Time
}

// NewInboundCreditPoller builds the worker. A non-positive interval falls back
// to 30s so the scheduler (which panics on a non-positive interval at
// Register) always gets a sane period.
func NewInboundCreditPoller(ingestor InboundStatementIngestor, interval time.Duration) *InboundCreditPoller {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &InboundCreditPoller{
		ingestor:      ingestor,
		interval:      interval,
		batch:         100,
		checkCooldown: 10 * time.Second,
		now:           time.Now,
	}
}

// SetMatcher wires the attribution step. Until it is called the poller ingests
// only, which is a valid production state: rows pile up as `unmatched` and are
// attributed the moment a matcher exists, because nothing about them expired.
func (w *InboundCreditPoller) SetMatcher(m InboundCreditMatcher) { w.matcher = m }

// SetCheckCooldown sets the minimum gap between ticks that CheckNow enforces.
// Non-positive keeps the default rather than letting every tap hit the provider.
func (w *InboundCreditPoller) SetCheckCooldown(d time.Duration) {
	if d > 0 {
		w.checkCooldown = d
	}
}

func (w *InboundCreditPoller) Name() string            { return "inbound-credit-poller" }
func (w *InboundCreditPoller) Interval() time.Duration { return w.interval }

func (w *InboundCreditPoller) Run(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tick(ctx)
}

// CheckNow runs a tick ahead of schedule, for a user who says they have just
// paid. A tick already in flight is waited for rather than skipped — that tick
// is the likeliest one to find the payment. It reports whether it ran a tick
// itself: false means one finished within the cooldown, and that result is as
// fresh as this one would be.
func (w *InboundCreditPoller) CheckNow(ctx context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.lastRun.IsZero() && w.now().Sub(w.lastRun) < w.checkCooldown {
		return false, nil
	}
	return true, w.tick(ctx)
}

func (w *InboundCreditPoller) tick(ctx context.Context) error {
	defer func() { w.lastRun = w.now() }()
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
