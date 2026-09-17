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

// "I've paid" runs a tick at once, but taps inside the cooldown share the last
// result instead of each fetching the statement again.
func TestInboundCreditPoller_CheckNowRespectsCooldown(t *testing.T) {
	ing := &fakeInboundIngestor{}
	w := NewInboundCreditPoller(ing, time.Minute)
	clock := time.Unix(1_700_000_000, 0)
	w.now = func() time.Time { return clock }

	if ran, err := w.CheckNow(context.Background()); !ran || err != nil {
		t.Fatalf("first CheckNow = (%v, %v), want a tick", ran, err)
	}
	if ran, _ := w.CheckNow(context.Background()); ran {
		t.Error("second CheckNow inside the cooldown ran a tick")
	}
	clock = clock.Add(w.checkCooldown)
	if ran, _ := w.CheckNow(context.Background()); !ran {
		t.Error("CheckNow after the cooldown did not run a tick")
	}
	if ing.calls != 2 {
		t.Errorf("ingest calls = %d, want 2", ing.calls)
	}
}

// A scheduled tick also counts: a check right after it has nothing newer to find.
func TestInboundCreditPoller_CheckNowSkipsRightAfterScheduledTick(t *testing.T) {
	ing := &fakeInboundIngestor{}
	w := NewInboundCreditPoller(ing, time.Minute)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran, _ := w.CheckNow(context.Background()); ran {
		t.Error("CheckNow ran straight after a scheduled tick")
	}
}

type blockingInboundIngestor struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

func (b *blockingInboundIngestor) RunOnce(context.Context) (services.IngestResult, error) {
	b.calls++
	if b.calls == 1 {
		close(b.started)
		<-b.release
	}
	return services.IngestResult{}, nil
}

// A check never overlaps a tick in flight: it waits for that tick, then relies
// on its result instead of fetching the statement a second time.
func TestInboundCreditPoller_CheckNowWaitsForTickInFlight(t *testing.T) {
	ing := &blockingInboundIngestor{started: make(chan struct{}), release: make(chan struct{})}
	w := NewInboundCreditPoller(ing, time.Minute)

	runDone := make(chan struct{})
	go func() {
		_ = w.Run(context.Background())
		close(runDone)
	}()
	<-ing.started

	checkDone := make(chan bool)
	go func() {
		ran, _ := w.CheckNow(context.Background())
		checkDone <- ran
	}()
	select {
	case <-checkDone:
		t.Fatal("CheckNow returned while a tick was still running")
	case <-time.After(20 * time.Millisecond):
	}

	close(ing.release)
	<-runDone
	if ran := <-checkDone; ran {
		t.Error("CheckNow ran a second tick straight after the one it waited for")
	}
	if ing.calls != 1 {
		t.Errorf("ingest calls = %d, want 1", ing.calls)
	}
}

// The tick's error reaches the caller so the handler can log it.
func TestInboundCreditPoller_CheckNowReturnsTickError(t *testing.T) {
	w := NewInboundCreditPoller(&fakeInboundIngestor{err: errors.New("fw down")}, time.Minute)
	if ran, err := w.CheckNow(context.Background()); !ran || err == nil {
		t.Errorf("CheckNow = (%v, %v), want ran with an error", ran, err)
	}
}

func TestInboundCreditPoller_SetCheckCooldownIgnoresNonPositive(t *testing.T) {
	w := NewInboundCreditPoller(&fakeInboundIngestor{}, time.Minute)
	w.SetCheckCooldown(0)
	if w.checkCooldown != 10*time.Second {
		t.Errorf("cooldown = %s, want the 10s default kept", w.checkCooldown)
	}
	w.SetCheckCooldown(time.Minute)
	if w.checkCooldown != time.Minute {
		t.Errorf("cooldown = %s, want 1m", w.checkCooldown)
	}
}
