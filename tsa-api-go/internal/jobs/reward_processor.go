package jobs

import (
	"context"
	"log"
	"time"
)

// rewardProcessor is the subset of services.RewardService this job needs. An
// interface keeps the job unit-testable without the full service.
type rewardProcessor interface {
	ProcessDirtyUsers(ctx context.Context, limit int) (processed, failed int, err error)
}

// RewardProcessorJob runs the SP5 payout computation (ProcessUserRewards) over
// users with recent TP/cashback activity on a periodic tick. It does NOT scan
// all users blindly — RewardService.ProcessDirtyUsers reads only users flagged
// dirty by the accrual path (a last-activity marker), processes each, and clears
// the flag on success.
//
// IDEMPOTENT: ProcessUserRewards is idempotent (level rewards keyed (user,level),
// cashback keyed per row, claimable is the Σ of those), so a missed tick
// (restart) is recovered next tick and an extra tick is harmless — matching the
// scheduler's Phase-1 in-process contract.
type RewardProcessorJob struct {
	svc      rewardProcessor
	interval time.Duration
	batch    int
}

// NewRewardProcessorJob builds the worker. A non-positive interval falls back to
// 1m and a non-positive batch to 500 so the scheduler (which panics on a
// non-positive interval at Register) always gets sane values.
func NewRewardProcessorJob(svc rewardProcessor, interval time.Duration, batch int) *RewardProcessorJob {
	if interval <= 0 {
		interval = time.Minute
	}
	if batch <= 0 {
		batch = 500
	}
	return &RewardProcessorJob{svc: svc, interval: interval, batch: batch}
}

func (j *RewardProcessorJob) Name() string            { return "reward-processor" }
func (j *RewardProcessorJob) Interval() time.Duration { return j.interval }

func (j *RewardProcessorJob) Run(ctx context.Context) error {
	processed, failed, err := j.svc.ProcessDirtyUsers(ctx, j.batch)
	if err != nil {
		return err
	}
	if processed > 0 || failed > 0 {
		log.Printf("[reward-processor] processed=%d failed=%d", processed, failed)
	}
	return nil
}
