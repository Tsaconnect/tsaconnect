package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// newRampLegStore opens an in-memory SQLite DB migrated for RampLeg, matching
// the newSponsorshipStore(t) convention next door.
func newRampLegStore(t *testing.T) *GormRampLegStore {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.RampLeg{}); err != nil {
		t.Fatalf("migrate ramp_legs: %v", err)
	}
	return &GormRampLegStore{DB: db}
}

func TestGormRampLegStore_ReserveIsIdempotentPerAttempt(t *testing.T) {
	store := newRampLegStore(t)
	orderID := uuid.New()
	leg := &models.RampLeg{OrderID: orderID, Kind: models.RampLegKindBridge, Attempt: 1,
		ChainKey: "base", Token: "USDC", FromAddr: "0xT", ToAddr: "0xU", Amount: "100", Status: models.RampLegStatusPending}

	existing, err := store.Reserve(context.Background(), leg)
	if err != nil || existing != nil {
		t.Fatalf("first reserve: existing=%v err=%v", existing, err)
	}
	again, err := store.Reserve(context.Background(), &models.RampLeg{OrderID: orderID, Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending})
	if err != nil || again == nil || again.ID != leg.ID {
		t.Fatalf("second reserve must return the first row, got %v err=%v", again, err)
	}
	next, err := store.NextAttempt(context.Background(), orderID, models.RampLegKindBridge)
	if err != nil || next != 2 {
		t.Fatalf("NextAttempt = %d, %v; want 2", next, err)
	}
}

func TestGormRampLegStore_ConfirmRecordsWhatLanded(t *testing.T) {
	store := newRampLegStore(t)
	leg := &models.RampLeg{OrderID: uuid.New(), Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending}
	if _, err := store.Reserve(context.Background(), leg); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTxHash(context.Background(), leg.ID, "0xabc", "across"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.Confirm(context.Background(), leg.ID, "99700000", at); err != nil {
		t.Fatal(err)
	}
	legs, err := store.ForOrder(context.Background(), leg.OrderID)
	if err != nil || len(legs) != 1 {
		t.Fatalf("legs = %v, err = %v", legs, err)
	}
	got := legs[0]
	if got.Status != models.RampLegStatusConfirmed || got.AmountReceived != "99700000" || got.TxHash != "0xabc" || got.LiFiTool != "across" || got.ConfirmedAt == nil {
		t.Fatalf("leg = %+v", got)
	}
}

func TestGormRampLegStore_FailRecordsReason(t *testing.T) {
	store := newRampLegStore(t)
	leg := &models.RampLeg{OrderID: uuid.New(), Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending}
	if _, err := store.Reserve(context.Background(), leg); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(context.Background(), leg.ID, "route expired"); err != nil {
		t.Fatal(err)
	}
	legs, err := store.ForOrder(context.Background(), leg.OrderID)
	if err != nil || len(legs) != 1 {
		t.Fatalf("legs = %v, err = %v", legs, err)
	}
	if legs[0].Status != models.RampLegStatusFailed || legs[0].Error != "route expired" {
		t.Fatalf("leg = %+v", legs[0])
	}
}

func TestGormRampLegStore_NextAttemptStartsAtOne(t *testing.T) {
	store := newRampLegStore(t)
	next, err := store.NextAttempt(context.Background(), uuid.New(), models.RampLegKindBridge)
	if err != nil || next != 1 {
		t.Fatalf("NextAttempt on empty order = %d, %v; want 1", next, err)
	}
}

// A late or duplicate Confirm/Fail must never overwrite a leg that already
// reached a terminal status: these rows are the arithmetic a refund is
// computed from, so a self-contradicting row (e.g. status=failed but
// amount_received/confirmed_at still populated) is a money bug.

func TestGormRampLegStore_ConfirmAfterConfirmReturnsErrLegNotPending(t *testing.T) {
	store := newRampLegStore(t)
	leg := &models.RampLeg{OrderID: uuid.New(), Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending}
	if _, err := store.Reserve(context.Background(), leg); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.Confirm(context.Background(), leg.ID, "100", at); err != nil {
		t.Fatal(err)
	}
	// A second, late confirm with a different amount must be rejected and
	// must not touch the row.
	err := store.Confirm(context.Background(), leg.ID, "999", at.Add(time.Hour))
	if !errors.Is(err, ErrLegNotPending) {
		t.Fatalf("second Confirm error = %v; want ErrLegNotPending", err)
	}
	legs, err := store.ForOrder(context.Background(), leg.OrderID)
	if err != nil || len(legs) != 1 {
		t.Fatalf("legs = %v, err = %v", legs, err)
	}
	got := legs[0]
	if got.Status != models.RampLegStatusConfirmed || got.AmountReceived != "100" {
		t.Fatalf("leg mutated by rejected confirm: %+v", got)
	}
}

func TestGormRampLegStore_FailAfterConfirmReturnsErrLegNotPending(t *testing.T) {
	store := newRampLegStore(t)
	leg := &models.RampLeg{OrderID: uuid.New(), Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending}
	if _, err := store.Reserve(context.Background(), leg); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.Confirm(context.Background(), leg.ID, "100", at); err != nil {
		t.Fatal(err)
	}
	err := store.Fail(context.Background(), leg.ID, "arrived late")
	if !errors.Is(err, ErrLegNotPending) {
		t.Fatalf("Fail-after-Confirm error = %v; want ErrLegNotPending", err)
	}
	legs, err := store.ForOrder(context.Background(), leg.OrderID)
	if err != nil || len(legs) != 1 {
		t.Fatalf("legs = %v, err = %v", legs, err)
	}
	got := legs[0]
	if got.Status != models.RampLegStatusConfirmed || got.AmountReceived != "100" || got.ConfirmedAt == nil {
		t.Fatalf("leg mutated by rejected fail: %+v", got)
	}
}

func TestGormRampLegStore_ReserveDefaultsAttemptAndStatus(t *testing.T) {
	store := newRampLegStore(t)
	leg := &models.RampLeg{OrderID: uuid.New(), Kind: models.RampLegKindBridge}
	if _, err := store.Reserve(context.Background(), leg); err != nil {
		t.Fatal(err)
	}
	legs, err := store.ForOrder(context.Background(), leg.OrderID)
	if err != nil || len(legs) != 1 {
		t.Fatalf("legs = %v, err = %v", legs, err)
	}
	if legs[0].Attempt != 1 || legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("leg = %+v; want attempt=1 status=pending", legs[0])
	}
}
