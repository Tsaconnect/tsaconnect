package jobs

import (
	"context"
	"testing"
	"time"
)

type countingReprober struct{ calls int }

func (c *countingReprober) ReprobeReceipts(context.Context) { c.calls++ }

func TestCompanyWalletReceiptProbe_RunsTheReprobe(t *testing.T) {
	c := &countingReprober{}
	p := NewCompanyWalletReceiptProbe(c, 0)
	if p.Name() != "company-wallet-receipt-probe" || p.Interval() != 5*time.Minute {
		t.Fatalf("name=%s interval=%s", p.Name(), p.Interval())
	}
	if err := p.Run(context.Background()); err != nil || c.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
}
