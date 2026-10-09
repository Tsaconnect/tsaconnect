package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRefundConfirmer struct {
	calls int
	err   error
}

func (f *fakeRefundConfirmer) ConfirmDue(context.Context) error {
	f.calls++
	return f.err
}

func TestPayRefundConfirmer_WorkerContract(t *testing.T) {
	w := NewPayRefundConfirmer(&fakeRefundConfirmer{}, 0)
	if w.Name() != "tsa-pay-refund-confirmer" || w.Interval() != 30*time.Second {
		t.Fatalf("name %q interval %s", w.Name(), w.Interval())
	}
	if got := NewPayRefundConfirmer(&fakeRefundConfirmer{}, time.Minute).Interval(); got != time.Minute {
		t.Fatalf("interval %s", got)
	}
}

func TestPayRefundConfirmer_RunCallsConfirmDueAndReturnsItsError(t *testing.T) {
	boom := errors.New("bsc rpc down")
	f := &fakeRefundConfirmer{err: boom}
	if err := NewPayRefundConfirmer(f, time.Minute).Run(context.Background()); !errors.Is(err, boom) || f.calls != 1 {
		t.Fatalf("err %v calls %d", err, f.calls)
	}
}
