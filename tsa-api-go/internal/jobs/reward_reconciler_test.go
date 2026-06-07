package jobs

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeRewardReconciler records calls and returns a canned result/error.
type fakeRewardReconciler struct {
	calls  int
	result *services.RewardReconcileResult
	err    error
}

func (f *fakeRewardReconciler) Reconcile(_ context.Context, _ services.RewardChainReader, _ string) (*services.RewardReconcileResult, error) {
	f.calls++
	return f.result, f.err
}

func TestRewardReconciler_RunDelegates(t *testing.T) {
	fr := &fakeRewardReconciler{result: &services.RewardReconcileResult{
		Epoch: 4, Accounts: 2, DriftCount: 1, TreasuryShort: true,
		OutstandingBaseUnits: big.NewInt(350), TreasuryBaseUnits: big.NewInt(200),
		ShortfallBaseUnits: big.NewInt(150),
	}}
	w := NewRewardReconciler(fr, nil, "0xmcgp", time.Minute)

	if w.Name() != "reward-reconciler" {
		t.Fatalf("name = %q", w.Name())
	}
	if w.Interval() != time.Minute {
		t.Fatalf("interval = %v, want 1m", w.Interval())
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fr.calls != 1 {
		t.Fatalf("calls = %d, want 1", fr.calls)
	}
}

func TestRewardReconciler_RunPropagatesError(t *testing.T) {
	fr := &fakeRewardReconciler{err: errors.New("rpc down")}
	w := NewRewardReconciler(fr, nil, "0xmcgp", time.Minute)
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("expected error to propagate")
	}
}

func TestRewardReconciler_NilResultIsNoOp(t *testing.T) {
	fr := &fakeRewardReconciler{result: nil}
	w := NewRewardReconciler(fr, nil, "0xmcgp", time.Minute)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("nil result should be a clean no-op, got %v", err)
	}
}

func TestRewardReconciler_IntervalDefault(t *testing.T) {
	w := NewRewardReconciler(&fakeRewardReconciler{}, nil, "0xmcgp", 0)
	if w.Interval() != time.Minute {
		t.Fatalf("interval = %v, want 1m fallback", w.Interval())
	}
}
