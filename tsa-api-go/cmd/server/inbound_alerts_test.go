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
