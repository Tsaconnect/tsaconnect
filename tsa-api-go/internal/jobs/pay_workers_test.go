package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakePayWebhookSender struct {
	calls      int
	n          int
	err        error
	purgeCalls int
	purgeErr   error
	purgeRet   time.Duration
	purgeLimit int
}

func (f *fakePayWebhookSender) PurgeOld(_ context.Context, retention time.Duration, limit int) (int64, error) {
	f.purgeCalls++
	f.purgeRet, f.purgeLimit = retention, limit
	return 0, f.purgeErr
}

func (f *fakePayWebhookSender) DeliverDue(_ context.Context, _ int) (int, error) {
	f.calls++
	return f.n, f.err
}

func TestPayWebhookWorker_WorkerContract(t *testing.T) {
	w := NewPayWebhookWorker(&fakePayWebhookSender{}, 0)
	if w.Name() != "tsa-pay-webhooks" {
		t.Errorf("Name = %q", w.Name())
	}
	if w.Interval() != 15*time.Second {
		t.Errorf("Interval = %s, want the 15s default", w.Interval())
	}
	if got := NewPayWebhookWorker(&fakePayWebhookSender{}, time.Minute).Interval(); got != time.Minute {
		t.Errorf("Interval = %s, want the configured minute", got)
	}
}

func TestPayWebhookWorker_RunCallsThroughOnceAndReturnsTheError(t *testing.T) {
	wantErr := errors.New("delivery boom")
	sender := &fakePayWebhookSender{err: wantErr}
	w := NewPayWebhookWorker(sender, time.Minute)

	if err := w.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Run err = %v, want %v", err, wantErr)
	}
	if sender.calls != 1 {
		t.Fatalf("calls = %d, want 1", sender.calls)
	}
}

type fakePayReconciler struct {
	calls int
	err   error
}

func (f *fakePayReconciler) ReconcileTick(_ context.Context) error {
	f.calls++
	return f.err
}

func TestPayReconciler_WorkerContract(t *testing.T) {
	w := NewPayReconciler(&fakePayReconciler{}, 0)
	if w.Name() != "tsa-pay-reconciler" {
		t.Errorf("Name = %q", w.Name())
	}
	if w.Interval() != 30*time.Second {
		t.Errorf("Interval = %s, want the 30s default", w.Interval())
	}
	if got := NewPayReconciler(&fakePayReconciler{}, time.Minute).Interval(); got != time.Minute {
		t.Errorf("Interval = %s, want the configured minute", got)
	}
}

func TestPayReconciler_RunCallsThroughOnceAndReturnsTheError(t *testing.T) {
	wantErr := errors.New("reconcile boom")
	svc := &fakePayReconciler{err: wantErr}
	w := NewPayReconciler(svc, time.Minute)

	if err := w.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Run err = %v, want %v", err, wantErr)
	}
	if svc.calls != 1 {
		t.Fatalf("calls = %d, want 1", svc.calls)
	}
}

func TestPayWebhookWorker_RunDeliversThenPurgesWithTheServiceRetention(t *testing.T) {
	sender := &fakePayWebhookSender{}
	if err := NewPayWebhookWorker(sender, time.Minute).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 || sender.purgeCalls != 1 {
		t.Fatalf("deliver calls %d, purge calls %d; want 1 and 1", sender.calls, sender.purgeCalls)
	}
	if sender.purgeRet != services.PayWebhookRetention || sender.purgeLimit != services.PayWebhookPurgeBatch {
		t.Fatalf("purge called with %s / %d", sender.purgeRet, sender.purgeLimit)
	}
}

func TestPayWebhookWorker_RunStillPurgesWhenDeliveryFailsAndReportsBothErrors(t *testing.T) {
	deliverErr, purgeErr := errors.New("deliver boom"), errors.New("purge boom")
	sender := &fakePayWebhookSender{err: deliverErr, purgeErr: purgeErr}
	err := NewPayWebhookWorker(sender, time.Minute).Run(context.Background())
	if !errors.Is(err, deliverErr) || !errors.Is(err, purgeErr) || sender.purgeCalls != 1 {
		t.Fatalf("err %v, purge calls %d", err, sender.purgeCalls)
	}
}

func TestPayWebhookWorker_TheBatchIsTheServicesDefault(t *testing.T) {
	if w := NewPayWebhookWorker(&fakePayWebhookSender{}, time.Minute); w.batch != services.PayWebhookDefaultBatch {
		t.Fatalf("batch = %d, want %d", w.batch, services.PayWebhookDefaultBatch)
	}
}
