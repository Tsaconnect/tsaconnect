package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeSettler records calls and returns a canned result/error.
type fakeSettler struct {
	calls         int
	result        services.GlobalSettlementResult
	err           error
	strandedTP    float64
	strandedCalls int
}

func (f *fakeSettler) SettleGlobalPoolIfDue(_ context.Context, _ time.Time) (services.GlobalSettlementResult, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeSettler) StrandedGlobalTP(_ context.Context) (float64, error) {
	f.strandedCalls++
	return f.strandedTP, nil
}

func TestGlobalPoolSettler_RunDelegates(t *testing.T) {
	fs := &fakeSettler{result: services.GlobalSettlementResult{Settled: true, UserCount: 5, DeltaTP: 10}}
	w := NewGlobalPoolSettler(fs, time.Hour)

	if w.Name() != "global-pool-settler" {
		t.Fatalf("name = %q", w.Name())
	}
	if w.Interval() != time.Hour {
		t.Fatalf("interval = %v, want 1h", w.Interval())
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fs.calls != 1 {
		t.Fatalf("calls = %d, want 1", fs.calls)
	}
}

func TestGlobalPoolSettler_RunPropagatesError(t *testing.T) {
	fs := &fakeSettler{err: errors.New("boom")}
	w := NewGlobalPoolSettler(fs, time.Minute)
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("expected error to propagate")
	}
}

func TestGlobalPoolSettler_DefaultsNonPositiveInterval(t *testing.T) {
	w := NewGlobalPoolSettler(&fakeSettler{}, 0)
	if w.Interval() != time.Hour {
		t.Fatalf("interval = %v, want 1h fallback", w.Interval())
	}
}
