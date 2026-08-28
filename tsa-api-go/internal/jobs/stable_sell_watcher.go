package jobs

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
)

// stableSellDriver is the slice of ITService this watcher needs.
type stableSellDriver interface {
	// PendingStableSells returns intents awaiting confirmation, awaiting
	// conversion, awaiting conversion SETTLEMENT, and awaiting PAYOUT, in that
	// order.
	PendingStableSells(ctx context.Context) (reported, confirmed, converting, debited []uuid.UUID, err error)
	ConfirmStableDeposit(ctx context.Context, id uuid.UUID) error
	ConvertAndQueuePayout(ctx context.Context, id uuid.UUID) error
	FinalizeConversion(ctx context.Context, id uuid.UUID) error
	PayoutStableSell(ctx context.Context, id uuid.UUID) error
}

// StableSellWatcher drives the brokered stablecoin sell state machine.
//
// It exists because no single event tells us a sell is ready: a bridge finishes
// without notifying us, and Flutterwave publishes no webhook for an inbound
// stablecoin deposit. Polling is the only signal available, so the watcher
// walks non-terminal intents and advances whichever ones can move — the same
// shape as the existing chain reconciler.
//
// Every step it calls is idempotent, so a redelivered tick is harmless.
type StableSellWatcher struct {
	driver   stableSellDriver
	interval time.Duration
}

func NewStableSellWatcher(driver stableSellDriver, interval time.Duration) *StableSellWatcher {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &StableSellWatcher{driver: driver, interval: interval}
}

func (w *StableSellWatcher) Name() string            { return "it-stable-sell-watcher" }
func (w *StableSellWatcher) Interval() time.Duration { return w.interval }

func (w *StableSellWatcher) Run(ctx context.Context) error {
	reported, confirmed, converting, debited, err := w.driver.PendingStableSells(ctx)
	if err != nil {
		return err
	}

	// Errors are logged and swallowed per intent, never returned: one user's
	// stalled bridge or one transient provider failure must not stop every
	// other user's payout from advancing on this tick.
	for _, id := range reported {
		if err := w.driver.ConfirmStableDeposit(ctx, id); err != nil {
			log.Printf("[it-stable-sell-watcher] confirm intent=%s: %v", id, err)
		}
	}
	for _, id := range confirmed {
		if err := w.driver.ConvertAndQueuePayout(ctx, id); err != nil {
			log.Printf("[it-stable-sell-watcher] convert intent=%s: %v", id, err)
		}
	}
	// Conversion is asynchronous: accepted as NEW, settled later. Only a
	// settled conversion may release the intent to the payout path.
	for _, id := range converting {
		if err := w.driver.FinalizeConversion(ctx, id); err != nil {
			log.Printf("[it-stable-sell-watcher] finalize intent=%s: %v", id, err)
		}
	}
	// The last hop. A settled conversion leaves the user's fiat sitting in the
	// platform's own Flutterwave balance; nothing else in the service pays a
	// brokered sell out (CreateSellIntent's inline AutoPayout is the MCGP path
	// only), so without this step every stablecoin sell ends converted and
	// unpaid. Re-running is free — AutoPayout short-circuits once a payout has
	// been requested — so a dropped call heals on the next tick.
	for _, id := range debited {
		if err := w.driver.PayoutStableSell(ctx, id); err != nil {
			log.Printf("[it-stable-sell-watcher] payout intent=%s: %v", id, err)
		}
	}
	return nil
}
