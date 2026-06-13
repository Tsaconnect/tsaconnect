package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeReconciler struct {
	trades    []models.P2PTrade
	listErr   error
	outcomes  map[uuid.UUID]services.ReconcileResult
	errOnce   map[uuid.UUID]error
	reconciled atomic.Int32
}

func newFakeReconciler() *fakeReconciler {
	return &fakeReconciler{
		outcomes: map[uuid.UUID]services.ReconcileResult{},
		errOnce:  map[uuid.UUID]error{},
	}
}

func (f *fakeReconciler) ListNonTerminalTrades(_ context.Context, _ int) ([]models.P2PTrade, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.trades, nil
}

func (f *fakeReconciler) ReconcileTrade(_ context.Context, id uuid.UUID) (services.ReconcileResult, error) {
	f.reconciled.Add(1)
	if err, ok := f.errOnce[id]; ok {
		delete(f.errOnce, id)
		return services.ReconcileResult{}, err
	}
	out, ok := f.outcomes[id]
	if !ok {
		return services.ReconcileResult{}, nil
	}
	return out, nil
}

func TestChainReconciler_NoTrades_NoOp(t *testing.T) {
	r := NewChainReconciler(newFakeReconciler())
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestChainReconciler_IteratesAllTrades(t *testing.T) {
	f := newFakeReconciler()
	for i := 0; i < 5; i++ {
		f.trades = append(f.trades, models.P2PTrade{ID: uuid.New()})
	}
	r := NewChainReconciler(f)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.reconciled.Load() != 5 {
		t.Fatalf("want 5 reconcile calls, got %d", f.reconciled.Load())
	}
}

func TestChainReconciler_ContinuesOnPerTradeErrors(t *testing.T) {
	// A bad RPC call on one trade must not abort the whole pass.
	f := newFakeReconciler()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, id := range ids {
		f.trades = append(f.trades, models.P2PTrade{ID: id})
	}
	f.errOnce[ids[1]] = errors.New("rpc flake")
	r := NewChainReconciler(f)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.reconciled.Load() != 3 {
		t.Fatalf("want 3 reconcile attempts, got %d", f.reconciled.Load())
	}
}

func TestChainReconciler_RespectsContextCancellation(t *testing.T) {
	f := newFakeReconciler()
	for i := 0; i < 5; i++ {
		f.trades = append(f.trades, models.P2PTrade{ID: uuid.New()})
	}
	r := NewChainReconciler(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel; loop should bail on first check
	err := r.Run(ctx)
	if err == nil {
		t.Fatalf("want ctx error, got nil")
	}
}

func TestChainReconciler_ListErrorBubblesUp(t *testing.T) {
	f := newFakeReconciler()
	f.listErr = errors.New("db down")
	r := NewChainReconciler(f)
	if err := r.Run(context.Background()); err == nil {
		t.Fatalf("want error when list fails")
	}
}

func TestChainReconciler_Interval(t *testing.T) {
	r := NewChainReconciler(newFakeReconciler())
	if r.Interval() != 30*time.Second {
		t.Fatalf("default interval want 30s, got %s", r.Interval())
	}
	if r.WithInterval(5*time.Second).Interval() != 5*time.Second {
		t.Fatalf("interval override failed")
	}
}
