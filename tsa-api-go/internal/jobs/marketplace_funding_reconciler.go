package jobs

import (
	"context"
	"time"
)

// fundingReconciler is the slice of services.MarketplaceFundingService this
// worker needs.
type fundingReconciler interface {
	Reconcile(ctx context.Context) error
}

// MarketplaceFundingReconciler follows marketplace checkouts funded by a swap
// (services.MarketplaceFundingService.Reconcile): closes unsigned intents,
// proves deliveries the app never confirmed, keeps an in-flight checkout's
// orders from expiring under it, and writes off swaps LiFi ended short. Every
// minute — well inside the one-hour unpaid-order TTL it holds off.
type MarketplaceFundingReconciler struct {
	svc      fundingReconciler
	interval time.Duration
}

func NewMarketplaceFundingReconciler(svc fundingReconciler, interval time.Duration) *MarketplaceFundingReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &MarketplaceFundingReconciler{svc: svc, interval: interval}
}

func (w *MarketplaceFundingReconciler) Name() string            { return "marketplace-funding-reconciler" }
func (w *MarketplaceFundingReconciler) Interval() time.Duration { return w.interval }

func (w *MarketplaceFundingReconciler) Run(ctx context.Context) error {
	return w.svc.Reconcile(ctx)
}
