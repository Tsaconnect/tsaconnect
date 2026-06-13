// Package jobs runs in-process periodic workers that need to outlive any
// individual HTTP request: P2P trade-timeout watchdog, chain-event
// reconciler, money-invariant checker, notification dispatcher.
//
// Phase 1 deliberately uses goroutine + time.Ticker rather than a durable
// queue (River, Asynq) because every Phase-1 worker is idempotent: each tick
// re-derives state from on-chain + DB, so a missed tick (server restart) is
// recovered by the next tick. Durability earns its keep in Phase 2 when the
// Flutterwave webhook handler enqueues operator-signed releases — at that
// point this package migrates to River and the Worker interface here is the
// adapter shape.
package jobs

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Worker is a single periodic task. Implementations MUST be idempotent — they
// can be invoked again on the next tick after a crash without side-effect
// duplication.
type Worker interface {
	// Name is used in log lines and metrics labels. Must be stable across
	// restarts. Keep it lowercase-with-dashes (e.g. "p2p-timeout-watchdog").
	Name() string

	// Interval is the tick period. Worker code must complete in well under
	// the interval; if Run blocks longer the next tick is skipped (not
	// queued) — the scheduler does not maintain a backlog.
	Interval() time.Duration

	// Run is invoked once per tick. ctx is cancelled on shutdown — workers
	// must observe it and return promptly. Errors are logged but do not
	// stop the worker; the next tick fires regardless.
	Run(ctx context.Context) error
}

// Scheduler owns the lifetime of registered workers.
//
// Lifecycle:
//
//	s := jobs.NewScheduler()
//	s.Register(workerA)
//	s.Register(workerB)
//	s.Start(rootCtx)   // returns immediately; workers run in goroutines
//	... server runs ...
//	s.Stop()           // cancels ctx, waits for workers to drain
//
// Register MUST NOT be called after Start.
type Scheduler struct {
	mu      sync.Mutex
	workers []Worker
	started bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewScheduler returns an empty scheduler. Register workers, then call Start.
func NewScheduler() *Scheduler {
	return &Scheduler{}
}

// Register adds a worker. Panics if called after Start to surface lifecycle
// bugs at boot rather than at the first scheduled tick.
func (s *Scheduler) Register(w Worker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic(fmt.Sprintf("jobs.Scheduler: cannot register %q after Start", w.Name()))
	}
	if w.Interval() <= 0 {
		panic(fmt.Sprintf("jobs.Scheduler: %q has non-positive interval %v", w.Name(), w.Interval()))
	}
	s.workers = append(s.workers, w)
}

// Start spawns one goroutine per registered worker. Returns immediately.
// Calling Start twice is a programmer error and panics.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		panic("jobs.Scheduler: Start called twice")
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	workers := append([]Worker(nil), s.workers...)
	s.mu.Unlock()

	for _, w := range workers {
		s.wg.Add(1)
		go s.runWorker(ctx, w)
	}
	log.Printf("[jobs] scheduler started with %d worker(s)", len(workers))
}

// Stop cancels the root context and blocks until every worker returns. Safe
// to call from a signal handler. Stop after a non-Started scheduler is a
// no-op.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started || s.cancel == nil {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	cancel()
	s.wg.Wait()
	log.Printf("[jobs] scheduler stopped")
}

func (s *Scheduler) runWorker(ctx context.Context, w Worker) {
	defer s.wg.Done()
	name := w.Name()
	interval := w.Interval()
	log.Printf("[jobs] worker %q started (interval=%s)", name, interval)

	t := time.NewTicker(interval)
	defer t.Stop()

	// Tick immediately on start so we don't wait one interval for the first
	// run. This matters because a fresh boot needs to reconcile in-flight
	// trades quickly, not interval seconds later.
	s.tick(ctx, w, name)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[jobs] worker %q stopping: %v", name, ctx.Err())
			return
		case <-t.C:
			s.tick(ctx, w, name)
		}
	}
}

// tick runs a single iteration. Panics in worker code are recovered so a
// single bad tick can't kill the scheduler goroutine and silently disable
// the worker forever.
func (s *Scheduler) tick(ctx context.Context, w Worker, name string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[jobs] worker %q panicked: %v", name, r)
		}
	}()

	if err := w.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("[jobs] worker %q tick error: %v", name, err)
	}
}
