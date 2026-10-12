package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakePoolSettle struct {
	received, delivering []uuid.UUID
	pendingErr           error
	advanced             []uuid.UUID
	advanceErr           error
	books                int
}

func (f *fakePoolSettle) PendingPoolBuys(context.Context) ([]uuid.UUID, []uuid.UUID, error) {
	return f.received, f.delivering, f.pendingErr
}
func (f *fakePoolSettle) AdvancePoolBuy(_ context.Context, id uuid.UUID) error {
	f.advanced = append(f.advanced, id)
	return f.advanceErr
}
func (f *fakePoolSettle) CompletePoolBooks(context.Context) (int, error) { f.books++; return 0, nil }

func TestPoolSettleJob_AdvancesEveryOrderThenBooks(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := &fakePoolSettle{received: []uuid.UUID{a}, delivering: []uuid.UUID{b}}
	j := NewPoolSettleJob(f, 0)
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.advanced) != 2 || f.books != 1 || j.Name() != "liquidity-pool-settle" || j.Interval() != 30*time.Second {
		t.Fatalf("advanced=%v books=%d name=%s every=%s", f.advanced, f.books, j.Name(), j.Interval())
	}
}

// The books pass (LP back-fill, review I-3) runs even when listing fails.
func TestPoolSettleJob_BooksRunEvenWhenListingFails(t *testing.T) {
	f := &fakePoolSettle{pendingErr: errors.New("db down")}
	if err := NewPoolSettleJob(f, 0).Run(context.Background()); err == nil || f.books != 1 {
		t.Fatalf("err=%v books=%d, want the error reported and the books pass run", err, f.books)
	}
}

// F10: a stall alerts on its own sustained channel and never fails the tick.
func TestPoolSettleJob_StallAlertsSustained(t *testing.T) {
	f := &fakePoolSettle{delivering: []uuid.UUID{uuid.New()},
		advanceErr: fmt.Errorf("%w: intent x", services.ErrPoolReleaseStalled)}
	n := &recordingNotifier{}
	j := NewPoolSettleJob(f, 0)
	j.SetAlerter(n)
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.failing) == 0 || n.failing[0].Worker != "liquidity-pool-release-stalled" {
		t.Fatalf("notified %+v, want the stalled channel", n.failing)
	}
}

// A paid order that cannot advance for any other reason alerts on the blocked
// channel once it has lasted 10 minutes, never fails the tick, and recovers.
func TestPoolSettleJob_BlockedAlertsAfterTenMinutesAndRecovers(t *testing.T) {
	f := &fakePoolSettle{received: []uuid.UUID{uuid.New()}, advanceErr: services.ErrPoolSettleUnwired}
	n := &recordingNotifier{}
	j := NewPoolSettleJob(f, 0)
	j.SetAlerter(n)
	start := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	now := start
	j.now = func() time.Time { return now }
	ctx := context.Background()

	if err := j.Run(ctx); err != nil || len(n.failing) != 0 {
		t.Fatalf("err=%v notified=%+v, want no alert before 10 minutes", err, n.failing)
	}
	now = start.Add(11 * time.Minute)
	if err := j.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(n.failing) != 1 || n.failing[0].Worker != "liquidity-pool-settle-blocked" {
		t.Fatalf("notified %+v, want the blocked channel", n.failing)
	}
	f.advanceErr = nil
	if err := j.Run(ctx); err != nil || len(n.recovered) != 1 {
		t.Fatalf("err=%v recovered=%+v, want one recovery", err, n.recovered)
	}
}

// The job is registered alert-watched: a settle job that fails every tick
// leaves paid orders unsettled, so its failure streak must page.
func TestPoolSettleJob_RegistersAlertWatched(t *testing.T) {
	s := NewScheduler()
	s.Register(NewPoolSettleJob(&fakePoolSettle{}, 0), AlertOnFailureStreak())
	if got := s.workers[0].alertThreshold; got != 10 {
		t.Fatalf("threshold = %d, want 10 for the 30s settle job", got)
	}
}

// poolSettleEverything is a driver that could also do the chain reconciler's
// and the conversion orchestrator's work. The settle job must call only its
// own three steps: the reconciler's trade walk and the orchestrator's tick
// stay theirs, so no tick is ever processed twice.
type poolSettleEverything struct {
	fakePoolSettle
	listTrades, reconciles, orchestratorTicks int
}

func (p *poolSettleEverything) ListNonTerminalTrades(context.Context, int) ([]models.P2PTrade, error) {
	p.listTrades++
	return nil, nil
}
func (p *poolSettleEverything) ReconcileTrade(context.Context, uuid.UUID) (services.ReconcileResult, error) {
	p.reconciles++
	return services.ReconcileResult{}, nil
}
func (p *poolSettleEverything) Tick(context.Context, int) error { p.orchestratorTicks++; return nil }

var (
	_ TradeReconciler  = (*poolSettleEverything)(nil)
	_ conversionTicker = (*poolSettleEverything)(nil)
)

func TestPoolSettleJob_NeverDoesTheReconcilersOrTheOrchestratorsWork(t *testing.T) {
	d := &poolSettleEverything{fakePoolSettle: fakePoolSettle{
		received: []uuid.UUID{uuid.New()}, delivering: []uuid.UUID{uuid.New()}}}
	j := NewPoolSettleJob(d, 0)
	for range 3 {
		if err := j.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d.listTrades != 0 || d.reconciles != 0 || d.orchestratorTicks != 0 {
		t.Fatalf("list=%d reconcile=%d orchestrator=%d, want the settle job to leave their work to them",
			d.listTrades, d.reconciles, d.orchestratorTicks)
	}
	if len(d.advanced) != 6 || d.books != 3 {
		t.Fatalf("advanced=%d books=%d, want its own steps every tick", len(d.advanced), d.books)
	}
	var w Worker = j
	if _, ok := w.(interface {
		ListNonTerminalTrades(context.Context, int) ([]models.P2PTrade, error)
	}); ok {
		t.Fatal("the settle job must not be a trade reconciler")
	}
}

// blockingPoolSettle holds AdvancePoolBuy until released: a release's
// receipt wait on the broadcast tick.
type blockingPoolSettle struct {
	fakePoolSettle
	entered, release chan struct{}
}

func (b *blockingPoolSettle) AdvancePoolBuy(ctx context.Context, _ uuid.UUID) error {
	b.entered <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

// tickingBuyDriver reports each StableBuyWatcher tick.
type tickingBuyDriver struct {
	*fakeBuyDriver
	ticked chan struct{}
}

func (d *tickingBuyDriver) PendingStableBuys(ctx context.Context) ([]uuid.UUID, []uuid.UUID, []uuid.UUID, []uuid.UUID, error) {
	select {
	case d.ticked <- struct{}{}:
	default:
	}
	return nil, nil, nil, nil, nil
}

// Review I-8: a pool release blocked on its receipt never delays the shared
// StableBuyWatcher (company wallet and Flutterwave deliveries).
func TestPoolSettleJob_ABlockedReleaseNeverHoldsTheStableBuyWatcher(t *testing.T) {
	pool := &blockingPoolSettle{fakePoolSettle: fakePoolSettle{delivering: []uuid.UUID{uuid.New()}},
		entered: make(chan struct{}, 1), release: make(chan struct{})}
	buys := &tickingBuyDriver{fakeBuyDriver: &fakeBuyDriver{}, ticked: make(chan struct{}, 1)}

	s := NewScheduler()
	s.Register(NewPoolSettleJob(pool, time.Hour), AlertOnFailureStreak())
	s.Register(NewStableBuyWatcher(buys, 10*time.Millisecond), AlertOnFailureStreak())
	s.Start(context.Background())
	defer s.Stop()
	defer close(pool.release)

	select {
	case <-pool.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool settle job never ticked")
	}
	for i := range 3 { // the watcher keeps ticking while the release is held
		select {
		case <-buys.ticked:
		case <-time.After(5 * time.Second):
			t.Fatalf("StableBuyWatcher tick %d never came while a pool release was blocked", i+1)
		}
	}
}
