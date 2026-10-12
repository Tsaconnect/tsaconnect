package jobs

import (
	"context"
	"errors"
	"testing"
)

// fakeForwardSweeper records both halves of the tick.
type fakeForwardSweeper struct {
	redriven   int
	redriveErr error
	swept      int
	sweepErr   error

	feeErr error

	redriveCalls int
	sweepCalls   int
	feeCalls     int
}

func (f *fakeForwardSweeper) RedrivePending(context.Context) (int, error) {
	f.redriveCalls++
	return f.redriven, f.redriveErr
}

func (f *fakeForwardSweeper) SweepUnconfirmed(context.Context) (int, error) {
	f.sweepCalls++
	return f.swept, f.sweepErr
}

func (f *fakeForwardSweeper) RecordSettledFees(context.Context) (int, error) {
	f.feeCalls++
	return 0, f.feeErr
}

// The job's original tick only alerted on `submitted` forwards. A forward held
// at `pending` — unreadable balance, short balance, no gateway — had nothing
// driving it anywhere, so a credited merchant was simply never paid.
func TestP2PForwardSweepJob_RedrivesPendingForwards(t *testing.T) {
	f := &fakeForwardSweeper{redriven: 2}
	if err := NewP2PForwardSweepJob(f).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.redriveCalls != 1 {
		t.Fatalf("RedrivePending called %d times, want 1 — pending forwards are never re-driven", f.redriveCalls)
	}
	if f.sweepCalls != 1 {
		t.Fatalf("SweepUnconfirmed called %d times, want 1", f.sweepCalls)
	}
}

// A re-drive that fails must not cost the tick its staleness alerting: the two
// halves answer different questions and an unpaid merchant is visible through
// whichever still works.
func TestP2PForwardSweepJob_StaleAlertingSurvivesARedriveFailure(t *testing.T) {
	f := &fakeForwardSweeper{redriveErr: errors.New("db down"), swept: 1}
	if err := NewP2PForwardSweepJob(f).Run(context.Background()); err == nil {
		t.Fatal("a failed re-drive was reported as a clean tick")
	}
	if f.sweepCalls != 1 {
		t.Fatalf("SweepUnconfirmed called %d times, want 1 — a re-drive failure silenced the staleness alert", f.sweepCalls)
	}
}

// The fee read rides the sweep's cadence (off the disburse webhook); its
// failure never fails the tick.
func TestP2PForwardSweepJob_RecordsFeesWithoutFailingTheTick(t *testing.T) {
	f := &fakeForwardSweeper{feeErr: errors.New("provider 503")}
	if err := NewP2PForwardSweepJob(f).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v, want nil (a fee read is bookkeeping)", err)
	}
	if f.feeCalls != 1 {
		t.Fatalf("RecordSettledFees called %d times, want 1", f.feeCalls)
	}
}
