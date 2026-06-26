package jobs

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

type fakeBackingReader struct {
	configured bool
	held, owed *big.Int
	err        error
}

func (f *fakeBackingReader) IsConfigured() bool { return f.configured }
func (f *fakeBackingReader) Backing() (*big.Int, *big.Int, error) {
	return f.held, f.owed, f.err
}

func TestMCGPSwapReconciler_Run(t *testing.T) {
	ctx := context.Background()

	// Not configured -> no-op, no error.
	r := NewMCGPSwapReconciler(&fakeBackingReader{configured: false}, nil, time.Minute)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("dormant run: %v", err)
	}

	// Healthy backing (held >= owed).
	r = NewMCGPSwapReconciler(&fakeBackingReader{configured: true, held: big.NewInt(100), owed: big.NewInt(80)}, nil, time.Minute)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("healthy run: %v", err)
	}

	// Shortfall (held < owed) — must still return nil (it alerts, doesn't fail).
	r = NewMCGPSwapReconciler(&fakeBackingReader{configured: true, held: big.NewInt(50), owed: big.NewInt(80)}, nil, time.Minute)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("shortfall run: %v", err)
	}

	// Read error — tolerated (logged, not returned).
	r = NewMCGPSwapReconciler(&fakeBackingReader{configured: true, err: errors.New("rpc down")}, nil, time.Minute)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("read-error run: %v", err)
	}
}

func TestMCGPSwapReconciler_Meta(t *testing.T) {
	r := NewMCGPSwapReconciler(&fakeBackingReader{}, nil, 0)
	if r.Name() != "mcgp-swap-reconciler" {
		t.Fatalf("name: %s", r.Name())
	}
	if r.Interval() != time.Minute {
		t.Fatalf("interval fallback: %s", r.Interval())
	}
}
