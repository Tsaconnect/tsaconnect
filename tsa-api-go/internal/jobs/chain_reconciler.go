package jobs

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// TradeReconciler is the slice of P2PTradeService the worker depends on. Kept
// narrow so tests can fake it without standing up the full service. The
// services package doesn't import jobs (no cycle) so we can refer to its
// concrete types directly.
type TradeReconciler interface {
	ListNonTerminalTrades(ctx context.Context, limit int) ([]models.P2PTrade, error)
	ReconcileTrade(ctx context.Context, tradeID uuid.UUID) (services.ReconcileResult, error)
}

// ChainReconciler is the periodic worker that walks non-terminal trades and
// reconciles DB state with on-chain truth. Phase-1 safety net for the
// prepare/confirm flow: when a Confirm* response drops, the user's UI may
// not retry; this worker eventually catches up.
//
// Idempotent — converged trades are a no-op. Runs every 30s by default;
// override via WithInterval for tests.
type ChainReconciler struct {
	svc      TradeReconciler
	interval time.Duration
}

func NewChainReconciler(svc TradeReconciler) *ChainReconciler {
	return &ChainReconciler{svc: svc, interval: 30 * time.Second}
}

func (w *ChainReconciler) WithInterval(d time.Duration) *ChainReconciler {
	cp := *w
	cp.interval = d
	return &cp
}

func (w *ChainReconciler) Name() string            { return "p2p-chain-reconciler" }
func (w *ChainReconciler) Interval() time.Duration { return w.interval }

func (w *ChainReconciler) Run(ctx context.Context) error {
	trades, err := w.svc.ListNonTerminalTrades(ctx, 200)
	if err != nil {
		return err
	}
	if len(trades) == 0 {
		return nil
	}
	driftCount := 0
	for _, t := range trades {
		// Respect ctx cancellation between trades — chain reads can be slow.
		if err := ctx.Err(); err != nil {
			log.Printf("[chain-reconciler] aborting mid-pass: %v", err)
			return err
		}
		out, err := w.svc.ReconcileTrade(ctx, t.ID)
		if err != nil {
			log.Printf("[chain-reconciler] trade=%s error: %v", t.ID, err)
			continue
		}
		if out.Changed {
			driftCount++
			log.Printf("[chain-reconciler] trade=%s drift %s -> %s (%s)",
				t.ID, out.FromStatus, out.ToStatus, out.Reason)
		}
	}
	if driftCount > 0 {
		log.Printf("[chain-reconciler] reconciled %d/%d trades", driftCount, len(trades))
	}
	return nil
}
