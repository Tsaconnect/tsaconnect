package jobs

import (
	"context"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// EventPublisher is the minimal events.Bus surface the watchdog depends on.
// Kept narrow so tests inject a fake without pulling in the full bus.
type EventPublisher interface {
	Publish(events.Event)
}

// TradeLister exposes the trade-service slice the watchdog reads. The Claim
// variant atomically marks trades as notified so subsequent ticks skip them
// — guarantees at-most-one expiry event per trade.
type TradeLister interface {
	ClaimExpiredPendingTrades(ctx context.Context, limit int) ([]models.P2PTrade, error)
}

// P2PTimeoutWatchdog scans for pending P2P trades whose 30-minute payment
// window has elapsed and nudges participants to reclaim escrow via the
// cancelByTimeout flow.
//
// Phase-1 behaviour: emit one P2PTradeExpired event per expired trade per
// tick. The notification dispatcher handles delivery (push, WS, in-app). No
// on-chain action — Phase 1 has no operator EOA, so the contract-side
// reclaim is user-driven (seller signs the cancel tx).
//
// The watchdog is idempotent: re-running on a converged state simply emits
// nothing new beyond the user-visible nudge. Notification de-duplication
// (so a user isn't pinged every minute) is the notification service's
// concern; Phase 1 accepts at-most-one-per-minute as the rough UX.
type P2PTimeoutWatchdog struct {
	trades   TradeLister
	bus      EventPublisher
	interval time.Duration
}

func NewP2PTimeoutWatchdog(trades TradeLister, bus EventPublisher) *P2PTimeoutWatchdog {
	return &P2PTimeoutWatchdog{
		trades:   trades,
		bus:      bus,
		interval: 1 * time.Minute,
	}
}

// WithInterval lets tests bypass the 1-minute cadence.
func (w *P2PTimeoutWatchdog) WithInterval(d time.Duration) *P2PTimeoutWatchdog {
	cp := *w
	cp.interval = d
	return &cp
}

func (w *P2PTimeoutWatchdog) Name() string            { return "p2p-timeout-watchdog" }
func (w *P2PTimeoutWatchdog) Interval() time.Duration { return w.interval }

func (w *P2PTimeoutWatchdog) Run(ctx context.Context) error {
	expired, err := w.trades.ClaimExpiredPendingTrades(ctx, 200)
	if err != nil {
		return err
	}
	if len(expired) == 0 {
		return nil
	}
	log.Printf("[p2p-timeout-watchdog] %d expired pending trades nudged", len(expired))
	for _, t := range expired {
		// Notify the seller first — they're the one whose escrow is locked.
		// Message wording is side-aware (cosmetic): on a pooled sell-ad the
		// crypto is locked from the seller's pool; on a deposited-by-taker
		// buy-ad the SellerID is the depositing user whose JIT deposit is
		// locked. No accounting happens here — the on-chain cancel + the
		// ConfirmCancel/reconciler restore capacity.
		sellerMsg := "Buyer did not mark payment within 30 minutes. Reclaim your crypto via cancel."
		if t.Funding == models.P2PTradeFundingPooled {
			sellerMsg = "Buyer did not mark payment within 30 minutes. Reclaim your USDT via cancel."
		}
		w.bus.Publish(events.Event{
			Type:    events.P2PTradeExpired,
			UserID:  t.SellerID,
			Title:   "P2P trade expired",
			Message: sellerMsg,
			Data: map[string]interface{}{
				"tradeId": t.ID.String(),
				"adId":    t.AdID.String(),
				"role":    "seller",
			},
		})
		// Buyer gets a softer ping — they may have abandoned, or they may
		// still want to act before the seller reclaims.
		if t.BuyerID != t.SellerID {
			w.bus.Publish(events.Event{
				Type:    events.P2PTradeExpired,
				UserID:  t.BuyerID,
				Title:   "P2P trade expired",
				Message: "This trade has timed out. If you've already paid, raise a dispute now.",
				Data: map[string]interface{}{
					"tradeId": t.ID.String(),
					"adId":    t.AdID.String(),
					"role":    "buyer",
				},
			})
		}
	}
	return nil
}
