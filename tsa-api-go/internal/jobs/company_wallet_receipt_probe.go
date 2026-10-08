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

// CompanyWalletReceiptProbe re-runs the receipt probe on the company wallet's
// chains, so one that failed at boot (or later) reopens for quotes once its
// RPC serves receipts again (review I1). It never fails the tick: the wallet
// logs the ALERT itself.
type CompanyWalletReceiptProbe struct {
	name     string
	w        receiptReprober
	interval time.Duration
}

func NewCompanyWalletReceiptProbe(w receiptReprober, every time.Duration) *CompanyWalletReceiptProbe {
	if every <= 0 {
		every = 5 * time.Minute
	}
	return &CompanyWalletReceiptProbe{w: w, interval: every}
}

// NewReceiptProbe is the same re-probe for any reprober under its own name
// (the company wallet's chains, the liquidity pool's gates).
func NewReceiptProbe(name string, w receiptReprober, every time.Duration) *CompanyWalletReceiptProbe {
	p := NewCompanyWalletReceiptProbe(w, every)
	p.name = name
	return p
}

func (p *CompanyWalletReceiptProbe) Name() string {
	if p.name != "" {
		return p.name
	}
	return "company-wallet-receipt-probe"
}
func (p *CompanyWalletReceiptProbe) Interval() time.Duration { return p.interval }

func (p *CompanyWalletReceiptProbe) Run(ctx context.Context) error {
	if p.w != nil {
		p.w.ReprobeReceipts(ctx)
	}
	return nil
}
