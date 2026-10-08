package jobs

import (
	"context"
	"log"
	"time"
)

// voucherRenewalSweeper is the slice of *services.P2PTradeService this worker needs.
type voucherRenewalSweeper interface {
	SweepVoucherRenewals(ctx context.Context) (notified, closed int, err error)
}

// LiquidityVoucherRenewal reminds LPs to renew stock-ad vouchers and closes
// expired and superseded ones (TSA Liquidity spec §8.4 step 6). Idempotent:
// notices are spaced by an audit row, closes are status- and trade-guarded.
//
// A voucher that lapses unnoticed drops the LP's stock out of the pool, so it
// is registered with AlertOnFailureStreak.
type LiquidityVoucherRenewal struct {
	svc      voucherRenewalSweeper
	interval time.Duration
}

func NewLiquidityVoucherRenewal(svc voucherRenewalSweeper, interval time.Duration) *LiquidityVoucherRenewal {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &LiquidityVoucherRenewal{svc: svc, interval: interval}
}

func (w *LiquidityVoucherRenewal) Name() string            { return "liquidity-voucher-renewal" }
func (w *LiquidityVoucherRenewal) Interval() time.Duration { return w.interval }

func (w *LiquidityVoucherRenewal) Run(ctx context.Context) error {
	notified, closed, err := w.svc.SweepVoucherRenewals(ctx)
	if notified > 0 || closed > 0 {
		log.Printf("[liquidity] METRIC liquidity_voucher_expiring notified=%d closed=%d", notified, closed)
	}
	return err
}
