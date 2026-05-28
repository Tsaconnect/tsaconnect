package jobs

import (
	"context"
	"log"
	"time"
)

// merchantStatsReconciler is the subset of MerchantStatsService this job needs.
type merchantStatsReconciler interface {
	ReconcileAllMerchants(ctx context.Context) error
}

// MerchantReputationReconciler recomputes windowed reputation stats nightly
// (correctness backstop for the incremental event path).
type MerchantReputationReconciler struct {
	svc      merchantStatsReconciler
	interval time.Duration
}

func NewMerchantReputationReconciler(svc merchantStatsReconciler) *MerchantReputationReconciler {
	return &MerchantReputationReconciler{svc: svc, interval: 24 * time.Hour}
}

func (r *MerchantReputationReconciler) Name() string            { return "merchant-reputation-reconciler" }
func (r *MerchantReputationReconciler) Interval() time.Duration { return r.interval }

func (r *MerchantReputationReconciler) Run(ctx context.Context) error {
	if err := r.svc.ReconcileAllMerchants(ctx); err != nil {
		return err
	}
	log.Printf("[merchant-reputation-reconciler] reconcile complete")
	return nil
}
