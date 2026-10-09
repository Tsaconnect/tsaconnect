package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestPrepareRaiseDispute_ReplacesAnUnconfirmedDispute(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("open_tx_hash", "0xopen")
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPaid, Exists: true, Amount: big.NewInt(30), Funding: 1}
	ctx := context.Background()
	if _, err := svc.PrepareRaiseDispute(ctx, tradeID, tr.SellerID, "0xa", "first try", nil); err != nil {
		t.Fatal(err)
	}
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC).Add(disputePrepareStaleAfter + time.Second)})
	if _, err := svc.PrepareRaiseDispute(ctx, tradeID, tr.BuyerID, "0xb", "second try", []string{"https://cdn.test/e.jpg"}); err != nil {
		t.Fatalf("an unbroadcast dispute must not block a retry: %v", err)
	}
	var rows []models.P2PDispute
	db.Where("trade_id = ?", tradeID).Find(&rows)
	if len(rows) != 1 || rows[0].RaisedBy != tr.BuyerID || rows[0].RaisedRole != models.P2PDisputeRoleBuyer ||
		rows[0].Reason != "second try" || len(rows[0].Evidence) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestPrepareRaiseDispute_ChainAlreadyDisputedStillRefuses(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("open_tx_hash", "0xopen")
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPaid, Exists: true, Amount: big.NewInt(30), Funding: 1}
	_, _ = svc.PrepareRaiseDispute(context.Background(), tradeID, tr.SellerID, "0xa", "first", nil)
	esc.onChainTrade.Status = OnChainP2PStatusDisputed
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC).Add(time.Hour)})
	if _, err := svc.PrepareRaiseDispute(context.Background(), tradeID, tr.BuyerID, "0xb", "second", nil); !errors.Is(err, ErrDisputeAlreadyOpen) {
		t.Fatalf("err = %v", err)
	}
}

func preparedDisputeFixture(t *testing.T) (*P2PTradeService, *mockEscrow, models.P2PTrade) {
	t.Helper()
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("open_tx_hash", "0xopen")
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPaid, Exists: true, Amount: big.NewInt(30), Funding: 1}
	if _, err := svc.PrepareRaiseDispute(context.Background(), tradeID, tr.SellerID, "0xa", "first try", nil); err != nil {
		t.Fatal(err)
	}
	return svc, esc, tr
}

func TestPrepareRaiseDispute_OtherPartyCannotReplaceALivePrepare(t *testing.T) {
	svc, _, tr := preparedDisputeFixture(t)
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.BuyerID, "0xb", "hijack", nil); !errors.Is(err, ErrDisputeAlreadyOpen) {
		t.Fatalf("err = %v", err)
	}
	var row models.P2PDispute
	svc.db.First(&row, "trade_id = ?", tr.ID)
	if row.RaisedBy != tr.SellerID || row.Reason != "first try" {
		t.Fatalf("row changed: %+v", row)
	}
}

func TestPrepareRaiseDispute_SameRaiserReplacesAnytime(t *testing.T) {
	svc, _, tr := preparedDisputeFixture(t)
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "retry", nil); err != nil {
		t.Fatal(err)
	}
	var row models.P2PDispute
	svc.db.First(&row, "trade_id = ?", tr.ID)
	if row.Reason != "retry" {
		t.Fatalf("row = %+v", row)
	}
}

func TestConfirmRaiseDispute_RaiserMustBeTheOnChainParty(t *testing.T) {
	svc, esc, tr := preparedDisputeFixture(t) // seller raised
	sellerAddr := common.HexToAddress("0x000000000000000000000000000000000000a001")
	buyerAddr := common.HexToAddress("0x000000000000000000000000000000000000b002")
	esc.onChainTrade.Seller, esc.onChainTrade.Buyer = sellerAddr, buyerAddr
	// users.wallet_address is irrelevant: the chain is the authority.
	esc.disputedEvent = &P2PTradeDisputedEvent{TradeID: HashID(tr.ID), By: buyerAddr}
	if _, err := svc.ConfirmRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xdisp"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mismatch: err = %v", err)
	}
	esc.disputedEvent.By = sellerAddr
	if _, err := svc.ConfirmRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xdisp"); err != nil {
		t.Fatalf("matching on-chain party: %v", err)
	}
}

func TestConfirmRaiseDispute_ChainReadErrorIsUnavailableNotSkipped(t *testing.T) {
	svc, esc, tr := preparedDisputeFixture(t)
	esc.readTradeErr = errors.New("rpc down")
	if _, err := svc.ConfirmRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xdisp"); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRaiseDispute_ReplaceChainReadErrorIsUnavailable(t *testing.T) {
	svc, esc, tr := preparedDisputeFixture(t)
	esc.readTradeErr = errors.New("rpc down")
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "retry", nil); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("err = %v", err)
	}
	var row models.P2PDispute
	svc.db.First(&row, "trade_id = ?", tr.ID)
	if row.Reason != "first try" {
		t.Fatalf("row changed: %+v", row)
	}
}

func TestPrepareRaiseDispute_ReplaceRaceIs409(t *testing.T) {
	svc, _, tr := preparedDisputeFixture(t)
	// Another request replaced the row between our read and write: simulate by
	// bumping created_at from a callback after the read.
	svc.db.Callback().Query().After("gorm:query").Register("race", func(d *gorm.DB) {
		if d.Statement.Table == "p2p_disputes" {
			d.Session(&gorm.Session{NewDB: true}).Exec("UPDATE p2p_disputes SET raised_by = ? WHERE trade_id = ?", tr.BuyerID, tr.ID)
			svc.db.Callback().Query().Remove("race")
		}
	})
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xa", "retry", nil); !errors.Is(err, ErrDisputeAlreadyOpen) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRaiseDispute_BuildFailureLeavesRowAndAuditUnchanged(t *testing.T) {
	svc, esc, tr := preparedDisputeFixture(t)
	var before models.P2PDispute
	svc.db.First(&before, "trade_id = ?", tr.ID)
	var auditsBefore int64
	svc.db.Model(&models.P2PAuditLog{}).Count(&auditsBefore)
	esc.raiseDisputeErr = errors.New("nonce too low")
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 23, 14, 0, 0, 0, time.UTC)})
	if _, err := svc.PrepareRaiseDispute(context.Background(), tr.ID, tr.BuyerID, "0xb", "late", nil); err == nil {
		t.Fatal("expected build failure")
	}
	var after models.P2PDispute
	svc.db.First(&after, "trade_id = ?", tr.ID)
	var auditsAfter int64
	svc.db.Model(&models.P2PAuditLog{}).Count(&auditsAfter)
	if after.RaisedBy != before.RaisedBy || after.Reason != before.Reason || !after.CreatedAt.Equal(before.CreatedAt) || auditsAfter != auditsBefore {
		t.Fatalf("changed: %+v -> %+v audits %d -> %d", before, after, auditsBefore, auditsAfter)
	}
}

func TestDisputeFiled_AlertsOnceFromConfirmOrReconciler(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("open_tx_hash", "0xopen")
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	rec := &recordingAlerter{}
	svc.SetAlerter(rec)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusPaid, Exists: true, Amount: big.NewInt(30), Funding: 1}
	ctx := context.Background()
	if _, err := svc.PrepareRaiseDispute(ctx, tradeID, tr.SellerID, "0xa", "no fiat received", nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("prepare must not alert, got %+v", rec.got)
	}
	if _, err := svc.ConfirmRaiseDispute(ctx, tradeID, tr.SellerID, "0xdisp"); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.ConfirmRaiseDispute(ctx, tradeID, tr.SellerID, "0xdisp")
	esc.onChainTrade.Status = OnChainP2PStatusDisputed
	_, _ = svc.ReconcileTrade(ctx, tradeID)
	if len(rec.got) != 1 || rec.got[0].Kind != P2PAlertDisputeFiled || rec.got[0].RaisedRole != "seller" ||
		rec.got[0].Reason != "no fiat received" || rec.got[0].FromStatus != "paid" {
		t.Fatalf("alerts = %+v", rec.got)
	}
}

func TestReconcileToDisputed_BackfillAlerts(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	// A trade with no open_tx_hash goes down the open-backfill path instead.
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("open_tx_hash", "0xopen")
	rec := &recordingAlerter{}
	svc.SetAlerter(rec)
	esc.onChainTrade = &OnChainP2PTrade{Status: OnChainP2PStatusDisputed, Exists: true, Amount: big.NewInt(30), Funding: 1}
	_, _ = svc.ReconcileTrade(context.Background(), tradeID)
	_, _ = svc.ReconcileTrade(context.Background(), tradeID)
	if len(rec.got) != 1 {
		t.Fatalf("alerts = %d, want 1", len(rec.got))
	}
}
