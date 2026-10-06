package jobs

import (
	"context"
	"time"
)

// receiptReprober is the company wallet's re-probe (*services.CompanyWallet).
type receiptReprober interface {
	ReprobeReceipts(ctx context.Context)
}

// CompanyWalletReceiptProbe re-runs the receipt probe on the company wallet's
// chains, so one that failed at boot (or later) reopens for quotes once its
// RPC serves receipts again (review I1). It never fails the tick: the wallet
// logs the ALERT itself.
type CompanyWalletReceiptProbe struct {
	w        receiptReprober
	interval time.Duration
}

func NewCompanyWalletReceiptProbe(w receiptReprober, every time.Duration) *CompanyWalletReceiptProbe {
	if every <= 0 {
		every = 5 * time.Minute
	}
	return &CompanyWalletReceiptProbe{w: w, interval: every}
}

func (p *CompanyWalletReceiptProbe) Name() string            { return "company-wallet-receipt-probe" }
func (p *CompanyWalletReceiptProbe) Interval() time.Duration { return p.interval }

func (p *CompanyWalletReceiptProbe) Run(ctx context.Context) error {
	if p.w != nil {
		p.w.ReprobeReceipts(ctx)
	}
	return nil
}
