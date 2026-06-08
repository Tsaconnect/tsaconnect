package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeFMReconciler records calls and returns a canned result/error.
type fakeFMReconciler struct {
	calls  int
	result *services.FeeManagerReconcileResult
	err    error
}

func (f *fakeFMReconciler) ReconcileChains(_ context.Context, _ *config.Config, _ services.FeeManagerChainReader) (*services.FeeManagerReconcileResult, error) {
	f.calls++
	return f.result, f.err
}

func TestFeeManagerReconciler_RunDelegates(t *testing.T) {
	fr := &fakeFMReconciler{result: &services.FeeManagerReconcileResult{
		ChainsChecked: 2, KeysChecked: 24, DriftCount: 1, Refreshed: 1,
	}}
	w := NewFeeManagerReconciler(fr, &config.Config{}, nil, time.Hour)

	if w.Name() != "fee-manager-reconciler" {
		t.Fatalf("name = %q", w.Name())
	}
	if w.Interval() != time.Hour {
		t.Fatalf("interval = %v, want 1h", w.Interval())
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fr.calls != 1 {
		t.Fatalf("calls = %d, want 1", fr.calls)
	}
}

func TestFeeManagerReconciler_RunPropagatesError(t *testing.T) {
	fr := &fakeFMReconciler{err: errors.New("rpc down")}
	w := NewFeeManagerReconciler(fr, &config.Config{}, nil, time.Hour)
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("expected error to propagate")
	}
}

func TestFeeManagerReconciler_NilResultIsNoOp(t *testing.T) {
	fr := &fakeFMReconciler{result: nil}
	w := NewFeeManagerReconciler(fr, &config.Config{}, nil, time.Hour)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("nil result should be a clean no-op, got %v", err)
	}
}

func TestFeeManagerReconciler_DefaultsToHourly(t *testing.T) {
	w := NewFeeManagerReconciler(&fakeFMReconciler{}, &config.Config{}, nil, 0)
	if w.Interval() != time.Hour {
		t.Fatalf("zero interval should default to 1h, got %v", w.Interval())
	}
}
