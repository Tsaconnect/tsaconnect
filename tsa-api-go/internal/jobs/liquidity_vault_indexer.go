package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// VaultLedger is the shared-pool vault's ledger: index final blocks, then
// reconcile them against the vault's books (spec 2026-10-14 §4.4).
type VaultLedger interface {
	IndexOnce(ctx context.Context) (int, error)
	Reconcile(ctx context.Context) (*services.VaultReconcileReport, error)
}

// LiquidityVaultIndexerJob indexes and reconciles every tick. A failure to
// index or reconcile fails the tick (failure-streak alerted); findings —
// drift, an unknown fill, high utilisation — are ALERTs from the reconciler.
type LiquidityVaultIndexerJob struct {
	l        VaultLedger
	interval time.Duration
}

func NewLiquidityVaultIndexerJob(l VaultLedger, interval time.Duration) *LiquidityVaultIndexerJob {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &LiquidityVaultIndexerJob{l: l, interval: interval}
}

func (j *LiquidityVaultIndexerJob) Name() string            { return "liquidity-vault-indexer" }
func (j *LiquidityVaultIndexerJob) Interval() time.Duration { return j.interval }

func (j *LiquidityVaultIndexerJob) Run(ctx context.Context) error {
	n, err := j.l.IndexOnce(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("[liquidity-vault-indexer] indexed %d events", n)
	}
	_, err = j.l.Reconcile(ctx)
	return err
}
