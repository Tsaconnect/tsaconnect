package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// poolNoDeskRefundRig: a tsa_pool buy parked with no pool desk wired, and the
// tables its refund reads (slices, vault fills, sale legs, the audit row).
func poolNoDeskRefundRig(t *testing.T) (*ITService, *models.ITIntent) {
	t.Helper()
	s, userID := newRoutedBuyService(t)
	if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}, &models.PoolOrder{}, &models.RampLeg{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.db.Exec(`CREATE TABLE IF NOT EXISTS p2p_trades (id TEXT PRIMARY KEY, it_intent_id TEXT, status TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	return s, seedProviderIntent(t, s, userID, models.ITProviderPool, models.ITStatusBuyAwaitingRate)
}

func addSlice(t *testing.T, s *ITService, intentID uuid.UUID, status string) {
	t.Helper()
	if err := s.db.Exec(`INSERT INTO p2p_trades (id, it_intent_id, status) VALUES (?, ?, ?)`, uuid.New(), intentID, status).Error; err != nil {
		t.Fatal(err)
	}
}

func intentStatus(t *testing.T, s *ITService, id uuid.UUID) string {
	t.Helper()
	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return got.Status
}

// No desk, nothing moved (its only slice cancelled, no fill): the operator's
// naira refund is the order's exit, audited.
func TestMarkStableBuyRefunded_PoolWithoutDesk_RefundsWhenNothingMoved(t *testing.T) {
	s, intent := poolNoDeskRefundRig(t)
	addSlice(t, s, intent.ID, models.P2PTradeStatusCancelled)
	got, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "returned by bank transfer")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if got.Status != models.ITStatusRefunded {
		t.Fatalf("status = %s, want refunded", got.Status)
	}
	var audits int64
	s.db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ? AND action = ?", intent.ID, models.ITAuditStableBuyRefunded).Count(&audits)
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}
}

// No desk, but crypto may have moved: a slice not cancelled, or a recorded
// vault fill, refuses — the order stays parked for a hand settlement.
func TestMarkStableBuyRefunded_PoolWithoutDesk_RefusesWhenCryptoMayHaveMoved(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, s *ITService, id uuid.UUID)
		want  string
	}{
		{"released slice", func(t *testing.T, s *ITService, id uuid.UUID) {
			addSlice(t, s, id, models.P2PTradeStatusCancelled)
			addSlice(t, s, id, models.P2PTradeStatusReleased)
		}, "slice not cancelled"},
		{"live slice", func(t *testing.T, s *ITService, id uuid.UUID) {
			addSlice(t, s, id, models.P2PTradeStatusPendingPayment)
		}, "slice not cancelled"},
		{"vault fill recorded", func(t *testing.T, s *ITService, id uuid.UUID) {
			fill := "1000000"
			if err := s.db.Create(&models.PoolOrder{IntentID: id, Generation: 1, ChainID: 56, Token: "USDT", TokenDecimals: 18,
				MidMinor: "1", LPRateMinor: "1", DisplayRateMinor: "1", OpenedUnits: "0", SurplusUnits: "0",
				FillUnits: &fill, FillTxHash: "0xabc"}).Error; err != nil {
				t.Fatal(err)
			}
		}, "vault fill is recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, intent := poolNoDeskRefundRig(t)
			tc.setup(t, s, intent.ID)
			_, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), intent.ID, "note")
			if !errors.Is(err, ErrITBadState) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refund err = %v, want a bad-state refusal naming %q", err, tc.want)
			}
			if st := intentStatus(t, s, intent.ID); st != models.ITStatusBuyAwaitingRate {
				t.Fatalf("status = %s, want it still parked", st)
			}
		})
	}
}
