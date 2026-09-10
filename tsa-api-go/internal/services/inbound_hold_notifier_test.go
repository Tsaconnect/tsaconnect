package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type sentAlert struct {
	toEmail, toName, subject, title, message, ctaText, ctaURL string
}

// fakeAlertMailer records sends and can fail or panic on demand.
type fakeAlertMailer struct {
	sent    []sentAlert
	err     error
	failFor string // fail only this recipient
	panics  bool
}

func (f *fakeAlertMailer) Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error {
	if f.panics {
		panic("mail transport exploded")
	}
	f.sent = append(f.sent, sentAlert{toEmail, toName, subject, title, message, ctaText, ctaURL})
	if f.failFor != "" {
		if f.failFor == toEmail {
			return errors.New("smtp refused")
		}
		return nil
	}
	return f.err
}

func heldTestCredit() *models.InboundCredit {
	id := uuid.MustParse("2b1f5a4e-0000-4000-8000-00000000abcd")
	return &models.InboundCredit{
		ID:              id,
		Provider:        models.InboundCreditProviderFlutterwave,
		ProviderRef:     "100004250910120000012345678901",
		Currency:        "NGN",
		AmountKobo:      197200,
		FeeKobo:         2800,
		GrossKobo:       200000,
		SenderRaw:       "Received Money from GTBANK|ADEMU EMMANUEL O.",
		SenderBank:      "GTBANK",
		SenderName:      "ADEMU EMMANUEL O.",
		TransactionDate: time.Date(2026, 9, 10, 14, 32, 0, 0, time.UTC),
		Status:          models.InboundCreditHeld,
		HoldReason:      "two open claims matched on amount and name",
	}
}

func TestInboundHoldNotifierMailsEveryRecipientPerCredit(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test", "finance@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	if len(mailer.sent) != 2 {
		t.Fatalf("sent %d emails, want one per recipient", len(mailer.sent))
	}
	if mailer.sent[0].toEmail != "ops@tsa.test" || mailer.sent[1].toEmail != "finance@tsa.test" {
		t.Errorf("recipients = %q, %q", mailer.sent[0].toEmail, mailer.sent[1].toEmail)
	}
}

// What an operator needs to triage: how much, when, why it held, and the
// provider reference that ties it to the Flutterwave dashboard.
func TestInboundHoldNotifierBodyCarriesWhatAnOperatorActsOn(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	credit := heldTestCredit()

	if err := n.NotifyHold(context.Background(), credit); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	got := mailer.sent[0]
	body := got.subject + "\n" + got.title + "\n" + got.message

	for _, want := range []string{
		"2,000.00",                // gross, in naira
		"NGN",                     // currency
		credit.ProviderRef,        // the handle into the provider dashboard
		"two open claims matched", // the hold reason, verbatim
		"2026-09-10",              // when the money landed
	} {
		if !strings.Contains(body, want) {
			t.Errorf("email body missing %q\n---\n%s", want, body)
		}
	}
}

// The sender's name is a real third party's identity. It belongs in the console
// behind adminAuth, not in a mailbox with unknown forwarding and retention —
// and an operator does not need it to decide to go and look.
func TestInboundHoldNotifierOmitsSenderIdentity(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	credit := heldTestCredit()

	if err := n.NotifyHold(context.Background(), credit); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	got := mailer.sent[0]
	whole := strings.Join([]string{got.subject, got.title, got.message, got.ctaText, got.ctaURL}, "\n")

	for _, leaked := range []string{credit.SenderName, credit.SenderRaw, credit.SenderBank, "ADEMU", "EMMANUEL"} {
		if strings.Contains(strings.ToUpper(whole), strings.ToUpper(leaked)) {
			t.Errorf("email leaked sender identity %q\n---\n%s", leaked, whole)
		}
	}
}

func TestInboundHoldNotifierDeepLinksToTheQueueEntry(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test/")
	credit := heldTestCredit()

	if err := n.NotifyHold(context.Background(), credit); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	want := "https://admin.tsa.test/inbound/credits/" + credit.ID.String()
	if mailer.sent[0].ctaURL != want {
		t.Errorf("ctaURL = %q, want %q", mailer.sent[0].ctaURL, want)
	}
	if mailer.sent[0].ctaText == "" {
		t.Error("ctaText must not be empty — the link is the whole point of the email")
	}
}

// Losing an email is recoverable; losing the hold is not. A transport failure
// must not reach the matcher as an error it could roll the hold back on, and
// one bad recipient must not stop the others being told.
func TestInboundHoldNotifierSwallowsSendFailures(t *testing.T) {
	mailer := &fakeAlertMailer{failFor: "ops@tsa.test"}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test", "finance@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold returned %v, want nil — a failed send must never look like a failed hold", err)
	}
	if len(mailer.sent) != 2 {
		t.Errorf("sent %d emails, want both attempted despite the first failing", len(mailer.sent))
	}
}

// The mailer is a third-party client on a network path. A panic there must not
// take down the 30-second poller that is the only thing noticing money arrive.
func TestInboundHoldNotifierSurvivesAPanickingMailer(t *testing.T) {
	n := NewInboundHoldNotifier(&fakeAlertMailer{panics: true}, []string{"ops@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold returned %v, want nil", err)
	}
}

// Unconfigured is a legitimate production state: the queue still fills, nobody
// is paged. It must be a quiet no-op, never a nil dereference in the poller.
func TestInboundHoldNotifierDormantWhenUnconfigured(t *testing.T) {
	mailer := &fakeAlertMailer{}
	noRecipients := NewInboundHoldNotifier(mailer, nil, "https://admin.tsa.test")
	if err := noRecipients.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("no recipients: %v", err)
	}
	if len(mailer.sent) != 0 {
		t.Errorf("sent %d emails with no recipients configured", len(mailer.sent))
	}

	noMailer := NewInboundHoldNotifier(nil, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	if err := noMailer.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("no mailer: %v", err)
	}

	var nilNotifier *InboundHoldNotifier
	if err := nilNotifier.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("nil notifier: %v", err)
	}
	if err := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "").NotifyHold(context.Background(), nil); err != nil {
		t.Fatalf("nil credit: %v", err)
	}
	if len(mailer.sent) != 0 {
		t.Errorf("sent %d emails for a nil credit", len(mailer.sent))
	}
}

// Without a console origin the email still has to go out — the amount, the
// reason and the reference are what makes it actionable, the link is a
// convenience — but it must not ship a dangling half-URL.
func TestInboundHoldNotifierSendsWithoutAConsoleURL(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "")

	if err := n.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(mailer.sent))
	}
	if mailer.sent[0].ctaURL != "" || mailer.sent[0].ctaText != "" {
		t.Errorf("cta = %q/%q, want both empty with no console origin configured",
			mailer.sent[0].ctaText, mailer.sent[0].ctaURL)
	}
}

func TestInboundAlertNairaText(t *testing.T) {
	tests := map[int64]string{
		0:            "0.00",
		1:            "0.01",
		99:           "0.99",
		100:          "1.00",
		200000:       "2,000.00",
		123456789:    "1,234,567.89",
		100000000000: "1,000,000,000.00",
		-250050:      "-2,500.50",
	}
	for kobo, want := range tests {
		if got := inboundAlertNairaText(kobo); got != want {
			t.Errorf("inboundAlertNairaText(%d) = %q, want %q", kobo, got, want)
		}
	}
}
