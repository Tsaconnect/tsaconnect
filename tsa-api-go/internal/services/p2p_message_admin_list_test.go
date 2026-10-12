package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// A dispute reviewer needs the latest messages of a long thread: AdminList
// returns the NEWEST `limit`, shown oldest-first. The participant list keeps
// paging forward from the oldest.
func TestAdminList_ReturnsTheNewestMessagesOldestFirst(t *testing.T) {
	db := setupTradeDB(t)
	_, _, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusDisputed)
	var tr models.P2PTrade
	db.First(&tr, "id = ?", tradeID)
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := db.Create(&models.P2PMessage{ID: uuid.New(), TradeID: tradeID, SenderID: &tr.BuyerID,
			Type: models.P2PMessageTypeText, Body: fmt.Sprintf("m%d", i), CreatedAt: base.Add(time.Duration(i) * time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := NewP2PMessageService(db, nil)

	got, err := svc.AdminList(context.Background(), tradeID, MessageFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != "m3" || got[1].Body != "m4" {
		t.Fatalf("admin = %v, want [m3 m4]", bodies(got))
	}

	mine, err := svc.List(context.Background(), tradeID, tr.BuyerID, MessageFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 || mine[0].Body != "m0" || mine[1].Body != "m1" {
		t.Fatalf("participant = %v, want [m0 m1]", bodies(mine))
	}
}

func bodies(ms []models.P2PMessage) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Body
	}
	return out
}
