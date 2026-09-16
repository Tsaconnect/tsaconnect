package jobs

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/google/uuid"
)

// stableBuyDriver is the slice of ITService this watcher needs.
type stableBuyDriver interface {
	// PendingStableBuys returns intents whose payment landed but have not yet
	// converted, whose conversion is settling, and whose crypto delivery is
	// in flight, in that order.
	PendingStableBuys(ctx context.Context) (received, awaiting, converting, delivering []uuid.UUID, err error)
	ConvertStableBuy(ctx context.Context, id uuid.UUID) error
	FinalizeBuyConversion(ctx context.Context, id uuid.UUID) error
	DeliverStableBuy(ctx context.Context, id uuid.UUID) error
	FinalizeBuyDelivery(ctx context.Context, id uuid.UUID) error
}

// StableBuyWatcher drives the brokered stablecoin buy state machine.
//
// Polling is the only signal available: Flutterwave publishes no webhook for a
// conversion settling, for a crypto transfer completing, or for collected
// naira becoming spendable. Every step it calls is idempotent, so a redelivered
// tick is harmless.
type StableBuyWatcher struct {
	driver   stableBuyDriver
	interval time.Duration
}

func NewStableBuyWatcher(driver stableBuyDriver, interval time.Duration) *StableBuyWatcher {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &StableBuyWatcher{driver: driver, interval: interval}
}

func (w *StableBuyWatcher) Name() string            { return "it-stable-buy-watcher" }
func (w *StableBuyWatcher) Interval() time.Duration { return w.interval }

func (w *StableBuyWatcher) Run(ctx context.Context) error {
	received, awaiting, converting, delivering, err := w.driver.PendingStableBuys(ctx)
	if err != nil {
		return err
	}

	// Errors are logged and swallowed per intent, never returned: one user's
	// unsettled conversion must not stop every other user from advancing.
	// slices.Concat, not append: append would write into `received`'s backing
	// array, which is not ours to reuse and says nothing about the intent.
	for _, id := range slices.Concat(received, awaiting) {
		if err := w.driver.ConvertStableBuy(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] convert intent=%s: %v", id, err)
		}
	}
	for _, id := range converting {
		if err := w.driver.FinalizeBuyConversion(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] finalize conversion intent=%s: %v", id, err)
		}
	}
	for _, id := range delivering {
		// Send first (a no-op once DeliveryID is set), then poll — so a newly
		// converted intent is dispatched and checked on the same tick.
		if err := w.driver.DeliverStableBuy(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] deliver intent=%s: %v", id, err)
		}
		if err := w.driver.FinalizeBuyDelivery(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] finalize delivery intent=%s: %v", id, err)
		}
	}
	return nil
}
