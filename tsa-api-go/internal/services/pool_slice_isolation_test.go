package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// isolatedSlice is an LP's pool slice (the LP is the trade's seller, the
// company PM user its buyer, S10) on Plan B's pool rig.
func isolatedSlice(t *testing.T, r *poolRig, status string, created time.Time) (*models.PaymentMerchant, *models.P2PTrade) {
	t.Helper()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	ad := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", usdt18(1000).String())
	sl := seedPoolSlice(t, r.db, sliceSeed{Intent: uuid.New(), Ad: ad, Units: usdt18(5), Status: status, Opened: true, CreatedAt: created})
	r.db.Model(sl).Update("open_tx_hash", "0x"+strings.Repeat("ab", 32))
	return lp, sl
}

// §23: no user — not the LP, not the buyer identity — can act on a slice.
func TestPoolSlices_HiddenFromUserP2PEndpoints(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC))
	notFound := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrTradeNotFound) {
			t.Errorf("%s = %v, want ErrTradeNotFound", name, err)
		}
	}
	_, err := r.svc.GetTrade(ctx, sl.ID, lp.UserID)
	notFound("GetTrade(LP)", err)
	_, err = r.svc.GetTrade(ctx, sl.ID, sl.BuyerID)
	notFound("GetTrade(buyer identity)", err)
	_, _, err = r.svc.PrepareMarkPaid(ctx, sl.ID, sl.BuyerID, lpWallet, "https://res.cloudinary.com/tsa/image/upload/x.png")
	notFound("PrepareMarkPaid", err)
	_, err = r.svc.ConfirmMarkPaid(ctx, sl.ID, sl.BuyerID, "0x"+strings.Repeat("cd", 32))
	notFound("ConfirmMarkPaid", err)
	_, err = r.svc.PrepareCancel(ctx, sl.ID, lp.UserID, lpWallet)
	notFound("PrepareCancel", err)
	_, err = r.svc.ConfirmCancel(ctx, sl.ID, lp.UserID, "0x"+strings.Repeat("cd", 32))
	notFound("ConfirmCancel", err)
	_, err = r.svc.PrepareRelease(ctx, sl.ID, lp.UserID, lpWallet)
	notFound("PrepareRelease", err)
	_, err = r.svc.ConfirmRelease(ctx, sl.ID, lp.UserID, "0x"+strings.Repeat("cd", 32))
	notFound("ConfirmRelease", err)
	_, err = r.svc.PrepareRaiseDispute(ctx, sl.ID, lp.UserID, lpWallet, "never paid", nil)
	notFound("PrepareRaiseDispute", err)
	_, err = r.svc.ConfirmRaiseDispute(ctx, sl.ID, lp.UserID, "0x"+strings.Repeat("cd", 32))
	notFound("ConfirmRaiseDispute", err)
	_, err = r.svc.ConfirmOpenTrade(ctx, sl.ID, sl.BuyerID, "0x"+strings.Repeat("cd", 32))
	notFound("ConfirmOpenTrade", err)
	_, err = NewP2PMessageService(r.db, nil).List(ctx, sl.ID, lp.UserID, MessageFilter{})
	notFound("messages", err)
	rows, err := r.svc.ListTrades(ctx, TradeFilter{UserID: lp.UserID})
	if err != nil || len(rows) != 0 {
		t.Fatalf("ListTrades(LP) = %d rows %v, want none", len(rows), err)
	}
}

// §14: the watchdog never tells an LP "Buyer did not mark payment".
func TestWatchdog_SkipsPoolSlices(t *testing.T) {
	r := newPoolRig(t)
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	plain := &models.P2PTrade{ID: uuid.New(), AdID: sl.AdID, BuyerID: uuid.New(), SellerID: uuid.New(), Token: "USDT",
		ChainID: 146, TokenAmount: "1", FiatAmount: "1", FiatCurrency: "NGN", Rate: "1",
		Status: models.P2PTradeStatusPendingPayment, ExpiresAt: time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)}
	r.db.Create(plain)
	got, err := r.svc.ClaimExpiredPendingTrades(context.Background(), 50)
	if err != nil || len(got) != 1 || got[0].ID != plain.ID {
		t.Fatalf("claimed = %v %v, want only the ordinary trade", got, err)
	}
	listed, _ := r.svc.ListExpiredPendingTrades(context.Background(), 50)
	if len(listed) != 1 || listed[0].ID != plain.ID {
		t.Fatalf("listed = %v", listed)
	}
}

// P13: PM automation never binds, releases or queues a slice (its ad has a
// pm_id — the LP row — so the old `pm_id IS NOT NULL` filters would match).
// Re-review N1: a slice's LP never had NGN reserved for it, so cancelling a
// slice with a real payout amount commits through both cancel paths and
// leaves the LP's reserved_ngn alone.
func TestPoolSlice_CancelCommitsWithoutNGNReservation(t *testing.T) {
	for name, onChain := range map[string]*OnChainP2PTrade{
		"reclaim (never landed)": {Exists: false},
		"reconcile-to-cancelled": {Exists: true, Status: OnChainP2PStatusCancelled, Amount: usdt18(5)},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPoolRig(t)
			lp, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
			r.db.Model(sl).Update("open_tx_hash", "") // the orphan gate before Task 12 wants no hash
			if sl.FiatAmount == "0" || sl.FiatAmount == "0.00" {
				t.Fatal("the fixture must carry a real payout amount")
			}
			var before models.PaymentMerchant
			r.db.First(&before, "id = ?", lp.ID)
			r.pool.onChainTrade = onChain
			if _, err := r.svc.ReconcileTrade(context.Background(), sl.ID); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			var got models.P2PTrade
			r.db.First(&got, "id = ?", sl.ID)
			var after models.PaymentMerchant
			r.db.First(&after, "id = ?", lp.ID)
			if got.Status != models.P2PTradeStatusCancelled || after.ReservedNGN != before.ReservedNGN {
				t.Fatalf("slice=%s reserved_ngn %s → %s, want cancelled and untouched", got.Status, before.ReservedNGN, after.ReservedNGN)
			}
		})
	}
}

func TestPMAutomation_SkipsPoolSlices(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPaid, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	r.db.Model(sl).Update("updated_at", time.Now().Add(-2*time.Hour))
	signer := &fakeReleaseSigner{addr: "0x00000000000000000000000000000000000000a1", hash: "0xr"}
	auto := NewPMAutomationService(r.db, NewPMService(r.db), r.svc, signer, nil, nil, "")
	if n, err := auto.RecoverStuckTrades(ctx, time.Minute); err != nil || n != 0 || len(signer.calls) != 0 {
		t.Fatalf("recover = %d %v (%d sends), want nothing", n, err, len(signer.calls))
	}
	q, err := auto.ListBuyManualQueue(ctx)
	if err != nil || len(q) != 0 {
		t.Fatalf("manual queue = %v %v", q, err)
	}
	if _, err := auto.ManualRelease(ctx, sl.ID, uuid.New()); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("manual release = %v", err)
	}
}

// §14 / §19: reconcile transitions of a slice write audit rows but no P2P
// notification — LPs see "TSA Connect" events (C2), never P2P trade pushes.
func TestPoolSlices_NoP2PNotifications(t *testing.T) {
	r := newPoolRig(t)
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	r.pool.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusCancelled, Amount: usdt18(5)}
	if _, err := r.svc.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusCancelled || len(r.bus.published) != 0 {
		t.Fatalf("status=%s events=%d, want cancelled and silent", got.Status, len(r.bus.published))
	}
}
