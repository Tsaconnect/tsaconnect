package jobs

import (
	"context"
	"log"
	"time"
)

// billReconcileSvc is the slice of *services.BillService this worker needs.
type billReconcileSvc interface {
	Reconcile(ctx context.Context, grace time.Duration) (int, error)
	IsEnabled() bool
}

// BillReconciler drives non-terminal bill orders to a terminal state: expires
// dead quotes, requeries ClubKonnect for vends whose outcome was lost, and
// retries deferred settles/reverses. Idempotent — every branch in Reconcile
// re-derives state from the DB + vendor, so a missed tick just delays
// resolution.
type BillReconciler struct {
	svc      billReconcileSvc
	interval time.Duration
	grace    time.Duration
}

func NewBillReconciler(svc billReconcileSvc, interval time.Duration) *BillReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	// Grace must comfortably exceed the debit (broadcast + receipt) plus the
	// 75s vend timeout, or the reconciler can race a still-in-flight Pay and
	// re-vend an order whose first vend hasn't answered yet.
	return &BillReconciler{svc: svc, interval: interval, grace: 5 * time.Minute}
}

func (w *BillReconciler) Name() string            { return "bill-reconciler" }
func (w *BillReconciler) Interval() time.Duration { return w.interval }

func (w *BillReconciler) Run(ctx context.Context) error {
	if !w.svc.IsEnabled() {
		return nil
	}
	n, err := w.svc.Reconcile(ctx, w.grace)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("[bill-reconciler] resolved=%d", n)
	}
	return nil
}
