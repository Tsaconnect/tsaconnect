package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePayWebhookSender struct {
	calls int
	n     int
	err   error
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
