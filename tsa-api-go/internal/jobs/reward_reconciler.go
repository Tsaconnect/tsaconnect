package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// rewardReconciler is the subset of services.RewardMerkleService this job needs.
// An interface keeps the job unit-testable without the full service / a live RPC.
type rewardReconciler interface {
	Reconcile(ctx context.Context, reader services.RewardChainReader, mcgpToken string) (*services.RewardReconcileResult, error)
}

// RewardReconciler is the SP6 periodic safety net (design §5 SP6 bullet + §9
// money-safety invariants). Once per tick it compares the off-chain published
// claimable ledger against on-chain truth for the live (published) merkle root:
//
//   - DRIFT: any account whose published cumulative is BELOW on-chain claimed
//     (the off-chain ledger regressed under already-paid funds — a bug that
//     bricks the account's future claims) is logged as
//     reward_reconciler_drift_total, mirroring p2p_reconciler_drift_total.
//   - TREASURY: when the distributor's MCGP balance is below the outstanding
//     (Σ published − Σ claimed) owed against the live root, it alerts via
//     reward_treasury_short_total so ops can fund the distributor BEFORE users
//     hit a clean-but-confusing underfunded revert — never partial-silent.
//
// Read-only on-chain (no tx, no key). Idempotent — a converged epoch is a no-op,
// so a missed tick is recovered next tick and an extra tick is harmless (the
// re-run each tick IS the "queue" the spec calls for). Matches the scheduler's
// Phase-1 in-process contract.
//
// GATING: only REGISTERED in main.go when the distributor is configured
// (REWARD_DISTRIBUTOR_ADDRESS set) AND a chain reader (MCGP token + RPC) is
// available. Dormant otherwise — Reconcile itself also no-ops when not wired.
type RewardReconciler struct {
	svc       rewardReconciler
	reader    services.RewardChainReader
	mcgpToken string
	interval  time.Duration
}

// NewRewardReconciler builds the worker. A non-positive interval falls back to
// 1m so the scheduler (which panics on a non-positive interval at Register)
// always gets a sane period.
func NewRewardReconciler(svc rewardReconciler, reader services.RewardChainReader, mcgpToken string, interval time.Duration) *RewardReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &RewardReconciler{svc: svc, reader: reader, mcgpToken: mcgpToken, interval: interval}
}

func (w *RewardReconciler) Name() string            { return "reward-reconciler" }
func (w *RewardReconciler) Interval() time.Duration { return w.interval }

func (w *RewardReconciler) Run(ctx context.Context) error {
	res, err := w.svc.Reconcile(ctx, w.reader, w.mcgpToken)
	if err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	if res.DriftCount > 0 || res.TreasuryShort {
		log.Printf("[reward-reconciler] epoch=%d accounts=%d drift=%d treasuryShort=%t outstanding=%s treasury=%s",
			res.Epoch, res.Accounts, res.DriftCount, res.TreasuryShort,
			res.OutstandingBaseUnits, res.TreasuryBaseUnits)
	}
	return nil
}
