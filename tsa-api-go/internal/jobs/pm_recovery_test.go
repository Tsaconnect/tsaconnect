package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePMRecoverer records the durations it was called with and returns canned
// counts/errors so the job's wiring (which call, which duration) is asserted
// without a real PMAutomationService graph.
type fakePMRecoverer struct {
	stuckCalls []time.Duration
	staleCalls []time.Duration
	stuckCount int
	staleCount int
	stuckErr   error
	staleErr   error
}

func (f *fakePMRecoverer) RecoverStuckTrades(_ context.Context, olderThan time.Duration) (int, error) {
	f.stuckCalls = append(f.stuckCalls, olderThan)
	return f.stuckCount, f.stuckErr
}

func (f *fakePMRecoverer) RetryFailedWebhooks(_ context.Context, staleAfter time.Duration) (int, error) {
	f.staleCalls = append(f.staleCalls, staleAfter)
	return f.staleCount, f.staleErr
}

func TestPMRecoveryJob_RunCallsBothWithConfiguredDurations(t *testing.T) {
	rec := &fakePMRecoverer{stuckCount: 2, staleCount: 3}
	j := NewPMRecoveryJob(rec)

	if err := j.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.stuckCalls) != 1 {
		t.Fatalf("RecoverStuckTrades called %d times, want 1", len(rec.stuckCalls))
	}
	if len(rec.staleCalls) != 1 {
		t.Fatalf("RetryFailedWebhooks called %d times, want 1", len(rec.staleCalls))
	}
	// Defaults: stuckAfter 5m, staleAfter 5m.
	if rec.stuckCalls[0] != 5*time.Minute {
		t.Errorf("stuckAfter = %s, want 5m", rec.stuckCalls[0])
	}
	if rec.staleCalls[0] != 5*time.Minute {
		t.Errorf("staleAfter = %s, want 5m", rec.staleCalls[0])
	}
}

func TestPMRecoveryJob_NameAndInterval(t *testing.T) {
	j := NewPMRecoveryJob(&fakePMRecoverer{})
	if j.Name() != "pm-recovery" {
		t.Errorf("Name = %q, want pm-recovery", j.Name())
	}
	if j.Interval() != 2*time.Minute {
		t.Errorf("default interval = %s, want 2m", j.Interval())
	}
	custom := j.WithInterval(30 * time.Second)
	if custom.Interval() != 30*time.Second {
		t.Errorf("WithInterval override = %s, want 30s", custom.Interval())
	}
}

func TestPMRecoveryJob_ReturnsFirstErrorButRunsBoth(t *testing.T) {
	rec := &fakePMRecoverer{stuckErr: errors.New("stuck boom")}
	j := NewPMRecoveryJob(rec)
	err := j.Run(context.Background())
	if err == nil {
		t.Fatal("want error when RecoverStuckTrades fails")
	}
	// Both must still run — one bad sub-task must not skip the other.
	if len(rec.staleCalls) != 1 {
		t.Fatalf("RetryFailedWebhooks should still run after stuck error; calls=%d", len(rec.staleCalls))
	}
}
