package jobs

import (
	"context"
	"time"
)

// receiptReprober is a re-probe: the company wallet's (*services.CompanyWallet)
// or the liquidity pool's gates (*services.PoolChainGates).
type receiptReprober interface {
	ReprobeReceipts(ctx context.Context)
}

// ReceiptProbe re-runs a receipt probe, so a chain that failed it at boot (or
// later) reopens once its RPC serves receipts again (review I1). It never fails
// the tick: the reprober logs the ALERT itself.
type ReceiptProbe struct {
	name     string
	w        receiptReprober
	interval time.Duration
}

// CompanyWalletReceiptProbeName is the company wallet's job name, unchanged
// since before the probe served the pool too (scheduler logs key on it).
const CompanyWalletReceiptProbeName = "company-wallet-receipt-probe"

// NewReceiptProbe re-probes w every interval (5 minutes when unset) under name
// (the company wallet's chains, the liquidity pool's gates).
func NewReceiptProbe(name string, w receiptReprober, every time.Duration) *ReceiptProbe {
	if every <= 0 {
		every = 5 * time.Minute
	}
	return &ReceiptProbe{name: name, w: w, interval: every}
}

func (p *ReceiptProbe) Name() string            { return p.name }
func (p *ReceiptProbe) Interval() time.Duration { return p.interval }

func (p *ReceiptProbe) Run(ctx context.Context) error {
	if p.w != nil {
		p.w.ReprobeReceipts(ctx)
	}
	return nil
}
