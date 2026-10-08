package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRenewalSweeper struct {
	calls int
	err   error
}

func (f *fakeRenewalSweeper) SweepVoucherRenewals(context.Context) (int, int, error) {
	f.calls++
	return 1, 0, f.err
}

var _ Worker = (*LiquidityVoucherRenewal)(nil)

func TestLiquidityVoucherRenewal_RunsTheSweep(t *testing.T) {
	f := &fakeRenewalSweeper{}
	w := NewLiquidityVoucherRenewal(f, 0)
	if w.Name() != "liquidity-voucher-renewal" || w.Interval() != 6*time.Hour {
		t.Fatalf("name=%s interval=%s", w.Name(), w.Interval())
	}
	if err := w.Run(context.Background()); err != nil || f.calls != 1 {
		t.Fatalf("run: %v calls=%d", err, f.calls)
	}
	f.err = errors.New("db down")
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("a failed sweep must surface to the scheduler")
	}
}
