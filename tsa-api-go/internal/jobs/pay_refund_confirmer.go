package jobs

import (
	"context"
	"time"
)

// PayRefundConfirmRunner is the slice of *services.PayRefundService the
// confirmer needs.
type PayRefundConfirmRunner interface {
	ConfirmDue(ctx context.Context) error
}

// PayRefundConfirmer checks every submitted TSA Pay refund against the chain:
// confirms the paid, fails the proven unpaid, sends the forgotten again. A
// refund nobody confirms leaves a merchant unsure whether to pay twice, so it
// is registered with AlertOnFailureStreak.
type PayRefundConfirmer struct {
	svc      PayRefundConfirmRunner
	interval time.Duration
}

// NewPayRefundConfirmer builds the worker; a non-positive interval is 30 s.
func NewPayRefundConfirmer(svc PayRefundConfirmRunner, interval time.Duration) *PayRefundConfirmer {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &PayRefundConfirmer{svc: svc, interval: interval}
}

func (w *PayRefundConfirmer) Name() string            { return "tsa-pay-refund-confirmer" }
func (w *PayRefundConfirmer) Interval() time.Duration { return w.interval }

func (w *PayRefundConfirmer) Run(ctx context.Context) error { return w.svc.ConfirmDue(ctx) }
