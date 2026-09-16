package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// The monitor is thin, but it is a safety net for a money invariant — so what
// matters is that it cannot silently do nothing. Each case below is a way it
// could appear to work while never actually reporting a drift.

func TestPMBalanceDivergenceMonitor_ReportsDrift(t *testing.T) {
	called := 0
	w := NewPMBalanceDivergenceMonitor(&gorm.DB{}, func(ctx context.Context, db *gorm.DB) ([]string, error) {
		called++
		return []string{"pm=abc amount mismatch"}, nil
	}, time.Minute)

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 1 {
		t.Errorf("check called %d times, want 1", called)
	}
}

// A failing check must SURFACE, not be swallowed. The scheduler logs returned
// errors; swallowing one would leave the drift watch silently broken, which is
// indistinguishable from "no drift" — the worst possible failure for a check
// whose entire job is to notice something.
func TestPMBalanceDivergenceMonitor_PropagatesCheckError(t *testing.T) {
	want := errors.New("db down")
	w := NewPMBalanceDivergenceMonitor(&gorm.DB{}, func(ctx context.Context, db *gorm.DB) ([]string, error) {
		return nil, want
	}, time.Minute)

	if err := w.Run(context.Background()); !errors.Is(err, want) {
		t.Errorf("Run err = %v, want %v", err, want)
	}
}

// Dormant-by-default: an unwired monitor is a no-op rather than a nil panic
// that would take the whole scheduler down.
func TestPMBalanceDivergenceMonitor_DormantWhenUnwired(t *testing.T) {
	for _, c := range []struct {
		name string
		w    *PMBalanceDivergenceMonitor
	}{
		{"nil db", NewPMBalanceDivergenceMonitor(nil, func(context.Context, *gorm.DB) ([]string, error) {
			t.Fatal("check must not run without a db")
			return nil, nil
		}, time.Minute)},
		{"nil check", NewPMBalanceDivergenceMonitor(&gorm.DB{}, nil, time.Minute)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.w.Run(context.Background()); err != nil {
				t.Errorf("Run: %v", err)
			}
		})
	}
}

func TestPMBalanceDivergenceMonitor_DefaultsInterval(t *testing.T) {
	w := NewPMBalanceDivergenceMonitor(&gorm.DB{}, nil, 0)
	if w.Interval() <= 0 {
		t.Errorf("Interval() = %v, want a positive default — a zero interval would "+
			"either spin or never fire", w.Interval())
	}
	if w.Name() == "" {
		t.Error("Name() is empty; the scheduler logs it")
	}
}
