package jobs

import (
	"context"
	"log"
	"time"
)

// orderMcgpReconcileSvc is the slice of *services.OrderSpendablePaymentService
// this worker needs.
type orderMcgpReconcileSvc interface {
	Reconcile(ctx context.Context) (int, error)
	IsEnabled() bool
}

// OrderMcgpReconciler drives marketplace orders paid from Spendable MCGP to a
// settled state on-chain: adopts or fails debits whose confirmation was lost,
// returns debits that landed after their order closed, finishes releases and
// refunds interrupted between chain calls, and resolves holds whose escrow
// window ran out (as ProductEscrow's autoRefund would). Every branch re-reads
// chain state before acting, so a missed or repeated tick only delays.
type OrderMcgpReconciler struct {
	svc      orderMcgpReconcileSvc
	interval time.Duration
}

func NewOrderMcgpReconciler(svc orderMcgpReconcileSvc, interval time.Duration) *OrderMcgpReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &OrderMcgpReconciler{svc: svc, interval: interval}
}

func (w *OrderMcgpReconciler) Name() string            { return "order-mcgp-reconciler" }
func (w *OrderMcgpReconciler) Interval() time.Duration { return w.interval }

func (w *OrderMcgpReconciler) Run(ctx context.Context) error {
	if !w.svc.IsEnabled() {
		return nil
	}
	n, err := w.svc.Reconcile(ctx)
	if n > 0 {
		log.Printf("[order-mcgp-reconciler] moved=%d", n)
	}
	return err
}
