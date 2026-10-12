package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// S37: paid, nothing released — refundable; every slice is dropped for the
// canceller and an audit row is written.
func TestPoolRefund_PaidUndeliveredRefunds(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	got, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned by bank transfer")
	if err != nil || got.Status != models.ITStatusRefunded {
		t.Fatalf("refund = %v %v", got, err)
	}
	if sl := r.slicesOf(t, intent.ID)[0]; sl.DroppedAt == nil {
		t.Fatal("the slice must be dropped so the canceller frees the LP's stock")
	}
	var n int64
	r.db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ? AND action = ?", intent.ID, models.ITAuditStableBuyRefunded).Count(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

// Roadmap risk 1 / S37: after any release the order must be delivered.
func TestPoolRefund_RefusedAfterAnyRelease(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	r.chain.setTrade(HashID(r.slicesOf(t, intent.ID)[1].ID), OnChainP2PStatusCancelled)
	_ = r.advance(t, intent.ID) // A released, B gone → parked pool_slices_short
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("setup: status %s", got.Status)
	}
	_, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note")
	if !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), "treasury") {
		t.Fatalf("err = %v, want refused: a slice is at the treasury", err)
	}
}

// S37 / review C1: "not released" also means "not releasing".
func TestPoolRefund_RefusedWhileSliceReleasing(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	r.db.Model(&models.P2PTrade{}).Where("it_intent_id = ?", intent.ID).Update("status", models.P2PTradeStatusReleasing)
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want refused while releasing", err)
	}
}

// Review C-1, the exact failure: A's release is broadcast but no receipt is
// seen within the wait; B is cancelled by anyone; the order parks. A stays
// at the treasury (never dropped), so the refund is refused; when A mines,
// settlement carries on — A's LP is paid once and re-delivery opens.
func TestPoolRefund_UnseenReleaseThenSiblingCancelled(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45, 30)
	_, gw, _ := r.wireForwards(t)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	intent := r.openPaid(t, "100000")
	sls := r.slicesOf(t, intent.ID)
	r.sender.noMine = true // A: broadcast, no receipt
	r.chain.setTrade(HashID(sls[1].ID), OnChainP2PStatusCancelled)
	_ = r.advance(t, intent.ID)
	var a models.P2PTrade
	r.db.First(&a, "id = ?", sls[0].ID)
	if a.DroppedAt != nil || a.ReleaseTxHash == "" || !sliceAtTreasury(a.Status, a.DroppedAt, a.ReleaseTxHash, a.CancelReason) {
		t.Fatalf("A = %s dropped=%v hash=%q, want kept at the treasury", a.Status, a.DroppedAt, a.ReleaseTxHash)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s, want parked short", got.Status)
	}
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("refund = %v, want refused while A's release is unresolved", err)
	}
	// A's release mines: the reconciler lands it, the books pass pays its LP.
	id := HashID(sls[0].ID)
	r.chain.setTrade(id, OnChainP2PStatusReleased)
	h := common.HexToHash(a.ReleaseTxHash)
	r.chain.receipts[h] = r.chain.head
	r.sender.receipts[h] = releaseReceipt(id, common.HexToAddress(poolTestTreasury), r.chain.trades[id].Amount, r.chain.head)
	r.verifier.credited[strings.ToLower(h.Hex())] = r.chain.trades[id].Amount
	if _, err := r.trades.ReconcileTrade(context.Background(), sls[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.it.CompletePoolBooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("LP payouts = %d, want A's LP paid once", len(gw.calls))
	}
	if _, err := r.it.RedeliverPoolBuy(context.Background(), uuid.New(), intent.ID, "re-deliver the owed units"); err != nil {
		t.Fatalf("redeliver = %v", err)
	}
}

// A delivering order (the settle engine won the CAS) is never refundable.
func TestPoolRefund_RefusedOnceDelivering(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	r.sender.noMine = true
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	r.db.Model(&models.P2PTrade{}).Where("it_intent_id = ?", intent.ID).Update("status", models.P2PTradeStatusPendingPayment)
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want refused: delivering", err)
	}
}

// flipBeforeDrop makes the settle engine's release flip land on slice id
// between a caller's read and its drop — what AutoRelease (run outside the
// pool lock) can do on Postgres. It fires once, on the first drop write.
func flipBeforeDrop(t *testing.T, db *gorm.DB, id uuid.UUID) {
	t.Helper()
	name := "test:flip_before_drop:" + id.String()
	fired := false
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		dest, ok := tx.Statement.Dest.(map[string]any)
		if fired || tx.Statement.Table != "p2p_trades" || !ok {
			return
		}
		if _, drop := dest["dropped_at"]; !drop {
			return
		}
		fired = true
		if _, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
			"UPDATE p2p_trades SET status = ? WHERE id = ?", models.P2PTradeStatusReleasing, id); err != nil {
			t.Errorf("flip: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
}

// dropSlices reports what it really dropped: never a releasing slice or one
// with a recorded release hash.
func TestPoolDropSlices_ReturnsOnlyTheDroppedIDs(t *testing.T) {
	r := newPoolSettleRig(t, 40, 30, 20)
	intent := r.openPaid(t, "100000")
	sls := r.slicesOf(t, intent.ID)
	if len(sls) < 3 {
		t.Fatalf("setup: %d slices", len(sls))
	}
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sls[0].ID).Update("status", models.P2PTradeStatusReleasing)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sls[1].ID).Update("release_tx_hash", "0xbroadcast")
	dropped, err := r.desk.dropSlices(r.db, []uuid.UUID{sls[0].ID, sls[1].ID, sls[2].ID}, time.Now())
	if err != nil || len(dropped) != 1 || dropped[0] != sls[2].ID {
		t.Fatalf("dropped=%v err=%v, want only the live unhashed slice", dropped, err)
	}
}

// Controller item 1 / C-1: the refund read every slice as live, then a
// release flip landed before its drop. The refund must not stand with a
// releasing slice behind it — it rolls back and refuses.
func TestPoolRefund_RefusedWhenADropRacesARelease(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	a := r.slicesOf(t, intent.ID)[0]
	flipBeforeDrop(t, r.db, a.ID)
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, errPoolSlicesMoved) {
		t.Fatalf("refund = %v, want refused: a slice moved under the decision", err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %s, want still payment_received (never refunded with a releasing slice)", got.Status)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.DroppedAt != nil {
			t.Fatalf("slice %s dropped, want the whole refund rolled back", sl.ID)
		}
	}
}

// Controller item 1: S18 in the watcher parks (drop-all) only if every
// pending slice really was dropped — never a park that leaves a releasing
// slice behind, which would open the refund.
func TestPoolReeval_ParkRefusedWhenADropRacesARelease(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent := r.openPaid(t, "100000")
	sls := r.slicesOf(t, intent.ID)
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusDelivering)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sls[1].ID).Update("status", models.P2PTradeStatusCancelled) // B lost
	flipBeforeDrop(t, r.db, sls[0].ID)
	parked, err := r.it.reevaluatePoolOrder(context.Background(), r.desk, intent.ID)
	if parked || !errors.Is(err, errPoolSlicesMoved) {
		t.Fatalf("parked=%v err=%v, want refused: A's release flip landed under the decision", parked, err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want still delivering", got.Status)
	}
}

// Controller item 1: the claim's park (drop-all) is refused the same way;
// the inbound poller retries and decides on what the slices really are.
func TestPoolClaim_ParkRefusedWhenADropRacesARelease(t *testing.T) {
	r := newPoolSettleRig(t, 60, 45)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	sls := r.slicesOf(t, intent.ID)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sls[1].ID).Update("dropped_at", time.Now()) // B no longer backs her
	flipBeforeDrop(t, r.db, sls[0].ID)
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_race", intent.AmountMinor, time.Now(), false, PayInCentral)
	if ok || !errors.Is(err, errPoolSlicesMoved) {
		t.Fatalf("claim = %v %v, want refused: a slice moved under the decision", ok, err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %s, want unclaimed (the poller retries)", got.Status)
	}
}

// Controller item 4 / C-1: a recorded release hash not yet proven gone is at
// the treasury — the refund refuses.
func TestPoolRefund_RefusedWithAnUnprovenReleaseHash(t *testing.T) {
	r := newPoolSettleRig(t, 50)
	intent := r.openPaid(t, "5000")
	r.db.Model(&models.P2PTrade{}).Where("it_intent_id = ?", intent.ID).
		Updates(map[string]any{"status": models.P2PTradeStatusPaid, "release_tx_hash": "0xbroadcast"})
	if _, err := r.it.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("refund = %v, want refused: a release was broadcast", err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %s", got.Status)
	}
}
