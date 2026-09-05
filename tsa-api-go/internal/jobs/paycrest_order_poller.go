package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PaycrestOrderSyncer is the narrow slice of PaycrestOrderService this needs.
// *services.PaycrestOrderService satisfies it; tests inject a fake.
type PaycrestOrderSyncer interface {
	DueForSync(ctx context.Context, limit int) ([]models.PaycrestOrderRecord, error)
	Sync(ctx context.Context, row *models.PaycrestOrderRecord) (*models.PaycrestOrderRecord, error)
}

// PaycrestOrderPoller advances brokered ramp orders.
//
// Paycrest delivers webhooks, but only to a public endpoint we have not stood
// up yet. Until then this poller is the ONLY thing that moves an order from
// "awaiting payment" to settled — without it a user who has paid sees nothing
// change. That makes it load-bearing rather than a safety net, which is why
// the tick is 20s rather than the minutes a reconciler would use.
//
// It is also the only place the unmatched-deposit failure is detected. Sync
// alarms on that state; this worker is what causes Sync to run at all.
type PaycrestOrderPoller struct {
	svc      PaycrestOrderSyncer
	interval time.Duration
	// batch bounds one tick's work so a backlog cannot make a tick outrun the
	// interval — the scheduler skips rather than queues, so a slow tick would
	// silently halve the effective poll rate.
	batch int
}

// NewPaycrestOrderPoller builds the worker. A non-positive interval falls back
// to 20s so the scheduler (which panics on a non-positive interval) always
// gets a sane period.
func NewPaycrestOrderPoller(svc PaycrestOrderSyncer, interval time.Duration) *PaycrestOrderPoller {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	return &PaycrestOrderPoller{svc: svc, interval: interval, batch: 25}
}

func (w *PaycrestOrderPoller) Name() string            { return "paycrest-order-poller" }
func (w *PaycrestOrderPoller) Interval() time.Duration { return w.interval }

func (w *PaycrestOrderPoller) Run(ctx context.Context) error {
	rows, err := w.svc.DueForSync(ctx, w.batch)
	if err != nil {
		return err
	}
	var advanced, unmatched int
	for i := range rows {
		// Shutdown must not leave a half-processed batch spinning.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		before := rows[i].Status
		after, err := w.svc.Sync(ctx, &rows[i])
		if err != nil {
			// One order failing to read must not stop the batch — a single
			// stuck order would otherwise freeze every other user's progress.
			log.Printf("[paycrest poller] sync %s (ref=%s): %v",
				rows[i].ID, rows[i].Reference, err)
			continue
		}
		if after != nil && after.Status != before {
			advanced++
			if after.Status == models.PaycrestStatusUnmatched {
				unmatched++
			}
		}
	}
	if advanced > 0 {
		log.Printf("[paycrest poller] checked=%d advanced=%d unmatched=%d",
			len(rows), advanced, unmatched)
	}
	return nil
}
