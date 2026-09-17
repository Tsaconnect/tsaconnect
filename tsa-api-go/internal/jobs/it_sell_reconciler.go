package jobs

import (
	"context"
	"log"
	"time"
)

// itSellReconcileSvc is the slice of *services.ITService this worker needs.
type itSellReconcileSvc interface {
	ReconcileStuckSells(ctx context.Context, grace time.Duration) (int, error)
}

// ITSellReconciler drives MCGP Instant Transfer sells that stopped between the
// on-chain debit and the naira payout to a terminal state — from the swap
// contract's debit record and Flutterwave's transfer record, never on a guess.
// See services/it_sell_recovery.go for every branch.
//
// Idempotent and safe on more than one instance: each write is guarded on the
// state it read, every chain op is keyed by the intent's single-use debit ref,
// and every payout reuses the intent's one reference, which is Flutterwave's
// idempotency key. A missed tick only delays resolution.
type ITSellReconciler struct {
	svc      itSellReconcileSvc
	interval time.Duration
	grace    time.Duration
}

func NewITSellReconciler(svc itSellReconcileSvc, interval time.Duration) *ITSellReconciler {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	// Grace must outlast a sell still in flight: CreateSellIntent runs detached
	// for up to 3 minutes (debit broadcast, 60s receipt wait, payout call). A
	// shorter grace would let the reconciler re-drive a payout the request is
	// still waiting on.
	return &ITSellReconciler{svc: svc, interval: interval, grace: 5 * time.Minute}
}

func (w *ITSellReconciler) Name() string            { return "it-sell-reconciler" }
func (w *ITSellReconciler) Interval() time.Duration { return w.interval }

func (w *ITSellReconciler) Run(ctx context.Context) error {
	n, err := w.svc.ReconcileStuckSells(ctx, w.grace)
	if n > 0 {
		log.Printf("[it-sell-reconciler] resolved=%d", n)
	}
	return err
}
