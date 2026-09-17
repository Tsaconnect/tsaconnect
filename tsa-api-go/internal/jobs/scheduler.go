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
	"strings"
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
	workers []*registration
	started bool

	// failureNotifier is told when an opted-in worker's failure streak alerts
	// or recovers. Written only before Start (SetFailureNotifier panics after),
	// so the worker goroutines read it without the lock. Nil leaves the log
	// line as the only alert.
	failureNotifier WorkerFailureNotifier

	// now and logf are injectable so streak behaviour is testable without a
	// real clock or the global logger.
	now  func() time.Time
	logf func(format string, args ...any)

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// registration is one worker plus how the scheduler watches it. streak is
// touched only by that worker's own goroutine — ticks of one worker never
// overlap — so it needs no lock, and workers never contend with each other.
type registration struct {
	worker Worker
	name   string
	// alertThreshold is the consecutive failed ticks that raise an alert; 0
	// means the worker is not watched.
	alertThreshold int
	streak         failureStreak
}

// NewScheduler returns an empty scheduler. Register workers, then call Start.
func NewScheduler() *Scheduler {
	return &Scheduler{now: time.Now, logf: log.Printf}
}

// Register adds a worker. Panics if called after Start to surface lifecycle
// bugs at boot rather than at the first scheduled tick.
func (s *Scheduler) Register(w Worker, opts ...RegisterOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic(fmt.Sprintf("jobs.Scheduler: cannot register %q after Start", w.Name()))
	}
	if w.Interval() <= 0 {
		panic(fmt.Sprintf("jobs.Scheduler: %q has non-positive interval %v", w.Name(), w.Interval()))
	}
	reg := &registration{worker: w, name: w.Name()}
	for _, opt := range opts {
		opt(reg)
	}
	s.workers = append(s.workers, reg)
}

// SetFailureNotifier wires where failure-streak alerts are sent beyond the log.
// Panics after Start for the same reason Register does.
func (s *Scheduler) SetFailureNotifier(n WorkerFailureNotifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic("jobs.Scheduler: cannot set the failure notifier after Start")
	}
	s.failureNotifier = n
}

// Count returns how many workers are currently registered. Mainly for tests
// that need to assert a dormant feature registered nothing, without reaching
// into the unexported workers slice.
func (s *Scheduler) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workers)
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
	workers := append([]*registration(nil), s.workers...)
	s.mu.Unlock()

	var watched []string
	for _, reg := range workers {
		if reg.alertThreshold > 0 {
			watched = append(watched, fmt.Sprintf("%s(%d)", reg.name, reg.alertThreshold))
		}
		s.wg.Add(1)
		go s.runWorker(ctx, reg)
	}
	log.Printf("[jobs] scheduler started with %d worker(s); failure alerts after N consecutive failures: %s",
		len(workers), strings.Join(watched, " "))
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

func (s *Scheduler) runWorker(ctx context.Context, reg *registration) {
	defer s.wg.Done()
	name := reg.name
	interval := reg.worker.Interval()
	log.Printf("[jobs] worker %q started (interval=%s)", name, interval)

	t := time.NewTicker(interval)
	defer t.Stop()

	// Tick immediately on start so we don't wait one interval for the first
	// run. This matters because a fresh boot needs to reconcile in-flight
	// trades quickly, not interval seconds later.
	s.tick(ctx, reg)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[jobs] worker %q stopping: %v", name, ctx.Err())
			return
		case <-t.C:
			s.tick(ctx, reg)
		}
	}
}

// tick runs a single iteration. Panics in worker code are recovered so a
// single bad tick can't kill the scheduler goroutine and silently disable
// the worker forever — and a panic counts as a failed tick, because a worker
// that panics every tick is exactly as dead as one that errors.
func (s *Scheduler) tick(ctx context.Context, reg *registration) {
	err := s.runOnce(ctx, reg)
	if ctx.Err() != nil {
		// Shutdown cut the tick short; that says nothing about the worker.
		return
	}
	if err != nil {
		s.logf("[jobs] worker %q tick error: %v", reg.name, err)
	}
	s.recordOutcome(ctx, reg, err)
}

func (s *Scheduler) runOnce(ctx context.Context, reg *registration) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("[jobs] worker %q panicked: %v", reg.name, r)
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return reg.worker.Run(ctx)
}
