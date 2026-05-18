package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type fakeTradeLister struct {
	trades []models.P2PTrade
	err    error
	calls  int
}

func (f *fakeTradeLister) ClaimExpiredPendingTrades(_ context.Context, limit int) ([]models.P2PTrade, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := f.trades
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	// Mimic the production "claim" semantic: each call consumes the
	// returned trades so subsequent ticks see an empty queue. This is what
	// dedups expiry notifications in production.
	f.trades = f.trades[len(out):]
	return out, nil
}

type fakePublisher struct{ events []events.Event }

func (p *fakePublisher) Publish(ev events.Event) { p.events = append(p.events, ev) }
func (p *fakePublisher) countByType(t string) int {
	n := 0
	for _, e := range p.events {
		if e.Type == t {
			n++
		}
	}
	return n
}

func TestP2PTimeoutWatchdog_EmitsOneEventPerParticipantPerExpiredTrade(t *testing.T) {
	sellerID, buyerID := uuid.New(), uuid.New()
	adID := uuid.New()
	tradeID := uuid.New()
	lister := &fakeTradeLister{
		trades: []models.P2PTrade{{
			ID:        tradeID,
			AdID:      adID,
			SellerID:  sellerID,
			BuyerID:   buyerID,
			ExpiresAt: time.Now().Add(-1 * time.Hour),
		}},
	}
	pub := &fakePublisher{}
	w := NewP2PTimeoutWatchdog(lister, pub)

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if pub.countByType(events.P2PTradeExpired) != 2 {
		t.Fatalf("want 2 expired events (seller+buyer), got %d", pub.countByType(events.P2PTradeExpired))
	}
	// Verify seller and buyer each got exactly one event.
	var sellerEvents, buyerEvents int
	for _, e := range pub.events {
		switch e.UserID {
		case sellerID:
			sellerEvents++
		case buyerID:
			buyerEvents++
		}
	}
	if sellerEvents != 1 || buyerEvents != 1 {
		t.Fatalf("want 1 event each; seller=%d buyer=%d", sellerEvents, buyerEvents)
	}
}

func TestP2PTimeoutWatchdog_NoEventsWhenNothingExpired(t *testing.T) {
	lister := &fakeTradeLister{trades: nil}
	pub := &fakePublisher{}
	w := NewP2PTimeoutWatchdog(lister, pub)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("want 0 events, got %d", len(pub.events))
	}
}

func TestP2PTimeoutWatchdog_ListerErrorBubblesUp(t *testing.T) {
	lister := &fakeTradeLister{err: errors.New("db gone")}
	pub := &fakePublisher{}
	w := NewP2PTimeoutWatchdog(lister, pub)
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("want error when lister fails")
	}
}

func TestP2PTimeoutWatchdog_OneEventOnSelfTrade(t *testing.T) {
	// Defensive: if a buggy state somehow recorded the same user as both
	// buyer and seller (shouldn't happen due to CHECK constraint, but pin
	// the behaviour anyway), only the seller event fires.
	uid := uuid.New()
	lister := &fakeTradeLister{
		trades: []models.P2PTrade{{
			ID: uuid.New(), AdID: uuid.New(),
			SellerID: uid, BuyerID: uid,
			ExpiresAt: time.Now().Add(-time.Hour),
		}},
	}
	pub := &fakePublisher{}
	w := NewP2PTimeoutWatchdog(lister, pub)
	_ = w.Run(context.Background())
	if pub.countByType(events.P2PTradeExpired) != 1 {
		t.Fatalf("want 1 event on degenerate self-trade, got %d", pub.countByType(events.P2PTradeExpired))
	}
}

func TestP2PTimeoutWatchdog_DoesNotReEmitOnSecondRun(t *testing.T) {
	// Production fix for H2: each expired trade fires exactly one event.
	// The fake lister mimics the production claim semantic (returned trades
	// are removed from the queue), so the second run sees nothing.
	lister := &fakeTradeLister{
		trades: []models.P2PTrade{{
			ID: uuid.New(), AdID: uuid.New(),
			SellerID: uuid.New(), BuyerID: uuid.New(),
			ExpiresAt: time.Now().Add(-time.Hour),
		}},
	}
	pub := &fakePublisher{}
	w := NewP2PTimeoutWatchdog(lister, pub)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstCount := pub.countByType(events.P2PTradeExpired)
	if firstCount == 0 {
		t.Fatalf("first run should have emitted")
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if pub.countByType(events.P2PTradeExpired) != firstCount {
		t.Fatalf("second run re-emitted: total=%d, expected stable at %d",
			pub.countByType(events.P2PTradeExpired), firstCount)
	}
}

func TestP2PTimeoutWatchdog_RespectsInterval(t *testing.T) {
	w := NewP2PTimeoutWatchdog(&fakeTradeLister{}, &fakePublisher{})
	if w.Interval() != time.Minute {
		t.Fatalf("default interval want 1m, got %s", w.Interval())
	}
	custom := w.WithInterval(15 * time.Second)
	if custom.Interval() != 15*time.Second {
		t.Fatalf("WithInterval override failed: %s", custom.Interval())
	}
}
