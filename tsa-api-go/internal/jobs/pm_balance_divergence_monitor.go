package jobs

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"
)

// divergenceChecker is the services-side comparison this worker drives.
// services.CheckPMBalanceDivergence satisfies it.
type divergenceChecker func(ctx context.Context, db *gorm.DB) ([]string, error)

// PMBalanceDivergenceMonitor compares each payment merchant's legacy
// available_ngn/reserved_ngn against its pm_balances NGN row, and alerts on any
// disagreement.
//
// It exists because the dual-write window is the only place a drift can be
// introduced, and nothing else in the system would notice one. The existing NGN
// reconciler compares an aggregate against the provider's parent balance — it
// has no view of the two internal representations at all.
//
// The timing is what makes this worth a worker rather than a test: a drift that
// goes undetected until the legacy columns are dropped is a drift that rolling
// back can no longer fix, because the columns you would roll back to are the
// ones that drifted. Catching it while both representations are still written
// is the entire window of opportunity.
//
// Read-only. It never mutates either representation — reconciling automatically
// would pick a winner between two records that disagree about money, which is an
// operator's decision, not a background job's.
type PMBalanceDivergenceMonitor struct {
	db       *gorm.DB
	check    divergenceChecker
	interval time.Duration
}

func NewPMBalanceDivergenceMonitor(db *gorm.DB, check divergenceChecker, interval time.Duration) *PMBalanceDivergenceMonitor {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &PMBalanceDivergenceMonitor{db: db, check: check, interval: interval}
}

func (w *PMBalanceDivergenceMonitor) Name() string            { return "pm-balance-divergence-monitor" }
func (w *PMBalanceDivergenceMonitor) Interval() time.Duration { return w.interval }

func (w *PMBalanceDivergenceMonitor) Run(ctx context.Context) error {
	if w.db == nil || w.check == nil {
		return nil
	}
	found, err := w.check(ctx, w.db)
	if err != nil {
		return err // logged by the scheduler; transient DB failures self-heal
	}
	if len(found) == 0 {
		return nil
	}
	log.Printf("[pm-balance-divergence] ALERT pm_balance_divergence_total=%d — the legacy NGN "+
		"columns and pm_balances disagree; do NOT drop the legacy columns until this is zero", len(found))
	for _, d := range found {
		log.Printf("[pm-balance-divergence] %s", d)
	}
	return nil
}
