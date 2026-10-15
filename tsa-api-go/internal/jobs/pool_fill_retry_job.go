package jobs

import (
	"context"
	"log"
	"time"
)

// PoolFillRetrier resumes pool orders parked on a liquidity refusal.
type PoolFillRetrier interface {
	AutoRetryPoolFills(ctx context.Context) (int, error)
}

// PoolFillAutoRetryJob retries, every couple of minutes, the paid pool orders
// whose fill was refused for want of liquidity (an LP withdrew before the
// company refilled), so a refill resumes them without a super admin. The
// settle job then sends their fills. Registered alert-watched next to it.
type PoolFillAutoRetryJob struct {
	r        PoolFillRetrier
	interval time.Duration
}

func NewPoolFillAutoRetryJob(r PoolFillRetrier, interval time.Duration) *PoolFillAutoRetryJob {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return &PoolFillAutoRetryJob{r: r, interval: interval}
}

func (j *PoolFillAutoRetryJob) Name() string            { return "liquidity-pool-fill-auto-retry" }
func (j *PoolFillAutoRetryJob) Interval() time.Duration { return j.interval }

func (j *PoolFillAutoRetryJob) Run(ctx context.Context) error {
	n, err := j.r.AutoRetryPoolFills(ctx)
	if n > 0 {
		log.Printf("[liquidity-pool-fill-auto-retry] resumed %d parked orders", n)
	}
	return err
}
