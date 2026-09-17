package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type recordingMailer struct {
	subjects []string
	bodies   []string
}

func (m *recordingMailer) Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error {
	m.subjects = append(m.subjects, subject)
	m.bodies = append(m.bodies, message)
	return nil
}

// The reconciler's notifier interface went unimplemented for its whole life:
// SetNotifier was never called and no type in the repo satisfied it, so a gap
// — including the direction that means a customer paid and we have no record
// of it, with no held credit anywhere for an operator to find — alerted
// nowhere but the log.
func TestInboundGapAlertsSatisfiesTheReconcilerNotifier(t *testing.T) {
	var _ jobs.InboundGapNotifier = inboundGapAlerts{}

	mailer := &recordingMailer{}
	notifier := services.NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")

	gap := jobs.InboundGap{
		Currency:     "NGN",
		From:         time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC),
		To:           time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC),
		IngestedKobo: 800000,
		ProviderKobo: 1000000,
		GapKobo:      -200000,
		IngestedRows: 4,
		ProviderRows: 5,
		Streak:       2,
	}
	if err := (inboundGapAlerts{to: notifier}).NotifyInboundGap(context.Background(), gap); err != nil {
		t.Fatalf("NotifyInboundGap: %v", err)
	}
	notifier.Flush()

	if len(mailer.subjects) != 1 {
		t.Fatalf("sent %d emails, want 1", len(mailer.subjects))
	}
	// Every field has to survive the copy; a zeroed one turns the alert into a
	// figure nobody can act on.
	for _, want := range []string{"2,000.00", "8,000.00", "10,000.00", "NGN", "2026-09-10", "MISSING"} {
		if !strings.Contains(mailer.subjects[0]+mailer.bodies[0], want) {
			t.Errorf("gap alert lost %q in the copy\n---\n%s", want, mailer.bodies[0])
		}
	}
}

// The scheduler's failure-streak alert reaches the same inbox as a held credit.
// Every field must survive the jobs → services copy: an alert without the
// worker, the count, the duration or the error is one nobody can act on.
func TestWorkerFailureAlertsSatisfiesTheSchedulerNotifier(t *testing.T) {
	var _ jobs.WorkerFailureNotifier = workerFailureAlerts{}

	mailer := &recordingMailer{}
	notifier := services.NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "")
	f := jobs.WorkerFailure{
		Worker:              "inbound-reconciler",
		ConsecutiveFailures: 1728,
		FailingSince:        time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC),
		FailingFor:          144 * time.Hour,
		LastError:           "flutterwave wallet-statement: HTTP 400: page size must not exceed 50",
	}
	a := workerFailureAlerts{to: notifier}
	if err := a.NotifyWorkerFailing(context.Background(), f); err != nil {
		t.Fatalf("NotifyWorkerFailing: %v", err)
	}
	if err := a.NotifyWorkerRecovered(context.Background(), f); err != nil {
		t.Fatalf("NotifyWorkerRecovered: %v", err)
	}
	notifier.Flush()

	if len(mailer.subjects) != 2 {
		t.Fatalf("sent %d emails, want failing + recovered", len(mailer.subjects))
	}
	failing := mailer.subjects[0] + mailer.bodies[0]
	for _, want := range []string{"inbound-reconciler", "1728", "2026-09-11 08:00:00", "144h0m0s", "page size must not exceed 50"} {
		if !strings.Contains(failing, want) {
			t.Errorf("failing alert lost %q in the copy\n---\n%s", want, failing)
		}
	}
	if !strings.Contains(mailer.subjects[1], "recovered") {
		t.Errorf("second email should be the recovery: %q", mailer.subjects[1])
	}
}
