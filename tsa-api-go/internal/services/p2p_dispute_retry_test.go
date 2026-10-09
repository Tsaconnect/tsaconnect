package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

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

func TestConfirmRaiseDispute_RefusesAMismatchedRaiser(t *testing.T) {
	svc, esc, tr := preparedDisputeFixture(t)
	seedUserAt(t, svc.db, tr.SellerID, models.VerificationStatusVerified)
	svc.db.Exec("UPDATE users SET wallet_address = ? WHERE id = ?", "0x000000000000000000000000000000000000a001", tr.SellerID)
	esc.disputedEvent = &P2PTradeDisputedEvent{TradeID: HashID(tr.ID), By: common.HexToAddress("0x000000000000000000000000000000000000b002")}
	if _, err := svc.ConfirmRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xdisp"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	esc.disputedEvent.By = common.HexToAddress("0x000000000000000000000000000000000000a001")
	if _, err := svc.ConfirmRaiseDispute(context.Background(), tr.ID, tr.SellerID, "0xdisp"); err != nil {
		t.Fatalf("matching raiser: %v", err)
	}
}
