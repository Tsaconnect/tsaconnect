package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The pool gates are both the re-probe job's reprober and the release EOA's
// gas-float reader (plan P13).
var (
	_ receiptReprober    = (*services.PoolChainGates)(nil)
	_ sponsorFloatReader = (*services.PoolChainGates)(nil)
)

type countingReprober struct{ calls int }

func (c *countingReprober) ReprobeReceipts(context.Context) { c.calls++ }

func TestCompanyWalletReceiptProbe_RunsTheReprobe(t *testing.T) {
	c := &countingReprober{}
	p := NewReceiptProbe(CompanyWalletReceiptProbeName, c, 0)
	if p.Name() != "company-wallet-receipt-probe" || p.Interval() != 5*time.Minute {
		t.Fatalf("name=%s interval=%s", p.Name(), p.Interval())
	}
	if err := p.Run(context.Background()); err != nil || c.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
}

func TestNewReceiptProbe_RunsUnderItsOwnName(t *testing.T) {
	c := &countingReprober{}
	p := NewReceiptProbe("liquidity-pool-gates", c, 0)
	if p.Name() != "liquidity-pool-gates" || p.Interval() != 5*time.Minute {
		t.Fatalf("name=%s interval=%s", p.Name(), p.Interval())
	}
	if err := p.Run(context.Background()); err != nil || c.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
}
