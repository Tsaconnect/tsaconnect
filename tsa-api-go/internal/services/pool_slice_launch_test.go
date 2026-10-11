package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// A slice whose open hash write was lost (pool_open records it detached; S44
// reclaims by nonce) is still the pool's to reconcile: the direct-P2P open
// backfill, which needs an opener_address no slice has, must not stall it.
func TestReconcile_SliceWithoutOpenHashSkipsTheOpenBackfill(t *testing.T) {
	r := newPoolRig(t)
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	r.db.Model(sl).Update("open_tx_hash", "")
	r.pool.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusReleased, Amount: usdt18(5)}
	if _, err := r.svc.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleased || got.OpenTxHash == openTxReconciled {
		t.Fatalf("status=%s open=%q, want released with no backfill marker", got.Status, got.OpenTxHash)
	}
	if len(r.bus.published) != 0 {
		t.Fatalf("events=%d, want a slice reconcile to stay silent", len(r.bus.published))
	}
}

// Chain Paid on a slice flips it like any trade, but no P2P push reaches the
// LP or the treasury identity (LPs see pool events, never P2P trade pushes).
func TestReconcile_SliceChainPaidIsSilent(t *testing.T) {
	r := newPoolRig(t)
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	r.pool.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPaid, Amount: usdt18(5)}
	if _, err := r.svc.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusPaid || len(r.bus.published) != 0 {
		t.Fatalf("status=%s events=%d, want paid and silent", got.Status, len(r.bus.published))
	}
}

// No gas drip for a slice: it answers as an unknown trade, like every other
// user P2P path, even to its LP (the seller) whose release gate would pass.
func TestGasNeed_PoolSliceIsUnknown(t *testing.T) {
	f := newGasNeedFixture(t)
	id := f.seedTrade(t, models.P2PTradeStatusPendingPayment, time.Hour, 146)
	if err := f.check(f.seller, P2PGasRequest{Action: P2PGasRelease, TradeID: id}); err != nil {
		t.Fatalf("ordinary trade release: %v, want a need", err)
	}
	intent := id // any non-nil id makes it a slice
	f.db.Model(&models.P2PTrade{}).Where("id = ?", id).Update("it_intent_id", intent)
	for _, a := range []P2PGasAction{P2PGasRelease, P2PGasCancelTrade, P2PGasDispute, P2PGasMarkPaid} {
		caller := f.seller
		if a == P2PGasMarkPaid {
			caller = f.buyer
		}
		err := f.check(caller, P2PGasRequest{Action: a, TradeID: id})
		if !errors.Is(err, ErrInvalidParams) || !strings.Contains(err.Error(), "unknown tradeId") {
			t.Errorf("%s on a slice: %v, want unknown tradeId", a, err)
		}
	}
}
