package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// feeManagerReconciler is the subset of services.FeeConfigService this job needs.
// An interface keeps the job unit-testable without the full service / a live RPC.
type feeManagerReconciler interface {
	ReconcileChains(ctx context.Context, cfg *config.Config, reader services.FeeManagerChainReader) (*services.FeeManagerReconcileResult, error)
}

// FeeManagerReconciler is the hourly fee-rate drift safety net (configurable-
// FeeManager design §10). Once per tick it compares each configured FeeManager
// chain's on-chain rateFor against the fee_config read replica:
//
//   - DRIFT: any (feature, token, chain) whose on-chain rate != the replica is
//     logged as fee_rate_drift_total and the replica is REFRESHED from chain
//     (chain is the source of truth — CQRS, write→chain/read→replica).
//   - READ ERROR: a failed on-chain read KEEPS the last-known-good replica and
//     escalates (fee_rate_read_error_total) — a transient RPC blip must never
//     erase a good rate.
//
// There is NO escrow-getter to reconcile (A1) — only FeeManager↔replica.
//
// Read-only on-chain except the replica refresh (no tx, no key). Idempotent — a
// converged chain is a no-op, so a missed tick is recovered next tick. Matches
// the scheduler's Phase-1 in-process contract.
//
// GATING: only REGISTERED in main.go when ≥1 FeeManager chain is configured AND a
// chain reader is available. Dormant otherwise — ReconcileChains itself also
// no-ops when not wired (gated/no-op when FeeManager address unset).
type FeeManagerReconciler struct {
	svc      feeManagerReconciler
	cfg      *config.Config
	reader   services.FeeManagerChainReader
	interval time.Duration
}

// NewFeeManagerReconciler builds the worker. A non-positive interval falls back
// to 1h so the scheduler (which panics on a non-positive interval at Register)
// always gets a sane period (design §10: runs hourly).
func NewFeeManagerReconciler(svc feeManagerReconciler, cfg *config.Config, reader services.FeeManagerChainReader, interval time.Duration) *FeeManagerReconciler {
	if interval <= 0 {
		interval = time.Hour
	}
	return &FeeManagerReconciler{svc: svc, cfg: cfg, reader: reader, interval: interval}
}

func (w *FeeManagerReconciler) Name() string            { return "fee-manager-reconciler" }
func (w *FeeManagerReconciler) Interval() time.Duration { return w.interval }

func (w *FeeManagerReconciler) Run(ctx context.Context) error {
	res, err := w.svc.ReconcileChains(ctx, w.cfg, w.reader)
	if err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	if res.DriftCount > 0 || res.ReadErrors > 0 {
		log.Printf("[fee-manager-reconciler] chains=%d keys=%d drift=%d refreshed=%d readErrors=%d",
			res.ChainsChecked, res.KeysChecked, res.DriftCount, res.Refreshed, res.ReadErrors)
	}
	return nil
}
