package jobs

import (
	"context"
	"time"
)

// DepositorSyncer keeps the vault's depositor allow-list in line with the LP
// rows (services.VaultLPService).
type DepositorSyncer interface {
	SyncDepositors(ctx context.Context) (int, error)
}

// LiquidityDepositorSyncJob retries allow-list writes an activation could not
// send and revokes paused LPs' deposits (never their withdrawals).
type LiquidityDepositorSyncJob struct {
	s        DepositorSyncer
	interval time.Duration
}

func NewLiquidityDepositorSyncJob(s DepositorSyncer, interval time.Duration) *LiquidityDepositorSyncJob {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return &LiquidityDepositorSyncJob{s: s, interval: interval}
}

func (j *LiquidityDepositorSyncJob) Name() string            { return "liquidity-depositor-sync" }
func (j *LiquidityDepositorSyncJob) Interval() time.Duration { return j.interval }

func (j *LiquidityDepositorSyncJob) Run(ctx context.Context) error {
	_, err := j.s.SyncDepositors(ctx)
	return err
}
