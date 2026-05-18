package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWorker counts ticks. interval is the requested cadence; runErr (if set)
// is returned on every tick; panicAfter (if >0) panics on the Nth tick to
// verify the recover path.
type fakeWorker struct {
	name       string
	interval   time.Duration
	ticks      atomic.Int32
	runErr     error
	panicAfter int32
}

func (f *fakeWorker) Name() string             { return f.name }
func (f *fakeWorker) Interval() time.Duration  { return f.interval }
func (f *fakeWorker) Run(_ context.Context) error {
	n := f.ticks.Add(1)
	if f.panicAfter > 0 && n == f.panicAfter {
		panic("induced panic for test")
	}
	return f.runErr
}

func TestScheduler_StartTicksImmediately(t *testing.T) {
	s := NewScheduler()
	w := &fakeWorker{name: "immediate", interval: time.Hour}
	s.Register(w)

	ctx := context.Background()
	s.Start(ctx)
	defer s.Stop()

	// Worker should tick on Start without waiting for the (1-hour) interval.
	if !waitForAtLeast(&w.ticks, 1, 500*time.Millisecond) {
		t.Fatalf("worker did not tick on start; got %d", w.ticks.Load())
	}
}

func TestScheduler_TicksAtInterval(t *testing.T) {
	s := NewScheduler()
	w := &fakeWorker{name: "interval", interval: 20 * time.Millisecond}
	s.Register(w)

	s.Start(context.Background())
	defer s.Stop()

	// Expect: 1 immediate tick + at least 2 interval ticks within 100ms.
	if !waitForAtLeast(&w.ticks, 3, 200*time.Millisecond) {
		t.Fatalf("worker only ticked %d times; expected >=3", w.ticks.Load())
	}
}

func TestScheduler_Stop_HaltsWorkers(t *testing.T) {
	s := NewScheduler()
	w := &fakeWorker{name: "stop", interval: 10 * time.Millisecond}
	s.Register(w)

	s.Start(context.Background())
	if !waitForAtLeast(&w.ticks, 2, 100*time.Millisecond) {
		t.Fatalf("worker did not start ticking")
	}
	s.Stop()

	mid := w.ticks.Load()
	time.Sleep(80 * time.Millisecond)
	after := w.ticks.Load()
	if after != mid {
		t.Fatalf("worker still ticked after Stop: mid=%d after=%d", mid, after)
	}
}

func TestScheduler_ErrorDoesNotHaltWorker(t *testing.T) {
	s := NewScheduler()
	w := &fakeWorker{name: "errs", interval: 15 * time.Millisecond, runErr: errors.New("boom")}
	s.Register(w)

	s.Start(context.Background())
	defer s.Stop()

	if !waitForAtLeast(&w.ticks, 3, 200*time.Millisecond) {
		t.Fatalf("worker stopped after returning errors; ticks=%d", w.ticks.Load())
	}
}

func TestScheduler_PanicRecovered(t *testing.T) {
	s := NewScheduler()
	w := &fakeWorker{name: "panicker", interval: 15 * time.Millisecond, panicAfter: 1}
	s.Register(w)

	s.Start(context.Background())
	defer s.Stop()

	// After the panic on tick 1, subsequent ticks must still fire.
	if !waitForAtLeast(&w.ticks, 3, 200*time.Millisecond) {
		t.Fatalf("worker did not recover from panic; ticks=%d", w.ticks.Load())
	}
}

func TestScheduler_RegisterAfterStartPanics(t *testing.T) {
	s := NewScheduler()
	s.Start(context.Background())
	defer s.Stop()

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when registering after Start")
		}
	}()
	s.Register(&fakeWorker{name: "late", interval: time.Second})
}

func TestScheduler_StartCalledTwicePanics(t *testing.T) {
	s := NewScheduler()
	s.Start(context.Background())
	defer s.Stop()

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on double Start")
		}
	}()
	s.Start(context.Background())
}

func TestScheduler_NonPositiveIntervalPanics(t *testing.T) {
	s := NewScheduler()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for zero interval")
		}
	}()
	s.Register(&fakeWorker{name: "zero", interval: 0})
}

func TestScheduler_StopWithoutStartIsNoOp(t *testing.T) {
	s := NewScheduler()
	s.Stop() // must not panic or hang
}

// blockingWorker is held inside Run() by a release channel — Stop must wait
// for Run to return before declaring the scheduler stopped.
type blockingWorker struct {
	name     string
	interval time.Duration
	entered  chan struct{}
	release  chan struct{}
	gotCtx   chan context.Context
}

func (b *blockingWorker) Name() string            { return b.name }
func (b *blockingWorker) Interval() time.Duration { return b.interval }
func (b *blockingWorker) Run(ctx context.Context) error {
	select {
	case b.gotCtx <- ctx:
	default:
	}
	close(b.entered)
	<-b.release
	return nil
}

func TestScheduler_Stop_WaitsForInFlightRun(t *testing.T) {
	w := &blockingWorker{
		name:     "blocking",
		interval: time.Hour, // never re-tick — only the immediate first tick matters
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		gotCtx:   make(chan context.Context, 1),
	}
	s := NewScheduler()
	s.Register(w)
	s.Start(context.Background())

	<-w.entered // worker is now mid-Run

	stopReturned := make(chan struct{})
	go func() {
		s.Stop()
		close(stopReturned)
	}()

	// Stop must not return while the worker is still inside Run.
	select {
	case <-stopReturned:
		t.Fatalf("Stop returned while worker mid-Run; expected wait")
	case <-time.After(80 * time.Millisecond):
	}

	close(w.release)

	select {
	case <-stopReturned:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Stop did not return after worker finished")
	}
}

func TestScheduler_Run_ReceivesCancellableCtx(t *testing.T) {
	w := &blockingWorker{
		name:     "ctx-capture",
		interval: time.Hour,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		gotCtx:   make(chan context.Context, 1),
	}
	s := NewScheduler()
	s.Register(w)
	s.Start(context.Background())
	<-w.entered

	var workerCtx context.Context
	select {
	case workerCtx = <-w.gotCtx:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("worker did not publish its ctx")
	}

	// Stop in another goroutine; the captured ctx should be cancelled.
	go s.Stop()
	select {
	case <-workerCtx.Done():
		// good — Stop propagated to worker
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("worker ctx not cancelled after Stop")
	}
	close(w.release)
}

func waitForAtLeast(counter *atomic.Int32, n int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if counter.Load() >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return counter.Load() >= n
}
