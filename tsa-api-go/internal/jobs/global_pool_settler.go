package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// globalPoolSettler is the subset of FeeDistributionService this job needs.
// An interface keeps the job unit-testable without the full service.
type globalPoolSettler interface {
	SettleGlobalPoolIfDue(ctx context.Context, now time.Time) (services.GlobalSettlementResult, error)
	StrandedGlobalTP(ctx context.Context) (float64, error)
}

// GlobalPoolSettler is the monthly global-pool payout. It ticks often (default
// hourly) but only folds when the payout day (24th, 00:00 WAT) has passed and
// that month has not been paid, so an outage over the 24th is made up on the
// next tick. A fold folds the global pool's newly-accrued TP into the
// per-user cumulative index — ONE scalar write per epoch, independent of the
// user count.
//
// IDEMPOTENT: SettleGlobalPoolEpoch no-ops when there's no new accrual
// (IndexedTP high-water mark guards against double-folding the same delta), so
// a missed tick (restart) is recovered by the next tick and an extra tick is
// harmless — matching the scheduler's Phase-1 in-process contract.
type GlobalPoolSettler struct {
	svc      globalPoolSettler
	interval time.Duration
}

// NewGlobalPoolSettler builds the worker. A non-positive interval falls back to
// 1h so the scheduler (which panics on a non-positive interval at Register)
// always gets a sane period.
func NewGlobalPoolSettler(svc globalPoolSettler, interval time.Duration) *GlobalPoolSettler {
	if interval <= 0 {
		interval = time.Hour
	}
	return &GlobalPoolSettler{svc: svc, interval: interval}
}

func (g *GlobalPoolSettler) Name() string            { return "global-pool-settler" }
func (g *GlobalPoolSettler) Interval() time.Duration { return g.interval }

func (g *GlobalPoolSettler) Run(ctx context.Context) error {
	res, err := g.svc.SettleGlobalPoolIfDue(ctx, time.Now())
	if err != nil {
		return err
	}
	if res.Settled {
		log.Printf("[global-pool-settler] epoch settled: deltaTP=%.6f userCount=%d perUserScaled=%d indexAfter=%d",
			res.DeltaTP, res.UserCount, res.PerUserScaled, res.IndexAfter)
	}
	// Drift gauge: TP folded for users soft-deleted before settlement is never
	// reclaimed (deferred), so surface it each tick so the leak stays visible and
	// bounded. Best-effort — a measurement error must not fail the settle tick.
	if stranded, err := g.svc.StrandedGlobalTP(ctx); err != nil {
		log.Printf("[global-pool-settler] stranded-TP measure failed: %v", err)
	} else if stranded > 0 {
		log.Printf("[global-pool-settler] global_pool_stranded_tp=%.6f (TP owed to soft-deleted users; not yet reclaimed)", stranded)
	}
	return nil
}
