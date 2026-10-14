package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Restored from pool_expiry_test.go and p2p_slice_release_test.go (deleted
// with the slice pool in 4cf9bb39). The behaviour they pinned is still live on
// ordinary P2P trades; the slice fixtures are swapped for legacy-scope ones.

func restoredReleaseKey() *fakeReleaseSigner {
	return &fakeReleaseSigner{addr: "0x00000000000000000000000000000000000000a1", hash: "0x" + strings.Repeat("ab", 32)}
}

// M1: a Flutterwave statement outage defers every cancel.
func TestExpiry_DeferredWhileStatementStale(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-61 * time.Minute).Unix())}
	stale := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender, Fresh: fixedFresh{at: time.Now().Add(-11 * time.Minute)}})
	tick, _ := stale.RunOnce(context.Background())
	if !tick.Deferred || len(sender.sendChains) != 0 {
		t.Fatalf("stale statement: tick=%+v sends=%v", tick, sender.sendChains)
	}
	fresh := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender, Fresh: fixedFresh{at: time.Now().Add(-30 * time.Second)}})
	tick, _ = fresh.RunOnce(context.Background())
	if tick.Deferred || tick.Legacy != 1 || len(sender.sendChains) != 1 {
		t.Fatalf("fresh statement: tick=%+v sends=%v", tick, sender.sendChains)
	}
}

// Review I1: a full page of rows the canceller cannot act on (here: payouts in
// flight, which the decision refuses) does not starve the rows behind it.
func TestExpiry_PagesPastRowsItCannotCancel(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	cancellable := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	ad := seedActiveAd(t, r.db, uuid.New(), 146, nil, "1500", "1000000")
	old := time.Now().Add(-3 * time.Hour)
	started := time.Now().Add(-4 * time.Hour)
	blocked := make([]models.P2PTrade, 0, expiryScanLimit)
	for i := 0; i < expiryScanLimit; i++ {
		id := uuid.New()
		notified := old.Add(time.Duration(i) * time.Millisecond) // ahead of the cancellable trade in the page order
		blocked = append(blocked, models.P2PTrade{ID: id, OnChainTradeID: HashIDHex(id), AdID: ad.ID, BuyerID: uuid.New(), SellerID: ad.SellerID,
			Token: "USDT", ChainID: 146, TokenAmount: "1000000", FiatAmount: "1500", FiatCurrency: "NGN", Rate: "1500",
			Status: models.P2PTradeStatusPendingPayment, OpenTxHash: "0xopened", ExpiresAt: notified.Add(-time.Minute),
			ExpiredNotifiedAt: &notified, PayoutStartedAt: &started})
	}
	if err := r.db.CreateInBatches(blocked, 100).Error; err != nil {
		t.Fatal(err)
	}
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-5 * time.Hour).Unix())}
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender})
	ctx := context.Background()
	if tick, _ := ec.RunOnce(ctx); tick.Legacy != 0 || len(sender.sendChains) != 0 {
		t.Fatalf("first page = %+v sends=%v, want only the rows it cannot cancel", tick, sender.sendChains)
	}
	if tick, _ := ec.RunOnce(ctx); tick.Legacy != 1 || len(sender.sendChains) != 1 {
		t.Fatalf("second page = %+v sends=%v, want the abandoned trade %s cancelled", tick, sender.sendChains, cancellable.ID)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", cancellable.ID)
	if got.DroppedAt == nil {
		t.Fatal("the cancellable trade's decision was not recorded")
	}
}

// Ordinary trades keep the rollback and write no hash on a failed verify.
func TestAutoRelease_OrdinaryTradeUnchangedOnVerifyFailure(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt"); err != nil {
		t.Fatal(err)
	}
	esc.verifyReleasedErr = errors.New("receipt not found")
	_, _ = svc.AutoRelease(context.Background(), trade.ID, restoredReleaseKey())
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusPaid || got.ReleaseTxHash != "" {
		t.Fatalf("ordinary trade = %s hash=%q, want the rollback", got.Status, got.ReleaseTxHash)
	}
}

// A released ordinary trade pushes to both seller and buyer.
func TestConfirmRelease_OrdinaryTradeStillPushes(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, bus := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt"); err != nil {
		t.Fatal(err)
	}
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID), AmountToBuyer: big.NewInt(999_500), FeeToSystem: big.NewInt(500)}
	if _, err := svc.AutoRelease(context.Background(), trade.ID, restoredReleaseKey()); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range bus.published {
		if ev.Type == events.P2PTradeReleased {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("pushes = %d, want seller + buyer", n)
	}
}
