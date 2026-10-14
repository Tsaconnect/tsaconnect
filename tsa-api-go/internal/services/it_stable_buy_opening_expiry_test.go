package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openingExpiryService(t *testing.T) *ITService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, it_intent_id TEXT, status TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	return NewITService(db, nil, nil)
}

func insertOpeningBuy(t *testing.T, s *ITService, userID uuid.UUID, age time.Duration) uuid.UUID {
	t.Helper()
	in := &models.ITIntent{
		ID: uuid.New(), UserID: userID, Direction: models.ITDirectionBuy, Status: models.ITStatusOpening,
		Asset: models.ITAssetUSDT, Provider: models.ITProviderPool, McgpAmount: "0",
	}
	if err := s.db.Create(in).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", in.ID).Update("created_at", time.Now().Add(-age)).Error; err != nil {
		t.Fatal(err)
	}
	return in.ID
}

// A slice-era order left `opening` by a crash no longer wedges the user: past
// the old sweep's bound it is closed as a failed open, unless a slice of it
// is still live (LP money may be on-chain) or it is still fresh.
func TestExpireStaleStableBuys_ExpiresStuckOpening(t *testing.T) {
	ctx := context.Background()
	s := openingExpiryService(t)
	userID := uuid.New()
	stuck := insertOpeningBuy(t, s, userID, 10*time.Minute)
	withSlice := insertOpeningBuy(t, s, userID, 10*time.Minute)
	fresh := insertOpeningBuy(t, s, userID, 30*time.Second)
	withDeadSlice := insertOpeningBuy(t, s, userID, 10*time.Minute)
	for _, row := range []struct {
		intent uuid.UUID
		status string
	}{{withSlice, models.P2PTradeStatusPendingPayment}, {withDeadSlice, models.P2PTradeStatusCancelled}} {
		if err := s.db.Exec(`INSERT INTO p2p_trades VALUES (?, ?, ?)`, uuid.New(), row.intent, row.status).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := s.expireStaleStableBuys(ctx, userID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	for id, want := range map[uuid.UUID]string{
		stuck: models.ITStatusCancelled, withDeadSlice: models.ITStatusCancelled,
		withSlice: models.ITStatusOpening, fresh: models.ITStatusOpening,
	} {
		var got models.ITIntent
		if err := s.db.First(&got, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != want {
			t.Errorf("intent %s status = %s, want %s", id, got.Status, want)
		}
		if want == models.ITStatusCancelled && got.FailureReason != models.ITFailurePoolOpenFailed {
			t.Errorf("intent %s failure_reason = %q, want %q (hidden, never an abandon)", id, got.FailureReason, models.ITFailurePoolOpenFailed)
		}
	}
}
