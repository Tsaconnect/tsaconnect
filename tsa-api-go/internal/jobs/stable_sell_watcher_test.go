package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeDriver struct {
	reported   []uuid.UUID
	confirmed  []uuid.UUID
	converted  []uuid.UUID
	converting []uuid.UUID
	finalized  []uuid.UUID
	debited    []uuid.UUID
	paidOut    []uuid.UUID

	confirmErr  error
	convertErr  error
	finalizeErr error
	payoutErr   error
}

func (f *fakeDriver) PendingStableSells(context.Context) ([]uuid.UUID, []uuid.UUID, []uuid.UUID, []uuid.UUID, error) {
	return f.reported, f.confirmed, f.converting, f.debited, nil
}
func (f *fakeDriver) PayoutStableSell(_ context.Context, id uuid.UUID) error {
	f.paidOut = append(f.paidOut, id)
	return f.payoutErr
}
func (f *fakeDriver) FinalizeConversion(_ context.Context, id uuid.UUID) error {
	f.finalized = append(f.finalized, id)
	return f.finalizeErr
}
func (f *fakeDriver) ConfirmStableDeposit(_ context.Context, id uuid.UUID) error {
	f.confirmed = append(f.confirmed, id)
	return f.confirmErr
}
func (f *fakeDriver) ConvertAndQueuePayout(_ context.Context, id uuid.UUID) error {
	f.converted = append(f.converted, id)
	return f.convertErr
}

func TestStableSellWatcher_ConfirmsReportedDeposits(t *testing.T) {
	id := uuid.New()
	f := &fakeDriver{reported: []uuid.UUID{id}}
	if err := NewStableSellWatcher(f, time.Minute).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.confirmed) == 0 || f.confirmed[0] != id {
		t.Fatalf("reported deposit was not confirmed: %v", f.confirmed)
	}
}

func TestStableSellWatcher_ConvertsConfirmedDeposits(t *testing.T) {
	id := uuid.New()
	f := &fakeDriver{confirmed: []uuid.UUID{id}}
	if err := NewStableSellWatcher(f, time.Minute).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.converted) == 0 || f.converted[0] != id {
		t.Fatalf("confirmed deposit was not converted: %v", f.converted)
	}
}

func TestStableSellWatcher_OneFailureDoesNotStopTheRest(t *testing.T) {
	// A stalled bridge on one intent must not block every other user's payout.
	a, b := uuid.New(), uuid.New()
	f := &fakeDriver{confirmed: []uuid.UUID{a, b}, convertErr: errors.New("provider down")}
	if err := NewStableSellWatcher(f, time.Minute).Run(context.Background()); err != nil {
		t.Fatalf("Run returned an error instead of continuing: %v", err)
	}
	if len(f.converted) != 2 {
		t.Fatalf("processed %d intents, want both", len(f.converted))
	}
}

func TestStableSellWatcher_FinalizesSettledConversions(t *testing.T) {
	// Without this bucket an intent whose conversion was merely ACCEPTED would
	// sit in `converting` forever and never pay out.
	id := uuid.New()
	f := &fakeDriver{converting: []uuid.UUID{id}}
	if err := NewStableSellWatcher(f, time.Minute).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.finalized) == 0 || f.finalized[0] != id {
		t.Fatalf("converting intent was not finalized: %v", f.finalized)
	}
}

func TestStableSellWatcher_PaysOutSettledSells(t *testing.T) {
	// The last hop, and the one that was missing: a settled conversion leaves
	// the user's fiat in the platform's own balance. Without this bucket every
	// brokered sell ends converted and unpaid.
	id := uuid.New()
	f := &fakeDriver{debited: []uuid.UUID{id}}
	if err := NewStableSellWatcher(f, time.Minute).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.paidOut) == 0 || f.paidOut[0] != id {
		t.Fatalf("debited intent was never paid out: %v", f.paidOut)
	}
}

func TestStableSellWatcher_HasAStableNameAndPositiveInterval(t *testing.T) {
	w := NewStableSellWatcher(&fakeDriver{}, 0)
	if w.Name() != "it-stable-sell-watcher" {
		t.Errorf("Name = %q", w.Name())
	}
	if w.Interval() <= 0 {
		t.Error("interval must be positive — the scheduler panics otherwise")
	}
}
