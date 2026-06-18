package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePMLedgerSource is an in-memory PMNGNLedgerSource for the aggregate
// reconciler test: a fixed Σ available_ngn and a fixed parent balance.
type fakePMLedgerSource struct {
	sumNGN    string
	pmCount   int
	sumErr    error
	parentNGN string
	parentErr error
}

func (f *fakePMLedgerSource) SumActiveAvailableNGN(_ context.Context) (string, int, error) {
	if f.sumErr != nil {
		return "", 0, f.sumErr
	}
	return f.sumNGN, f.pmCount, nil
}

func (f *fakePMLedgerSource) FetchParentBalance(_ context.Context) (string, error) {
	if f.parentErr != nil {
		return "", f.parentErr
	}
	return f.parentNGN, nil
}

// Σ ledger == parent balance (within tolerance) → no drift.
func TestPMNGNReconciler_NoDrift(t *testing.T) {
	src := &fakePMLedgerSource{sumNGN: "3500.00", pmCount: 2, parentNGN: "3500.00"}
	rec := NewPMNGNReconciler(src, time.Minute)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1", res.Checked)
	}
	if res.DriftCount != 0 {
		t.Errorf("DriftCount = %d, want 0", res.DriftCount)
	}
	if res.PMCount != 2 {
		t.Errorf("PMCount = %d, want 2", res.PMCount)
	}
}

// Σ ledger diverges from the parent balance beyond tolerance → drift flagged.
func TestPMNGNReconciler_FlagsAggregateDrift(t *testing.T) {
	src := &fakePMLedgerSource{sumNGN: "3500.00", pmCount: 2, parentNGN: "3266.00"} // off by 234
	rec := NewPMNGNReconciler(src, time.Minute)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1", res.Checked)
	}
	if res.DriftCount != 1 {
		t.Errorf("DriftCount = %d, want 1 (aggregate drift)", res.DriftCount)
	}
}

// A balance within tolerance must NOT flag drift (rounding at the 2dp boundary).
func TestPMNGNReconciler_WithinTolerance_NoDrift(t *testing.T) {
	src := &fakePMLedgerSource{sumNGN: "1000.00", pmCount: 1, parentNGN: "1000.50"} // 50 kobo < 100
	rec := NewPMNGNReconciler(src, time.Minute)
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.DriftCount != 0 {
		t.Errorf("DriftCount = %d, want 0 (within tolerance)", res.DriftCount)
	}
}

// A parent-balance fetch failure must not abort the tick; it is counted as an
// error and surfaced in the result (no drift recorded).
func TestPMNGNReconciler_ParentFetchError_CountsError(t *testing.T) {
	src := &fakePMLedgerSource{sumNGN: "1000.00", pmCount: 1, parentErr: errors.New("nomba 503")}
	rec := NewPMNGNReconciler(src, time.Minute)
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Errors != 1 {
		t.Errorf("Errors = %d, want 1", res.Errors)
	}
	if res.DriftCount != 0 {
		t.Errorf("DriftCount = %d, want 0", res.DriftCount)
	}
	if res.Checked != 0 {
		t.Errorf("Checked = %d, want 0 (no comparison ran)", res.Checked)
	}
}

// A sum-fetch failure is returned (the PM list itself can't be read).
func TestPMNGNReconciler_SumError_Returns(t *testing.T) {
	src := &fakePMLedgerSource{sumErr: errors.New("db down")}
	rec := NewPMNGNReconciler(src, time.Minute)
	if _, err := rec.RunOnce(context.Background()); err == nil {
		t.Fatal("expected error when SumActiveAvailableNGN fails, got nil")
	}
}

// TestPMNGNReconciler_LedgerLessThanParent_IsHealthy is the Prove-It test for
// FIX 2: when Σ available_ngn is LESS than the parent balance (the normal
// commingled-parent case — parent holds PMs' VA funds + TSA float), the
// reconciler must NOT flag drift. Before the fix abs64(diff) > tolerance fires
// on the negative direction, producing alarm fatigue on every tick.
func TestPMNGNReconciler_LedgerLessThanParent_IsHealthy(t *testing.T) {
	// Σ = 1000 NGN, parent = 3500 NGN (2500 NGN surplus = TSA float). This is the
	// normal healthy state in a commingled-parent model.
	src := &fakePMLedgerSource{sumNGN: "1000.00", pmCount: 1, parentNGN: "3500.00"}
	rec := NewPMNGNReconciler(src, time.Minute)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1", res.Checked)
	}
	// Healthy: ledger < parent → no drift (parent surplus is TSA float, not a problem).
	if res.DriftCount != 0 {
		t.Errorf("DriftCount = %d, want 0 (ledger < parent is healthy, must not alarm)", res.DriftCount)
	}
}

// TestPMNGNReconciler_LedgerExceedsParent_FlagsDrift verifies the over-credit
// direction (FIX 2): when Σ available_ngn exceeds the parent balance beyond
// tolerance, drift IS flagged — we claim more than Nomba holds.
func TestPMNGNReconciler_LedgerExceedsParent_FlagsDrift(t *testing.T) {
	// Σ = 3500 NGN, parent = 1000 NGN → ledger overclaims by 2500 NGN — real danger.
	src := &fakePMLedgerSource{sumNGN: "3500.00", pmCount: 2, parentNGN: "1000.00"}
	rec := NewPMNGNReconciler(src, time.Minute)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1", res.Checked)
	}
	if res.DriftCount != 1 {
		t.Errorf("DriftCount = %d, want 1 (ledger > parent is over-credit, must alarm)", res.DriftCount)
	}
}

func TestPMNGNReconciler_WorkerMetadata(t *testing.T) {
	rec := NewPMNGNReconciler(&fakePMLedgerSource{parentNGN: "0.00", sumNGN: "0.00"}, 0)
	if rec.Name() != "pm-ngn-reconciler" {
		t.Errorf("Name = %q", rec.Name())
	}
	if rec.Interval() <= 0 {
		t.Errorf("Interval = %v, want positive default", rec.Interval())
	}
	// Run must not error on a zero/zero source.
	if err := rec.Run(context.Background()); err != nil {
		t.Errorf("Run(zero): %v", err)
	}
}
