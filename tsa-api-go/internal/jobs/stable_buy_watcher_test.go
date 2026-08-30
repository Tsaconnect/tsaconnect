package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeBuyDriver struct {
	received, awaiting, converting, delivering []uuid.UUID
	convertCalls, finalizeConvCalls            []uuid.UUID
	deliverCalls, finalizeDelCalls             []uuid.UUID
	convertErr                                 error
}

func (f *fakeBuyDriver) PendingStableBuys(context.Context) ([]uuid.UUID, []uuid.UUID, []uuid.UUID, []uuid.UUID, error) {
	return f.received, f.awaiting, f.converting, f.delivering, nil
}
func (f *fakeBuyDriver) ConvertStableBuy(_ context.Context, id uuid.UUID) error {
	f.convertCalls = append(f.convertCalls, id)
	return f.convertErr
}
func (f *fakeBuyDriver) FinalizeBuyConversion(_ context.Context, id uuid.UUID) error {
	f.finalizeConvCalls = append(f.finalizeConvCalls, id)
	return nil
}
func (f *fakeBuyDriver) DeliverStableBuy(_ context.Context, id uuid.UUID) error {
	f.deliverCalls = append(f.deliverCalls, id)
	return nil
}
func (f *fakeBuyDriver) FinalizeBuyDelivery(_ context.Context, id uuid.UUID) error {
	f.finalizeDelCalls = append(f.finalizeDelCalls, id)
	return nil
}

func TestStableBuyWatcher_AdvancesEveryBucket(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := &fakeBuyDriver{received: []uuid.UUID{a}, awaiting: []uuid.UUID{b}, converting: []uuid.UUID{c}, delivering: []uuid.UUID{d}}

	if err := NewStableBuyWatcher(f, 0).Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.convertCalls) != 2 { // received AND awaiting both feed conversion
		t.Fatalf("convert calls = %v, want a and b", f.convertCalls)
	}
	if len(f.finalizeConvCalls) != 1 || f.finalizeConvCalls[0] != c {
		t.Fatalf("finalize conversion calls = %v", f.finalizeConvCalls)
	}
	// A delivering intent is both sent and polled on the same tick; DeliverStableBuy
	// is a no-op once DeliveryID is set, so this is safe and saves a cycle.
	if len(f.deliverCalls) != 1 || len(f.finalizeDelCalls) != 1 {
		t.Fatalf("deliver=%v finalize=%v", f.deliverCalls, f.finalizeDelCalls)
	}
}

func TestStableBuyWatcher_OneFailureDoesNotStopTheTick(t *testing.T) {
	// One user's stalled provider call must not block every other user.
	a, b := uuid.New(), uuid.New()
	f := &fakeBuyDriver{received: []uuid.UUID{a, b}, convertErr: errors.New("boom")}

	if err := NewStableBuyWatcher(f, 0).Run(context.Background()); err != nil {
		t.Fatalf("watcher returned an error instead of swallowing it: %v", err)
	}
	if len(f.convertCalls) != 2 {
		t.Fatalf("stopped after the first failure: %v", f.convertCalls)
	}
}
