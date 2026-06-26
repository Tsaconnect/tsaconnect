package jobs

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// mcgpSwapBackingReader is the subset of services.MCGPSwapService this job needs
// (an interface keeps it unit-testable without a live RPC).
type mcgpSwapBackingReader interface {
	IsConfigured() bool
	Backing() (held, owed *big.Int, err error)
}

// MCGPSwapReconciler is the read-only safety net for McgpEntitlementSwap
// ("Swap Contract B"). Once per tick it:
//
//   - asserts the money invariant: on-chain MCGP held >= premium owed
//     (accountedMcgp). A shortfall is logged as mcgp_swap_backing_drift_total —
//     this should be IMPOSSIBLE (buyPremium enforces backing on-chain), so any
//     drift is a paging-level alert.
//   - counts stale pending ledger rows (prepared but never confirmed past a TTL)
//     and logs mcgp_swap_stale_pending_total so dropped confirms are visible.
//
// Deliberately READ-ONLY: per the security review it never moves money or
// "corrects" balances — on-chain is the source of truth; divergence is an alert
// for a human, not an instruction to credit/debit. Idempotent; a missed tick is
// recovered next tick. Registered only when the swap contract is configured.
type MCGPSwapReconciler struct {
	svc          mcgpSwapBackingReader
	db           *gorm.DB
	interval     time.Duration
	stalePending time.Duration
}

// NewMCGPSwapReconciler builds the worker. Non-positive interval falls back to
// 1m (the scheduler panics on a non-positive interval at Register).
func NewMCGPSwapReconciler(svc mcgpSwapBackingReader, db *gorm.DB, interval time.Duration) *MCGPSwapReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &MCGPSwapReconciler{svc: svc, db: db, interval: interval, stalePending: 15 * time.Minute}
}

func (w *MCGPSwapReconciler) Name() string            { return "mcgp-swap-reconciler" }
func (w *MCGPSwapReconciler) Interval() time.Duration { return w.interval }

func (w *MCGPSwapReconciler) Run(ctx context.Context) error {
	if w.svc == nil || !w.svc.IsConfigured() {
		return nil
	}

	// 1. Backing invariant (read-only).
	held, owed, err := w.svc.Backing()
	if err != nil {
		// A transient RPC blip must not be treated as a shortfall.
		log.Printf("[mcgp-swap-reconciler] mcgp_swap_backing_read_error_total=1 err=%v", err)
	} else if held.Cmp(owed) < 0 {
		shortfall := new(big.Int).Sub(owed, held)
		log.Printf("[mcgp-swap-reconciler] ALERT mcgp_swap_backing_drift_total=1 held=%s owed=%s shortfall=%s",
			held.String(), owed.String(), shortfall.String())
	}

	// 2. Stale pending ledger rows (read-only count).
	if w.db != nil {
		cutoff := time.Now().Add(-w.stalePending)
		var stale int64
		if e := w.db.WithContext(ctx).Model(&models.MCGPSwapLedgerEntry{}).
			Where("status = ? AND created_at < ?", models.MCGPLedgerPending, cutoff).
			Count(&stale).Error; e != nil {
			log.Printf("[mcgp-swap-reconciler] stale-pending query failed: %v", e)
		} else if stale > 0 {
			log.Printf("[mcgp-swap-reconciler] mcgp_swap_stale_pending_total=%d (prepared but unconfirmed > %s)",
				stale, w.stalePending)
		}
	}
	return nil
}
