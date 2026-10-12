package jobs

import (
	"context"
	"testing"
	"time"
)

type fakeStalePaid struct {
	calls int
	after time.Duration
}

func (f *fakeStalePaid) AlertStalePaidTrades(_ context.Context, d time.Duration, _ int) (int, error) {
	f.calls++
	f.after = d
	return 1, nil
}

func TestP2PPaidUnreleasedAlerter_UsesThreshold(t *testing.T) {
	f := &fakeStalePaid{}
	w := NewP2PPaidUnreleasedAlerter(f, 2*time.Hour)
	if w.Name() != "p2p-paid-unreleased-alerter" || w.Interval() != 5*time.Minute {
		t.Fatal(w.Name(), w.Interval())
	}
	if err := w.Run(context.Background()); err != nil || f.calls != 1 || f.after != 2*time.Hour {
		t.Fatalf("calls=%d after=%v err=%v", f.calls, f.after, err)
	}
	if NewP2PPaidUnreleasedAlerter(f, 0).after != 2*time.Hour {
		t.Fatal("zero threshold must default to 2h")
	}
}
