package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeTicker struct {
	err   error
	calls int
}

func (f *fakeTicker) Tick(ctx context.Context, limit int) error {
	f.calls++
	return f.err
}

func TestConversionOrchestratorWorker_TicksAndSurvivesAnError(t *testing.T) {
	fake := &fakeTicker{err: errors.New("rpc down")}
	w := NewConversionOrchestratorWorker(fake, 30*time.Second)
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("the worker must surface the error so the scheduler logs it")
	}
	if fake.calls != 1 || w.Interval() != 30*time.Second || w.Name() != "conversion-orchestrator" {
		t.Fatalf("worker = %s/%s calls=%d", w.Name(), w.Interval(), fake.calls)
	}
}

func TestConversionOrchestratorWorker_NilSvcIsDormantNotPanic(t *testing.T) {
	w := NewConversionOrchestratorWorker(nil, 30*time.Second)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("unconfigured orchestrator should be dormant, got err: %v", err)
	}
}
