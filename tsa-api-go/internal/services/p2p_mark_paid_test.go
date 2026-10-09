package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const testPayerAddr = "0x000000000000000000000000000000000000d00d"

// markPaidViaChain drives the fiat payer's mark-paid the way the app does now:
// prepare the on-chain markPaid, then confirm on its TradePaid receipt.
func markPaidViaChain(t *testing.T, svc *P2PTradeService, tradeID, payerID uuid.UUID) {
	t.Helper()
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tradeID, payerID, testPayerAddr, "https://proof/x.png"); err != nil {
		t.Fatalf("PrepareMarkPaid: %v", err)
	}
	if _, err := svc.ConfirmMarkPaid(context.Background(), tradeID, payerID, "0xpaid"); err != nil {
		t.Fatalf("ConfirmMarkPaid: %v", err)
	}
}

func openedBuyAdTrade(t *testing.T, status string) (*P2PTradeService, *mockEscrow, models.P2PTrade, *gorm.DB) {
	t.Helper()
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, status)
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("open_tx_hash", "0xopen")
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	return svc, esc, tr, db
}

func TestPrepareMarkPaid_ReturnsTxKeepsPendingStoresProof(t *testing.T) {
	svc, _, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	got, raw, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://proof/x.png")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"tx":"markPaid"}` || got.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("raw=%s status=%s", raw, got.Status)
	}
	if got.ProofImageURL != "https://proof/x.png" {
		t.Fatalf("returned trade proof = %q", got.ProofImageURL)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.Status != models.P2PTradeStatusPendingPayment || after.ProofImageURL != "https://proof/x.png" {
		t.Fatalf("after = %s / %q", after.Status, after.ProofImageURL)
	}
	if after.MarkedPaidAt != nil {
		t.Fatal("prepare must not stamp marked_paid_at — only the verified confirm does")
	}
}

func TestPrepareMarkPaid_OnlyTheFiatPayer(t *testing.T) {
	svc, _, tr, _ := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.SellerID, testPayerAddr, "https://p/x.png"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("crypto provider must not mark paid: %v", err)
	}
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, uuid.New(), testPayerAddr, "https://p/x.png"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger must not mark paid: %v", err)
	}
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, ""); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("proof is required: %v", err)
	}
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "file:///x.png"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("proof must be an http(s) URL: %v", err)
	}
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, "not-an-address", "https://p/x.png"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("payer address must be a hex address: %v", err)
	}
}

func TestPrepareMarkPaid_RefusedOutsidePendingPayment(t *testing.T) {
	svc, _, tr, _ := openedBuyAdTrade(t, models.P2PTradeStatusPaid)
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://p/x.png"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("already-paid trade: %v", err)
	}
}

func TestPrepareMarkPaid_RevertMapsTo422AndSavesNothing(t *testing.T) {
	svc, esc, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	esc.prepareMarkPaidErr = errors.New("execution reverted: Not buyer")
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://p/x.png"); !errors.Is(err, ErrTradePrepareReverted) {
		t.Fatalf("revert must map to ErrTradePrepareReverted: %v", err)
	}
	esc.prepareMarkPaidErr = errors.New("dial tcp: connection refused")
	if _, _, err := svc.PrepareMarkPaid(context.Background(), tr.ID, tr.BuyerID, testPayerAddr, "https://p/x.png"); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("transport failure must map to ErrChainUnavailable: %v", err)
	}
	var after models.P2PTrade
	db.First(&after, "id = ?", tr.ID)
	if after.ProofImageURL != "" || after.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("a failed prepare must change nothing: %s / %q", after.Status, after.ProofImageURL)
	}
}

func TestConfirmMarkPaid_VerifiesReceiptThenFlips(t *testing.T) {
	svc, esc, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	ctx := context.Background()
	if _, _, err := svc.PrepareMarkPaid(ctx, tr.ID, tr.BuyerID, testPayerAddr, "https://p/x.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmMarkPaid(ctx, tr.ID, tr.SellerID, "0xpaid"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("only the payer confirms: %v", err)
	}
	esc.verifyPaidErr = errors.New("TradePaid event not found")
	if _, err := svc.ConfirmMarkPaid(ctx, tr.ID, tr.BuyerID, "0xbad"); err == nil {
		t.Fatal("unverified receipt must not flip")
	}
	var mid models.P2PTrade
	db.First(&mid, "id = ?", tr.ID)
	if mid.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("status after failed verify = %s", mid.Status)
	}
	esc.verifyPaidErr = nil
	got, err := svc.ConfirmMarkPaid(ctx, tr.ID, tr.BuyerID, "0xpaid")
	if err != nil || got.Status != models.P2PTradeStatusPaid || got.MarkedPaidAt == nil {
		t.Fatalf("got %+v err %v", got, err)
	}
	if got.ProofImageURL != "https://p/x.png" {
		t.Fatalf("confirm must keep the proof saved at prepare: %q", got.ProofImageURL)
	}
	if _, err := svc.ConfirmMarkPaid(ctx, tr.ID, tr.BuyerID, "0xpaid"); err != nil {
		t.Fatalf("replay must be idempotent: %v", err)
	}
	var n int64
	db.Table("p2p_audit_logs").Where("trade_id = ? AND action = ?", tr.ID, models.P2PAuditTradeMarkedPaid).Count(&n)
	if n != 1 {
		t.Fatalf("marked_paid audits = %d, want 1", n)
	}
}

func TestConfirmMarkPaid_RefusedOnCancelledTrade(t *testing.T) {
	svc, _, tr, _ := openedBuyAdTrade(t, models.P2PTradeStatusCancelled)
	if _, err := svc.ConfirmMarkPaid(context.Background(), tr.ID, tr.BuyerID, "0xpaid"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancelled trade: %v", err)
	}
}

func TestReconcile_ChainPaidBackfillUsesMarkPaidCore(t *testing.T) {
	svc, esc, tr, db := openedBuyAdTrade(t, models.P2PTradeStatusPendingPayment)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPaid, Funding: models.P2PTradeFundingDepositedByTaker, Amount: big.NewInt(30), Exists: true}
	out, err := svc.ReconcileTrade(context.Background(), tr.ID)
	if err != nil || !out.Changed || out.ToStatus != models.P2PTradeStatusPaid {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var n int64
	db.Table("p2p_audit_logs").Where("trade_id = ? AND action = ?", tr.ID, models.P2PAuditTradeMarkedPaid).Count(&n)
	if n != 1 {
		t.Fatalf("reconciler backfill must record the mark-paid like a confirm (audits=%d)", n)
	}
	db.Table("p2p_audit_logs").Where("trade_id = ? AND action = ?", tr.ID, models.P2PAuditReconcilerDrift).Count(&n)
	if n != 1 {
		t.Fatalf("reconciler drift audit kept (audits=%d)", n)
	}
}

func TestReconcile_CancellingAPaidTradeAlerts(t *testing.T) {
	for _, tc := range []struct {
		status string
		alerts int
	}{{models.P2PTradeStatusPaid, 1}, {models.P2PTradeStatusReleasing, 1}, {models.P2PTradeStatusPendingPayment, 0}} {
		db := setupTradeDB(t)
		svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, tc.status)
		rec := &recordingAlerter{}
		svc.SetAlerter(rec)
		esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusCancelled, Funding: models.P2PTradeFundingDepositedByTaker, Amount: big.NewInt(30), Exists: true}
		out, err := svc.ReconcileTrade(context.Background(), tradeID)
		if err != nil || out.ToStatus != models.P2PTradeStatusCancelled {
			t.Fatalf("%s: out=%+v err=%v", tc.status, out, err)
		}
		if len(rec.got) != tc.alerts {
			t.Fatalf("%s: alerts=%d want %d", tc.status, len(rec.got), tc.alerts)
		}
		if tc.alerts == 1 && (rec.got[0].Kind != P2PAlertPaidTradeCancelled || rec.got[0].FromStatus != tc.status) {
			t.Fatalf("alert = %+v", rec.got[0])
		}
	}
}

func TestReconcileToCancelled_UsesLockedStatusNotPreRead(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	rec := &recordingAlerter{}
	svc.SetAlerter(rec)
	var stale models.P2PTrade
	if err := db.First(&stale, "id = ?", tradeID).Error; err != nil {
		t.Fatal(err)
	}
	stale.Status = models.P2PTradeStatusPendingPayment // pre-read before confirm landed
	out, err := svc.reconcileToCancelled(context.Background(), &stale, models.P2PTradeFundingDepositedByTaker)
	if err != nil || !out.Changed || out.FromStatus != models.P2PTradeStatusPaid {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(rec.got) != 1 || rec.got[0].Kind != P2PAlertPaidTradeCancelled || rec.got[0].FromStatus != "paid" {
		t.Fatalf("alerts=%+v", rec.got)
	}
}

func TestReconcileToCancelled_AlreadyTerminalIsNoOp(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusCancelled)
	rec := &recordingAlerter{}
	svc.SetAlerter(rec)
	var tr models.P2PTrade
	if err := db.First(&tr, "id = ?", tradeID).Error; err != nil {
		t.Fatal(err)
	}
	tr.Status = models.P2PTradeStatusPaid // stale pre-read
	var before int64
	db.Model(&models.P2PAuditLog{}).Count(&before)
	out, err := svc.reconcileToCancelled(context.Background(), &tr, models.P2PTradeFundingDepositedByTaker)
	if err != nil || out.Changed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var after int64
	db.Model(&models.P2PAuditLog{}).Count(&after)
	if after != before || len(rec.got) != 0 {
		t.Fatalf("audit %d->%d alerts=%d", before, after, len(rec.got))
	}
}
