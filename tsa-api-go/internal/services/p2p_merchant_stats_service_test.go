package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newStatsSvc(t *testing.T) (*MerchantStatsService, *gorm.DB) {
	t.Helper()
	db := setupP2PTestDB(t) // tables already created by helper (raw DDL, SQLite-safe)
	return NewMerchantStatsServiceNoBus(db), db
}

func TestApplyReleasedCreditsBothParties(t *testing.T) {
	svc, db := newStatsSvc(t)
	buyer, seller := uuid.New(), uuid.New()
	mp := time.Now().Add(-3 * time.Minute)
	rel := time.Now()
	trade := models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller,
		Status: models.P2PTradeStatusReleased, MarkedPaidAt: &mp, ReleasedAt: &rel}
	if err := svc.ApplyReleased(context.Background(), &trade); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, id := range []uuid.UUID{buyer, seller} {
		var s models.P2PMerchantStats
		db.First(&s, "user_id = ?", id)
		if s.ReleasedCount != 1 {
			t.Fatalf("user %s releasedCount = %d, want 1", id, s.ReleasedCount)
		}
	}
}

func TestApplyCancelledBlamesBuyer(t *testing.T) {
	svc, db := newStatsSvc(t)
	buyer, seller := uuid.New(), uuid.New()
	trade := models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller, Status: models.P2PTradeStatusCancelled}
	if err := svc.ApplyCancelled(context.Background(), &trade); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var bs, ss models.P2PMerchantStats
	db.First(&bs, "user_id = ?", buyer)
	db.First(&ss, "user_id = ?", seller)
	if bs.CancelledCount != 1 {
		t.Fatalf("buyer cancelledCount = %d, want 1", bs.CancelledCount)
	}
	if ss.CancelledCount != 0 {
		t.Fatalf("seller cancelledCount = %d, want 0 (not at fault)", ss.CancelledCount)
	}
}

func TestApplyResolvedBlamesLoser(t *testing.T) {
	svc, db := newStatsSvc(t)
	buyer, seller := uuid.New(), uuid.New()
	trade := models.P2PTrade{ID: uuid.New(), BuyerID: buyer, SellerID: seller, Status: models.P2PTradeStatusResolved}
	if err := svc.ApplyResolved(context.Background(), &trade, true); err != nil { // releasedToBuyer=true -> seller lost
		t.Fatalf("apply: %v", err)
	}
	var ss models.P2PMerchantStats
	db.First(&ss, "user_id = ?", seller)
	if ss.CancelledCount != 1 {
		t.Fatalf("seller (loser) cancelledCount = %d, want 1", ss.CancelledCount)
	}
}

func TestRecordRatingAndGetStats(t *testing.T) {
	svc, _ := newStatsSvc(t)
	ratee := uuid.New()
	if err := svc.RecordRating(context.Background(), &models.P2PRating{
		ID: uuid.New(), TradeID: uuid.New(), RaterID: uuid.New(), RateeID: ratee, Value: 1,
	}); err != nil {
		t.Fatalf("rating: %v", err)
	}
	stats, err := svc.GetStats(context.Background(), []uuid.UUID{ratee})
	if err != nil {
		t.Fatalf("getstats: %v", err)
	}
	got := stats[ratee]
	if got.PositivePct == nil || *got.PositivePct != 1.0 {
		t.Fatalf("positivePct = %v, want 1.0", got.PositivePct)
	}
}

func TestGetStatsMissingIsNew(t *testing.T) {
	svc, _ := newStatsSvc(t)
	id := uuid.New()
	stats, err := svc.GetStats(context.Background(), []uuid.UUID{id})
	if err != nil {
		t.Fatalf("getstats: %v", err)
	}
	if !stats[id].IsNew {
		t.Fatalf("missing merchant should be IsNew=true")
	}
}
