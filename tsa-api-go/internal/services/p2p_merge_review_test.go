package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// expiredDirectTrade is a direct (non-merchant) trade 31 minutes past its
// expiry notice, Pending on-chain and past its window: the legacy scope's case.
func expiredDirectTrade(t *testing.T, r *poolRig) *models.P2PTrade {
	t.Helper()
	tr := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending,
		CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
	return tr
}

// Review I1: a direct trade whose buyer saved payment proof but whose on-chain
// markPaid never landed is not refunded to the seller by the platform. It is
// alerted once and left to the parties.
func TestExpiry_LegacyLeavesADirectTradeWithProofAndAlertsOnce(t *testing.T) {
	r := newPoolRig(t)
	rec := &recordingAlerter{}
	r.svc.SetAlerter(rec)
	tr := expiredDirectTrade(t, r)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("proof_image_url", "https://res.cloudinary.com/tsa/image/upload/proof.png")
	sender := releaseSender()
	e := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender})
	for i := 0; i < 2; i++ {
		tick, err := e.RunOnce(context.Background())
		if err != nil || tick.Legacy != 0 || len(sender.sendChains) != 0 {
			t.Fatalf("tick %d: %+v err=%v sends=%v, want no cancel", i, tick, err, sender.sendChains)
		}
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", tr.ID)
	if got.DroppedAt != nil || got.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("dropped=%v status=%s, want no decision recorded", got.DroppedAt, got.Status)
	}
	if len(rec.got) != 1 || rec.got[0].Kind != P2PAlertExpiredWithProof || rec.got[0].TradeID != tr.ID ||
		rec.got[0].FromStatus != models.P2PTradeStatusPendingPayment {
		t.Fatalf("alerts = %+v, want one expired_with_proof for the trade", rec.got)
	}
	// The buyer can still finish: her mark-paid prepare is not refused.
	if _, _, err := r.svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr,
		"https://res.cloudinary.com/tsa/image/upload/proof.png"); err != nil && errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("mark-paid prepare refused after the canceller left it: %v", err)
	}
}

// Review I1, the other half: with no proof the direct trade is cancelled as
// before, and nothing is alerted.
func TestExpiry_LegacyCancelsADirectTradeWithoutProof(t *testing.T) {
	r := newPoolRig(t)
	rec := &recordingAlerter{}
	r.svc.SetAlerter(rec)
	tr := expiredDirectTrade(t, r)
	sender := releaseSender()
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender}).RunOnce(context.Background())
	var got models.P2PTrade
	r.db.First(&got, "id = ?", tr.ID)
	if err != nil || tick.Legacy != 1 || len(sender.sendChains) != 1 || got.DroppedAt == nil || len(rec.got) != 0 {
		t.Fatalf("tick=%+v err=%v sends=%v dropped=%v alerts=%d, want cancelled silently", tick, err, sender.sendChains, got.DroppedAt, len(rec.got))
	}
}

// PM behaviour is main's: a merchant trade's proof (only SystemMarkPaid sets
// one, with paid) is not a money-moved leg, so the canceller still cancels.
func TestExpiry_LegacyProofLegIsDirectTradesOnly(t *testing.T) {
	r := newPoolRig(t)
	tr := expiredDirectTrade(t, r)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("proof_image_url", "manual:op")
	r.db.Model(&models.P2PAd{}).Where("id = ?", tr.AdID).Update("pm_id", uuid.New())
	sender := releaseSender()
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender}).RunOnce(context.Background())
	if err != nil || tick.Legacy != 1 || len(sender.sendChains) != 1 {
		t.Fatalf("tick=%+v err=%v sends=%v, want the merchant trade cancelled as on main", tick, err, sender.sendChains)
	}
}

// Review M1: no drip for a cancel the canceller is already sending; the
// prepare itself stays permissionless.
func TestGasNeed_CancelTradeRefusesADecidedTrade(t *testing.T) {
	f := newGasNeedFixture(t)
	id := f.seedTrade(t, models.P2PTradeStatusPendingPayment, -time.Hour, 146)
	if err := f.check(f.seller, P2PGasRequest{Action: P2PGasCancelTrade, TradeID: id}); err != nil {
		t.Fatalf("expired trade cancel: %v, want a need", err)
	}
	f.db.Model(&models.P2PTrade{}).Where("id = ?", id).Update("dropped_at", f.now)
	if err := f.check(f.seller, P2PGasRequest{Action: P2PGasCancelTrade, TradeID: id}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("decided trade cancel: %v, want gas_not_needed", err)
	}
	if _, err := f.svc.PrepareCancel(context.Background(), id, f.seller, gasNeedWallet); err != nil {
		t.Fatalf("PrepareCancel on a decided trade: %v, want it still permissionless", err)
	}
}

// Review M4: the mark_paid gas need refuses a decided trade (canMarkPaid).
func TestGasNeed_MarkPaidRefusesADecidedTrade(t *testing.T) {
	f := newGasNeedFixture(t)
	id := f.seedTrade(t, models.P2PTradeStatusPendingPayment, time.Hour, 146)
	if err := f.check(f.buyer, P2PGasRequest{Action: P2PGasMarkPaid, TradeID: id}); err != nil {
		t.Fatalf("mark_paid: %v, want a need", err)
	}
	f.db.Model(&models.P2PTrade{}).Where("id = ?", id).Update("dropped_at", f.now)
	if err := f.check(f.buyer, P2PGasRequest{Action: P2PGasMarkPaid, TradeID: id}); !errors.Is(err, ErrP2PGasNotNeeded) {
		t.Fatalf("decided mark_paid: %v, want gas_not_needed", err)
	}
}

// Review M4: the deliberate no-guard in markPaidCore. A markPaid that landed
// on chain beat the canceller's cancel (which reverted), so the decided trade
// still flips to paid — through the payer's confirm and through the reconciler.
func TestMarkPaid_DecidedTradeOnChainPaidStillFlips(t *testing.T) {
	t.Run("ConfirmMarkPaid", func(t *testing.T) {
		svc, _, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
		db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("dropped_at", time.Now())
		got, err := svc.ConfirmMarkPaid(context.Background(), tr.ID, tr.BuyerID, "0xpaid")
		if err != nil || got.Status != models.P2PTradeStatusPaid {
			t.Fatalf("confirm = %v / %v, want paid", got, err)
		}
	})
	t.Run("ReconcileTrade", func(t *testing.T) {
		svc, esc, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
		db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("dropped_at", time.Now())
		esc.onChainTrade = seededBuyAdChainTrade(OnChainP2PStatusPaid)
		if _, err := svc.ReconcileTrade(context.Background(), tr.ID); err != nil {
			t.Fatal(err)
		}
		var got models.P2PTrade
		db.First(&got, "id = ?", tr.ID)
		if got.Status != models.P2PTradeStatusPaid {
			t.Fatalf("status = %s, want paid", got.Status)
		}
	})
}

// Review M3: a slice on chain Paid writes only the reconciler's drift audit,
// as on main — no trade.marked_paid row.
func TestReconcile_SliceChainPaidWritesOnlyTheDriftAudit(t *testing.T) {
	r := newPoolRig(t)
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	r.pool.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPaid, Amount: usdt18(5)}
	if _, err := r.svc.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	var marked, drift int64
	r.db.Model(&models.P2PAuditLog{}).Where("trade_id = ? AND action = ?", sl.ID, models.P2PAuditTradeMarkedPaid).Count(&marked)
	r.db.Model(&models.P2PAuditLog{}).Where("trade_id = ? AND action = ?", sl.ID, models.P2PAuditReconcilerDrift).Count(&drift)
	if marked != 0 || drift != 1 {
		t.Fatalf("marked_paid=%d drift=%d, want 0 and 1", marked, drift)
	}
}

// Review M6: her cancel lost to another cancelByTimeout and reverted, but the
// trade is cancelled on chain — the confirm applies it instead of erroring.
func TestConfirmCancel_LostRaceStillReturnsTheCancelledTrade(t *testing.T) {
	svc, esc, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	esc.verifyTradeCanErr = errors.New("receipt status 0")
	esc.onChainTrade = seededBuyAdChainTrade(OnChainP2PStatusPending)
	if _, err := svc.ConfirmCancel(context.Background(), tr.ID, tr.SellerID, "0xmine"); err == nil || !strings.Contains(err.Error(), "receipt status 0") {
		t.Fatal("chain still Pending: want the verify error")
	}
	esc.onChainTrade = seededBuyAdChainTrade(OnChainP2PStatusCancelled)
	got, err := svc.ConfirmCancel(context.Background(), tr.ID, tr.SellerID, "0xmine")
	if err != nil || got.Status != models.P2PTradeStatusCancelled {
		t.Fatalf("confirm = %v / %v, want the cancelled trade", got, err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", tr.AdID)
	if ad.Reserved != "0" || ad.Remaining != "100" {
		t.Fatalf("ad reserved=%s remaining=%s, want capacity restored once", ad.Reserved, ad.Remaining)
	}
}

// Review M7: a pool slice is invisible to the admin trade view and its
// messages, as everywhere outside the pool console.
func TestAdminTradeView_PoolSliceIsNotFound(t *testing.T) {
	r := newPoolRig(t)
	_, sl := isolatedSlice(t, r, models.P2PTradeStatusPendingPayment, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	if _, err := r.svc.AdminTradeDetail(context.Background(), sl.ID); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("AdminTradeDetail = %v, want not found", err)
	}
	if _, err := NewP2PMessageService(r.db, nil).AdminList(context.Background(), sl.ID, MessageFilter{}); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("AdminList = %v, want not found", err)
	}
}
