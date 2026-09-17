package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSellReconciler struct {
	calls int
	grace time.Duration
	err   error
}

func (f *fakeSellReconciler) ReconcileStuckSells(_ context.Context, grace time.Duration) (int, error) {
	f.calls++
	f.grace = grace
	return 0, f.err
}

func TestITSellReconciler_RunsOncePerTickWithGrace(t *testing.T) {
	f := &fakeSellReconciler{}
	w := NewITSellReconciler(f, time.Minute)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("calls = %d, want 1", f.calls)
	}
	// The grace must outlast an in-flight sell: the 3-minute detached create
	// budget, or the reconciler races a request still broadcasting or paying.
	if f.grace < 3*time.Minute {
		t.Fatalf("grace = %v, want at least the sell create budget", f.grace)
	}
}

func TestITSellReconciler_ReportsFailure(t *testing.T) {
	boom := errors.New("db down")
	if err := NewITSellReconciler(&fakeSellReconciler{err: boom}, time.Minute).Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestITSellReconciler_ZeroIntervalFallsBack(t *testing.T) {
	if got := NewITSellReconciler(&fakeSellReconciler{}, 0).Interval(); got <= 0 {
		t.Fatalf("interval = %v, want a positive default", got)
	}
}
