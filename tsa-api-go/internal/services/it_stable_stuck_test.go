package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestListStuckStableSells_OnlyReportsIntentsPastTheThreshold(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	userID := uuid.New()
	fresh, _, _ := s.CreateStableSellIntent(context.Background(), userID, testSellQuote(), "POLYGON", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), userID, fresh.ID, "0xfresh")

	got, err := s.ListStuckStableSells(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ListStuckStableSells: %v", err)
	}
	for _, r := range got {
		if r.IntentID == fresh.ID {
			t.Fatal("a just-created intent was reported as stuck")
		}
	}
}

func TestListStuckStableSells_CarriesTheEvidenceNeededToResolve(t *testing.T) {
	// An operator resolving a stuck deposit needs the transaction hashes — the
	// on-chain proof is the only leverage against a provider that has not
	// credited the funds.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	userID := uuid.New()
	intent, _, _ := s.CreateStableSellIntent(context.Background(), userID, testSellQuote(), "POLYGON", testSellBank())
	_ = s.ReportStableDeposit(context.Background(), userID, intent.ID, "0xevidence")
	backdateIntent(t, s, intent.ID, time.Now().Add(-2*time.Hour))

	got, err := s.ListStuckStableSells(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ListStuckStableSells: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("stuck = %d, want 1", len(got))
	}
	if got[0].DepositTxHash != "0xevidence" {
		t.Fatalf("depositTxHash = %q, want the reported hash", got[0].DepositTxHash)
	}
	if got[0].Status != models.ITStatusDepositReported {
		t.Fatalf("status = %q", got[0].Status)
	}
}

func TestListStuckStableSells_IgnoresTerminalIntents(t *testing.T) {
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	userID := uuid.New()
	intent, _, _ := s.CreateStableSellIntent(context.Background(), userID, testSellQuote(), "POLYGON", testSellBank())
	if err := s.db.Model(intent).Update("status", models.ITStatusSettled).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	backdateIntent(t, s, intent.ID, time.Now().Add(-2*time.Hour))

	got, err := s.ListStuckStableSells(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ListStuckStableSells: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("settled intent reported as stuck: %+v", got)
	}
}

// backdateIntent ages an intent so threshold logic can be tested without sleeping.
func backdateIntent(t *testing.T, s *ITService, id uuid.UUID, at time.Time) {
	t.Helper()
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", id).
		UpdateColumn("updated_at", at).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func TestListStuckStableSells_SurfacesASellStalledAtDebited(t *testing.T) {
	// `debited` is where the fiat exists but has not reached the user's bank.
	// It is the state every brokered sell passed through with no payout wired,
	// and it was invisible to this list — the one tool built to catch stalls
	// could not see the only stall that mattered.
	s := newStableSellDBService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)
	userID := uuid.New()
	intent, _, _ := s.CreateStableSellIntent(context.Background(), userID, testSellQuote(), "POLYGON", testSellBank())
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Update("status", models.ITStatusDebited).Error; err != nil {
		t.Fatalf("seed debited: %v", err)
	}
	backdateIntent(t, s, intent.ID, time.Now().Add(-2*time.Hour))

	got, err := s.ListStuckStableSells(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ListStuckStableSells: %v", err)
	}
	found := false
	for _, r := range got {
		if r.IntentID == intent.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("a sell stalled at debited is not reported as stuck: %v", got)
	}
}
