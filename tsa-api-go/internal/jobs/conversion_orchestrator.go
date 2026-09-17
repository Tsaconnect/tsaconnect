package jobs

import (
	"context"
	"time"
)

// conversionTicker is the tiny slice of services.ConversionOrchestrator this
// worker depends on — kept local so this package never imports the concrete
// orchestrator type.
type conversionTicker interface {
	Tick(ctx context.Context, limit int) error
}

// ConversionOrchestratorWorker drives converted-buy state machine steps
// (Paycrest payment → treasury custody → bridge → deliver) forward on each
// tick. Idempotent by construction: Tick re-derives what to do from DB +
// on-chain state, so a missed tick just gets picked up on the next one.
type ConversionOrchestratorWorker struct {
	svc      conversionTicker
	interval time.Duration
}

func NewConversionOrchestratorWorker(svc conversionTicker, interval time.Duration) *ConversionOrchestratorWorker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &ConversionOrchestratorWorker{svc: svc, interval: interval}
}

func (w *ConversionOrchestratorWorker) Name() string            { return "conversion-orchestrator" }
func (w *ConversionOrchestratorWorker) Interval() time.Duration { return w.interval }

// Run ticks a batch and surfaces the error so the scheduler logs it; per the
// Worker contract a single failed tick never stops the worker. limit is 0 so
// the batch size lives in one place — services.ConversionOrchestrator.Tick's
// own default — rather than a second magic number here.
func (w *ConversionOrchestratorWorker) Run(ctx context.Context) error {
	if w.svc == nil {
		return nil // unconfigured orchestrator is a dormant feature, not a failure
	}
	return w.svc.Tick(ctx, 0)
}
