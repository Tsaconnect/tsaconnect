package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeInboundIngestor struct {
	res   services.IngestResult
	err   error
	calls int
}

func (f *fakeInboundIngestor) RunOnce(_ context.Context) (services.IngestResult, error) {
	f.calls++
	return f.res, f.err
}

type fakeInboundMatcher struct {
	decided   int
	err       error
	calls     int
	lastLimit int
}

func (f *fakeInboundMatcher) MatchPending(_ context.Context, limit int) (int, error) {
	f.calls++
	f.lastLimit = limit
	return f.decided, f.err
}

// The poller must be a usable Worker with the 30s cadence of decision 8, and a
// bad interval must not be able to panic the scheduler at Register.
func TestInboundCreditPoller_WorkerContract(t *testing.T) {
	w := NewInboundCreditPoller(&fakeInboundIngestor{}, 0)
	if w.Name() != "inbound-credit-poller" {
		t.Errorf("Name = %q", w.Name())
	}
	if w.Interval() != 30*time.Second {
		t.Errorf("Interval = %s, want the 30s default", w.Interval())
	}
	if got := NewInboundCreditPoller(&fakeInboundIngestor{}, time.Minute).Interval(); got != time.Minute {
		t.Errorf("Interval = %s, want the configured minute", got)
	}
}

// Ingest-only is a valid production state: rows are recorded and simply wait
// for a matcher to exist.
func TestInboundCreditPoller_IngestsWithoutAMatcher(t *testing.T) {
	ing := &fakeInboundIngestor{res: services.IngestResult{Rows: 3, Funding: 1, Inserted: 1}}
	w := NewInboundCreditPoller(ing, time.Minute)

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ing.calls != 1 {
		t.Errorf("ingest calls = %d, want 1", ing.calls)
	}
}

func TestInboundCreditPoller_RunsMatcherAfterIngest(t *testing.T) {
	ing := &fakeInboundIngestor{res: services.IngestResult{Inserted: 2}}
	m := &fakeInboundMatcher{decided: 2}
	w := NewInboundCreditPoller(ing, time.Minute)
	w.SetMatcher(m)

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.calls != 1 {
		t.Fatalf("matcher calls = %d, want 1", m.calls)
	}
	if m.lastLimit <= 0 {
		t.Errorf("matcher limit = %d, want a bounded positive batch", m.lastLimit)
	}
}

// A provider outage must not stop attribution of credits already recorded —
// that separation is the point of splitting ingest from match.
func TestInboundCreditPoller_MatchesEvenWhenIngestFails(t *testing.T) {
	ing := &fakeInboundIngestor{err: errors.New("flutterwave 503")}
	m := &fakeInboundMatcher{decided: 1}
	w := NewInboundCreditPoller(ing, time.Minute)
	w.SetMatcher(m)

	err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded, want the ingest error surfaced")
	}
	if m.calls != 1 {
		t.Errorf("matcher calls = %d, want 1 despite the fetch failure", m.calls)
	}
}

// Both failures must be reported; neither may mask the other.
func TestInboundCreditPoller_JoinsBothErrors(t *testing.T) {
	ingErr := errors.New("flutterwave 503")
	matchErr := errors.New("matcher exploded")
	w := NewInboundCreditPoller(&fakeInboundIngestor{err: ingErr}, time.Minute)
	w.SetMatcher(&fakeInboundMatcher{err: matchErr})

	err := w.Run(context.Background())
	if !errors.Is(err, ingErr) || !errors.Is(err, matchErr) {
		t.Fatalf("err = %v, want both the ingest and match errors", err)
	}
}

// Shutdown must not start a matching pass the context has already cancelled.
func TestInboundCreditPoller_StopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &fakeInboundMatcher{}
	w := NewInboundCreditPoller(&fakeInboundIngestor{}, time.Minute)
	w.SetMatcher(m)

	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if m.calls != 0 {
		t.Errorf("matcher calls = %d, want 0 on a cancelled context", m.calls)
	}
}
