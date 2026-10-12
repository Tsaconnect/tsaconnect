package jobs

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// PoolSettleDriver is the TSA Liquidity pool's settle step (plan C2).
type PoolSettleDriver interface {
	PendingPoolBuys(ctx context.Context) (received, delivering []uuid.UUID, err error)
	AdvancePoolBuy(ctx context.Context, id uuid.UUID) error
	CompletePoolBooks(ctx context.Context) (int, error)
}

// PoolSettleJob releases, proves, pays LPs and delivers paid pool orders, then
// completes the books and back-fills any LP forward a settle tick missed. Its
// own job (review I-8): a slow release receipt never delays another rail.
// It walks only paid pool orders: the chain reconciler's trade walk and the
// conversion orchestrator's tick (a converted order once handed off) stay
// theirs.
type PoolSettleJob struct {
	d        PoolSettleDriver
	interval time.Duration
	now      func() time.Time
	stalled  *sustainedAlert
	blocked  *sustainedAlert
}

func NewPoolSettleJob(d PoolSettleDriver, interval time.Duration) *PoolSettleJob {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &PoolSettleJob{d: d, interval: interval, now: time.Now,
		stalled: &sustainedAlert{worker: "liquidity-pool-release-stalled", metric: "liquidity_pool_release_stalled",
			what:  "%d paid pool orders with a slice release unproven for 10 minutes — check the release key's gas and the BSC RPC",
			every: time.Hour},
		blocked: &sustainedAlert{worker: "liquidity-pool-settle-blocked", metric: "liquidity_pool_settle_blocked",
			what:  "%d paid pool orders cannot advance (unwired settle side, a refused send, a failed read) — fix it before the slice window ends",
			after: 10 * time.Minute, every: time.Hour},
	}
}

// SetAlerter wires where the job's sustained conditions are reported. Nil logs only.
func (j *PoolSettleJob) SetAlerter(n WorkerFailureNotifier) {
	j.stalled.notifier, j.blocked.notifier = n, n
}
func (j *PoolSettleJob) Name() string            { return "liquidity-pool-settle" }
func (j *PoolSettleJob) Interval() time.Duration { return j.interval }

// Run advances every paid pool order and then runs the books pass, even when
// listing failed. Per-order errors are counted onto the sustained alerts and
// never fail the tick; only a failed listing does, so a dead settle side
// pages through the failure streak.
func (j *PoolSettleJob) Run(ctx context.Context) error {
	received, delivering, listErr := j.d.PendingPoolBuys(ctx)
	stalled, blocked := 0, 0
	for _, id := range slices.Concat(received, delivering) {
		if err := j.d.AdvancePoolBuy(ctx, id); err != nil {
			if errors.Is(err, services.ErrPoolReleaseStalled) {
				stalled++
			} else {
				blocked++
			}
			log.Printf("[liquidity-pool-settle] advance intent=%s: %s", id, services.RedactAlertText(err.Error()))
		}
	}
	if _, err := j.d.CompletePoolBooks(ctx); err != nil {
		log.Printf("[liquidity-pool-settle] books: %s", services.RedactAlertText(err.Error()))
	}
	now := j.now()
	j.stalled.track(ctx, stalled, now)
	j.blocked.track(ctx, blocked, now)
	return listErr
}
