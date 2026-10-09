package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestAlertStalePaidTrades_OnceAfterThreshold(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, tradeID, _ := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	rec := &recordingAlerter{}
	svc.SetAlerter(rec)
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC) // newService fixed clock
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("marked_paid_at", now.Add(-3*time.Hour))
	n, err := svc.AlertStalePaidTrades(context.Background(), 2*time.Hour, 50)
	if err != nil || n != 1 || len(rec.got) != 1 || rec.got[0].Kind != P2PAlertPaidUnreleased || rec.got[0].Age != 3*time.Hour {
		t.Fatalf("n=%d err=%v got=%+v", n, err, rec.got)
	}
	if n, _ = svc.AlertStalePaidTrades(context.Background(), 2*time.Hour, 50); n != 0 {
		t.Fatalf("second pass alerted %d", n)
	}
}

func TestAlertStalePaidTrades_SkipsFreshAndPMTrades(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, tradeID, adID := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("marked_paid_at", now.Add(-1*time.Hour))
	if n, _ := svc.AlertStalePaidTrades(context.Background(), 2*time.Hour, 50); n != 0 {
		t.Fatal("fresh trade alerted")
	}
	db.Model(&models.P2PTrade{}).Where("id = ?", tradeID).Update("marked_paid_at", now.Add(-3*time.Hour))
	db.Model(&models.P2PAd{}).Where("id = ?", adID).Update("pm_id", uuid.New())
	if n, _ := svc.AlertStalePaidTrades(context.Background(), 2*time.Hour, 50); n != 0 {
		t.Fatal("PM trades are out of scope")
	}
}
