package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

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
	if _, err := svc.PrepareRaiseDispute(context.Background(), tradeID, tr.BuyerID, "0xb", "second", nil); !errors.Is(err, ErrDisputeAlreadyOpen) {
		t.Fatalf("err = %v", err)
	}
}
