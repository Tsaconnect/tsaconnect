package services

import (
	"context"
	"strings"
	"testing"
	"time"
)

func workerFailureAlert() WorkerFailureAlert {
	return WorkerFailureAlert{
		Worker:              "inbound-credit-poller",
		ConsecutiveFailures: 11520,
		FailingSince:        time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC),
		FailingFor:          96 * time.Hour,
		LastError:           "flutterwave wallet-statement: HTTP 400: page size must not exceed 50",
	}
}

// The 2026-09-11 outage: every poll failed for six days and the only trace was
// a log line nobody reads. The email is the part that tells a human, so it has
// to carry everything needed to act without opening the logs.
func TestWorkerFailingAlertCarriesWhatAnOperatorActsOn(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test", "cto@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyWorkerFailing(context.Background(), workerFailureAlert()); err != nil {
		t.Fatalf("NotifyWorkerFailing: %v", err)
	}
	n.Flush()

	sent := mailer.records()
	if len(sent) != 2 {
		t.Fatalf("sent %d emails, want one per recipient", len(sent))
	}
	body := sent[0].subject + "\n" + sent[0].title + "\n" + sent[0].message
	for _, want := range []string{
		"inbound-credit-poller",
		"11520",
		"2026-09-11 08:00:00",
		"96h0m0s",
		"page size must not exceed 50",
		"hourly",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failing alert missing %q\n---\n%s", want, body)
		}
	}
	if !strings.Contains(sent[0].subject, "inbound-credit-poller") {
		t.Errorf("subject must name the worker so the inbox alone says what broke: %q", sent[0].subject)
	}
}

func TestWorkerRecoveredAlertNamesTheWorkerAndTheOutage(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "")

	if err := n.NotifyWorkerRecovered(context.Background(), workerFailureAlert()); err != nil {
		t.Fatalf("NotifyWorkerRecovered: %v", err)
	}
	n.Flush()

	sent := mailer.records()
	if len(sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(sent))
	}
	if !strings.Contains(strings.ToLower(sent[0].subject), "recovered") || !strings.Contains(sent[0].subject, "inbound-credit-poller") {
		t.Errorf("subject = %q", sent[0].subject)
	}
	for _, want := range []string{"11520", "96h0m0s"} {
		if !strings.Contains(sent[0].message, want) {
			t.Errorf("recovery missing %q\n---\n%s", want, sent[0].message)
		}
	}
	if sent[0].ctaURL != "" {
		t.Errorf("no console page exists for jobs; a CTA would link nowhere useful: %q", sent[0].ctaURL)
	}
}

// The scheduler calls this from the worker's own goroutine; a hung mail
// connection must not stop the worker ticking.
func TestWorkerFailingAlertDoesNotBlockTheWorker(t *testing.T) {
	release := make(chan struct{})
	n := NewInboundHoldNotifier(&fakeAlertMailer{block: release}, []string{"ops@tsa.test"}, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = n.NotifyWorkerFailing(context.Background(), workerFailureAlert())
		_ = n.NotifyWorkerRecovered(context.Background(), workerFailureAlert())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker alert blocked on the mailer")
	}
	close(release)
	n.Flush()
}

func TestWorkerFailureAlertDormantWhenUnconfigured(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, nil, "")
	_ = n.NotifyWorkerFailing(context.Background(), workerFailureAlert())
	_ = n.NotifyWorkerRecovered(context.Background(), workerFailureAlert())
	n.Flush()
	if got := len(mailer.records()); got != 0 {
		t.Errorf("sent %d emails with no recipients", got)
	}

	var nilNotifier *InboundHoldNotifier
	if err := nilNotifier.NotifyWorkerFailing(context.Background(), workerFailureAlert()); err != nil {
		t.Fatalf("nil notifier: %v", err)
	}
	if err := nilNotifier.NotifyWorkerRecovered(context.Background(), workerFailureAlert()); err != nil {
		t.Fatalf("nil notifier: %v", err)
	}
}
