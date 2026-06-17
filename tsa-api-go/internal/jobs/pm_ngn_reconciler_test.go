package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePMLedgerSource is an in-memory PMNGNLedgerSource for the reconciler test.
type fakePMLedgerSource struct {
	pms      []PMLedgerRow
	listErr  error
	balances map[string]string // subAccountID → Nomba balance
	balErr   map[string]error
}

func (f *fakePMLedgerSource) ActiveLedgerRows(_ context.Context) ([]PMLedgerRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.pms, nil
}

func (f *fakePMLedgerSource) FetchSubAccountBalance(_ context.Context, subAccountID string) (string, error) {
	if e, ok := f.balErr[subAccountID]; ok {
		return "", e
	}
	return f.balances[subAccountID], nil
}

func TestPMNGNReconciler_FlagsDrift(t *testing.T) {
	src := &fakePMLedgerSource{
		pms: []PMLedgerRow{
			{Name: "matched", SubAccountID: "sub-1", AvailableNGN: "1000.00"},
			{Name: "drifted", SubAccountID: "sub-2", AvailableNGN: "1000.00"},
		},
		balances: map[string]string{
			"sub-1": "1000.00", // matches → no drift
			"sub-2": "1234.00", // differs by 234 → drift
		},
	}
	rec := NewPMNGNReconciler(src, time.Minute)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 2 {
		t.Errorf("Checked = %d, want 2", res.Checked)
	}
	if res.DriftCount != 1 {
		t.Errorf("DriftCount = %d, want 1 (sub-2 drifted)", res.DriftCount)
	}
}

// A balance within tolerance must NOT flag drift (rounding at the 2dp boundary).
func TestPMNGNReconciler_WithinTolerance_NoDrift(t *testing.T) {
	src := &fakePMLedgerSource{
		pms: []PMLedgerRow{
			{Name: "ok", SubAccountID: "sub-1", AvailableNGN: "1000.00"},
		},
		balances: map[string]string{"sub-1": "1000.00"},
	}
	rec := NewPMNGNReconciler(src, time.Minute)
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.DriftCount != 0 {
		t.Errorf("DriftCount = %d, want 0", res.DriftCount)
	}
}

// A per-PM balance-fetch failure must not abort the sweep; it is counted as an
// error and the other PMs are still checked.
func TestPMNGNReconciler_FetchError_SkipsRow(t *testing.T) {
	src := &fakePMLedgerSource{
		pms: []PMLedgerRow{
			{Name: "broken", SubAccountID: "sub-1", AvailableNGN: "1000.00"},
			{Name: "ok", SubAccountID: "sub-2", AvailableNGN: "500.00"},
		},
		balances: map[string]string{"sub-2": "500.00"},
		balErr:   map[string]error{"sub-1": errors.New("nomba 503")},
	}
	rec := NewPMNGNReconciler(src, time.Minute)
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Errors != 1 {
		t.Errorf("Errors = %d, want 1", res.Errors)
	}
	if res.DriftCount != 0 {
		t.Errorf("DriftCount = %d, want 0 (sub-2 matches)", res.DriftCount)
	}
}

func TestPMNGNReconciler_WorkerMetadata(t *testing.T) {
	rec := NewPMNGNReconciler(&fakePMLedgerSource{}, 0)
	if rec.Name() != "pm-ngn-reconciler" {
		t.Errorf("Name = %q", rec.Name())
	}
	if rec.Interval() <= 0 {
		t.Errorf("Interval = %v, want positive default", rec.Interval())
	}
	// Run must not error on an empty source.
	if err := rec.Run(context.Background()); err != nil {
		t.Errorf("Run(empty): %v", err)
	}
}
