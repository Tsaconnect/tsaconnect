package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBuyOrderExpirer struct {
	calls int
	n     int64
	err   error
}

func (f *fakeBuyOrderExpirer) ExpireStaleBuyOrders(context.Context) (int64, error) {
	f.calls++
	return f.n, f.err
}

func TestBuyOrderExpiry_RunSweepsOnce(t *testing.T) {
	f := &fakeBuyOrderExpirer{n: 3}
	if err := NewBuyOrderExpiry(f, time.Hour).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.calls != 1 {
		t.Errorf("swept %d times, want 1", f.calls)
	}
}

// The scheduler logs a returned error and fires the next tick regardless, so
// surfacing the failure is the whole job — swallowing it would hide a sweep
// that has silently stopped closing anything.
func TestBuyOrderExpiry_RunReportsFailure(t *testing.T) {
	boom := errors.New("db down")
	err := NewBuyOrderExpiry(&fakeBuyOrderExpirer{err: boom}, time.Hour).Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to carry %v", err, boom)
	}
}

// A zero interval from a caller that forgot to set one must not become a
// busy-loop against the database.
func TestBuyOrderExpiry_ZeroIntervalFallsBackToHourly(t *testing.T) {
	if got := NewBuyOrderExpiry(&fakeBuyOrderExpirer{}, 0).Interval(); got != time.Hour {
		t.Errorf("interval = %v, want 1h", got)
	}
}
