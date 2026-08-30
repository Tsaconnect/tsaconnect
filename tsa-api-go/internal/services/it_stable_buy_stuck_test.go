package services

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestListStuckStableBuys(t *testing.T) {
	s, userID := newStableBuyDBService(t)

	fresh, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(fresh).Update("status", models.ITStatusAwaitingFunds)

	stuck, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(stuck).Updates(map[string]any{
		"status":     models.ITStatusConverting,
		"updated_at": time.Now().Add(-2 * time.Hour),
	})

	got, err := s.ListStuckStableBuys(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != stuck.ID {
		t.Fatalf("got %+v, want only the stale intent", got)
	}
}

func TestListStuckStableBuys_IncludesEveryNonTerminalState(t *testing.T) {
	// A state missing here is an intent holding user funds that no operator
	// ever sees.
	s, userID := newStableBuyDBService(t)
	for _, st := range []string{
		models.ITStatusPaymentReceived,
		models.ITStatusAwaitingFunds,
		models.ITStatusConverting,
		models.ITStatusDelivering,
		// Parked by settleStableBuyFromCharge when the money landed but could
		// not be priced. This is the state that MOST needs an operator, so it
		// must never be missing from the stuck list.
		models.ITStatusBuyAwaitingRate,
	} {
		i, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
		// ONE Updates call. A second write without updated_at makes GORM stamp
		// it to now, pushing every row back inside the cutoff so the query
		// returns nothing. The status change alone frees the pending_payment
		// slot — no payment_ref write is needed.
		s.db.Model(i).Updates(map[string]any{"status": st, "updated_at": time.Now().Add(-2 * time.Hour)})
	}
	got, err := s.ListStuckStableBuys(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d stuck intents, want 5 — a non-terminal state is unmonitored", len(got))
	}
}
