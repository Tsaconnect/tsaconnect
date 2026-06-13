package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRewardProcessor records calls and returns a canned result/error.
type fakeRewardProcessor struct {
	calls     int
	lastLimit int
	processed int
	failed    int
	err       error
}

func (f *fakeRewardProcessor) ProcessDirtyUsers(_ context.Context, limit int) (int, int, error) {
	f.calls++
	f.lastLimit = limit
	return f.processed, f.failed, f.err
}

func TestRewardProcessorJob_RunDelegates(t *testing.T) {
	fp := &fakeRewardProcessor{processed: 3, failed: 1}
	w := NewRewardProcessorJob(fp, time.Minute, 250)

	if w.Name() != "reward-processor" {
		t.Fatalf("name = %q", w.Name())
	}
	if w.Interval() != time.Minute {
		t.Fatalf("interval = %v, want 1m", w.Interval())
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fp.calls != 1 {
		t.Fatalf("calls = %d, want 1", fp.calls)
	}
	if fp.lastLimit != 250 {
		t.Fatalf("batch limit = %d, want 250", fp.lastLimit)
	}
}

func TestRewardProcessorJob_RunPropagatesError(t *testing.T) {
	fp := &fakeRewardProcessor{err: errors.New("boom")}
	w := NewRewardProcessorJob(fp, time.Minute, 100)
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("expected error to propagate")
	}
}

func TestRewardProcessorJob_Defaults(t *testing.T) {
	w := NewRewardProcessorJob(&fakeRewardProcessor{}, 0, 0)
	if w.Interval() != time.Minute {
		t.Fatalf("interval = %v, want 1m fallback", w.Interval())
	}
	// Batch fallback is exercised via Run delegating the default limit.
	fp := &fakeRewardProcessor{}
	w2 := NewRewardProcessorJob(fp, time.Minute, 0)
	_ = w2.Run(context.Background())
	if fp.lastLimit != 500 {
		t.Fatalf("default batch = %d, want 500", fp.lastLimit)
	}
}
