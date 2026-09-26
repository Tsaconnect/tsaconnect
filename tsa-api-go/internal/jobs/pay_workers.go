package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// PayWebhookDeliverer is the narrow slice of *services.PayWebhookSender this
// worker needs. Kept as an interface so tests inject a fake instead of a
// real HTTP-calling sender.
type PayWebhookDeliverer interface {
	DeliverDue(ctx context.Context, limit int) (int, error)
	PurgeOld(ctx context.Context, retention time.Duration, limit int) (int64, error)
}

// PayWebhookWorker delivers due TSA Pay webhook events on a schedule — the
// only thing that ever calls a merchant's webhook URL; a payment's state
// change only enqueues the delivery row (see PayPaymentService.transition).
type PayWebhookWorker struct {
	sender   PayWebhookDeliverer
	interval time.Duration
	// batch bounds one tick's work so a backlog cannot make a tick outrun
	// the interval — the scheduler skips rather than queues.
	batch int
}

// NewPayWebhookWorker builds the worker. A non-positive interval falls back
// to 15s so the scheduler (which panics on a non-positive interval) always
// gets a sane period.
func NewPayWebhookWorker(sender PayWebhookDeliverer, interval time.Duration) *PayWebhookWorker {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &PayWebhookWorker{sender: sender, interval: interval, batch: services.PayWebhookDefaultBatch}
}

func (w *PayWebhookWorker) Name() string            { return "tsa-pay-webhooks" }
func (w *PayWebhookWorker) Interval() time.Duration { return w.interval }

func (w *PayWebhookWorker) Run(ctx context.Context) error {
	_, deliverErr := w.sender.DeliverDue(ctx, w.batch)
	// Retention runs even when delivery failed: the two are independent, and
	// a failing endpoint must not let finished rows pile up.
	_, purgeErr := w.sender.PurgeOld(ctx, services.PayWebhookRetention, services.PayWebhookPurgeBatch)
	return errors.Join(deliverErr, purgeErr)
}

// PayReconcileRunner is the narrow slice of *services.PayPaymentService this
// worker needs.
type PayReconcileRunner interface {
	ReconcileTick(ctx context.Context) error
}

// PayReconciler runs TSA Pay's periodic reconciliation tick: the expiry sweep,
// the stuck-test-payment release, and settlement of every in-flight live
// payment against its Paycrest order or sell intent (ReconcileTick). On
// Postgres one instance runs a tick at a time.
type PayReconciler struct {
	svc      PayReconcileRunner
	interval time.Duration
}

// NewPayReconciler builds the worker. A non-positive interval falls back to
// 30s.
func NewPayReconciler(svc PayReconcileRunner, interval time.Duration) *PayReconciler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &PayReconciler{svc: svc, interval: interval}
}

func (w *PayReconciler) Name() string            { return "tsa-pay-reconciler" }
func (w *PayReconciler) Interval() time.Duration { return w.interval }

func (w *PayReconciler) Run(ctx context.Context) error {
	return w.svc.ReconcileTick(ctx)
}
