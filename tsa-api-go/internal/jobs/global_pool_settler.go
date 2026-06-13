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
	SettleGlobalPoolEpoch(ctx context.Context) (services.GlobalSettlementResult, error)
}

// GlobalPoolSettler runs the SP4 global-pool settlement epoch on a periodic
// tick (default daily): it folds the global pool's newly-accrued TP into the
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
// 24h so the scheduler (which panics on a non-positive interval at Register)
// always gets a sane period.
func NewGlobalPoolSettler(svc globalPoolSettler, interval time.Duration) *GlobalPoolSettler {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &GlobalPoolSettler{svc: svc, interval: interval}
}

func (g *GlobalPoolSettler) Name() string            { return "global-pool-settler" }
func (g *GlobalPoolSettler) Interval() time.Duration { return g.interval }

func (g *GlobalPoolSettler) Run(ctx context.Context) error {
	res, err := g.svc.SettleGlobalPoolEpoch(ctx)
	if err != nil {
		return err
	}
	if res.Settled {
		log.Printf("[global-pool-settler] epoch settled: deltaTP=%.6f userCount=%d perUserScaled=%d indexAfter=%d",
			res.DeltaTP, res.UserCount, res.PerUserScaled, res.IndexAfter)
	}
	return nil
}
