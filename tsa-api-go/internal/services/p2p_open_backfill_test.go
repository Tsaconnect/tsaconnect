package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// lostOpen seeds a JIT buy-ad trade whose openTradeWithDeposit landed on chain
// but whose confirm-open never reached the API: open_tx_hash is empty while the
// escrow reports the trade Pending with the DB's amount and funding.
func lostOpen(t *testing.T, amount int64) (*P2PTradeService, *mockEscrow, models.P2PTrade, *gorm.DB, time.Time) {
	t.Helper()
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPendingPayment) // open_tx_hash empty
	created := time.Date(2026, 5, 23, 11, 50, 0, 0, time.UTC)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPending, Funding: models.P2PTradeFundingDepositedByTaker,
		Amount: big.NewInt(amount), CreatedAt: big.NewInt(created.Unix()), Exists: true}
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	return svc, esc, tr, db, created
}

func TestReconcile_BackfillsLostOpenConfirm(t *testing.T) {
	svc, _, tr, db, created := lostOpen(t, 30)
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || !out.Changed || out.Reason != "open backfilled" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.Status != models.P2PTradeStatusPendingPayment || after.OpenTxHash != "reconciled" ||
		!after.ExpiresAt.Equal(created.Add(30*time.Minute)) {
		t.Fatalf("after = %+v", after)
	}
	var audits int64
	db.Model(&models.P2PAuditLog{}).Where("trade_id = ? AND action = ?", tr.ID, models.P2PAuditReconcilerDrift).Count(&audits)
	if audits != 1 {
		t.Fatalf("reconciler drift audits = %d, want 1", audits)
	}
	if out2, _ := svc.ReconcileTrade(context.Background(), tr.ID); out2.Changed {
		t.Fatal("second pass must be a no-op")
	}
}

func TestReconcile_LostOpenWithWrongAmountIsNotBackfilled(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 31)
	_, _ = svc.ReconcileTrade(context.Background(), tr.ID)
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "" {
		t.Fatal("an amount mismatch must not be recorded as our open")
	}
}

func TestReconcile_LostOpenWithWrongFundingIsNotBackfilled(t *testing.T) {
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade.Funding = models.P2PTradeFundingPooled
	_, _ = svc.ReconcileTrade(context.Background(), tr.ID)
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "" {
		t.Fatal("a funding mismatch must not be recorded as our open")
	}
}

// A lost open-confirm on a trade whose markPaid also landed: the backfill
// records the open and the same pass still applies the chain-Paid transition.
func TestReconcile_LostOpenThenChainPaidStillFlipsToPaid(t *testing.T) {
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade.Status = OnChainP2PStatusPaid
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || !out.Changed || out.ToStatus != models.P2PTradeStatusPaid {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "reconciled" || after.Status != models.P2PTradeStatusPaid {
		t.Fatalf("after = %+v", after)
	}
}

func TestPrepareMarkPaid_AcceptsOpenConfirmedOnChain(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 30)
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://p/x.png"); err != nil {
		t.Fatalf("open is on chain, mark-paid must be allowed: %v", err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.OpenTxHash != "reconciled" {
		t.Fatal("gate should backfill")
	}
}

func TestPrepareMarkPaid_RefusesWhenOpenNotOnChain(t *testing.T) {
	svc, esc, tr, _, _ := lostOpen(t, 30)
	esc.onChainTrade = nil
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://p/x.png"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRaiseDispute_AcceptsOpenConfirmedOnChain(t *testing.T) {
	svc, _, tr, _, _ := lostOpen(t, 30)
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "no fiat", nil); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRaiseDispute_RefusesWhenOpenNotOnChain(t *testing.T) {
	svc, esc, tr, db, _ := lostOpen(t, 30)
	esc.onChainTrade = nil
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "no fiat", nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
	var disputes int64
	db.Model(&models.P2PDispute{}).Where("trade_id = ?", tr.ID).Count(&disputes)
	if disputes != 0 {
		t.Fatal("a refused dispute must not leave a dispute row")
	}
}

func TestConfirmOpenTrade_IdempotentOnceOpenRecordedAndMovedOn(t *testing.T) {
	svc, _, tr, db, _ := lostOpen(t, 30)
	db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Updates(map[string]any{"open_tx_hash": "reconciled", "status": models.P2PTradeStatusPaid})
	got, err := svc.ConfirmOpenTrade(context.Background(), tr.ID, tr.SellerID, "", "0xrealopen")
	if err != nil || got.Status != models.P2PTradeStatusPaid {
		t.Fatalf("retry-confirm after backfill must succeed: %+v %v", got, err)
	}
}
