package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func migrateIntentAudit(t *testing.T, s *ITService) {
	t.Helper()
	if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}); err != nil {
		t.Fatalf("migrate audit: %v", err)
	}
}

// Review Focus 4 / P5: the unknown-provider park has one exit.
func TestMarkStableBuyRefunded_UnknownProviderParkRefundable(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	migrateIntentAudit(t, s)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusBuyAwaitingRate)
	got, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned by bank transfer")
	if err != nil || got.Status != models.ITStatusRefunded {
		t.Fatalf("intent = %+v err = %v, want refunded", got, err)
	}
	var audits int64
	s.db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ?", intent.ID).Count(&audits)
	if audits != 1 {
		t.Fatalf("%d audit rows, want 1", audits)
	}
}

// Review M5: the park refunds once; a second refund is refused, row untouched,
// no second audit row.
func TestMarkStableBuyRefunded_UnknownProviderSecondRefundRefused(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	migrateIntentAudit(t, s)
	intent := seedProviderIntent(t, s, userID, "bogus", models.ITStatusBuyAwaitingRate)
	if _, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned"); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	if _, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned again"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("second refund: err = %v, want ErrITBadState", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	var audits int64
	s.db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ?", intent.ID).Count(&audits)
	if got.Status != models.ITStatusRefunded || audits != 1 {
		t.Fatalf("status = %s, %d audit rows; want refunded once", got.Status, audits)
	}
}

// Never from a status where a provider might still act (S37).
func TestMarkStableBuyRefunded_UnknownProviderNonParkRefused(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	migrateIntentAudit(t, s)
	for _, st := range []string{models.ITStatusDelivering, models.ITStatusPaymentReceived, models.ITStatusSettled} {
		intent := seedProviderIntent(t, s, userID, "bogus", st)
		if _, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note"); !errors.Is(err, ErrITBadState) {
			t.Fatalf("%s: err = %v, want ErrITBadState", st, err)
		}
		var got models.ITIntent
		s.db.First(&got, "id = ?", intent.ID)
		if got.Status != st {
			t.Fatalf("%s: status = %s, want untouched", st, got.Status)
		}
	}
}

func TestMarkStableBuyRefunded_FlutterwaveParkStillRefunds(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	migrateIntentAudit(t, s)
	intent := seedProviderIntent(t, s, userID, "", models.ITStatusBuyAwaitingRate)
	got, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned")
	if err != nil || got.Status != models.ITStatusRefunded {
		t.Fatalf("intent = %+v err = %v", got, err)
	}
}
