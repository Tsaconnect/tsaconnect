package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type sentAlert struct {
	toEmail, toName, subject, title, message, ctaText, ctaURL string
}

// fakeAlertMailer records sends and can fail, panic or block on demand.
// Sends now happen on the notifier's own goroutine, so the recording is
// mutex-guarded: without it `go test -race` fails on the notifier's own tests.
type fakeAlertMailer struct {
	mu      sync.Mutex
	sent    []sentAlert
	err     error
	failFor string // fail only this recipient
	panics  bool
	block   chan struct{} // when set, Send waits on it before recording
}

func (f *fakeAlertMailer) Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error {
	if f.panics {
		panic("mail transport exploded")
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.sent = append(f.sent, sentAlert{toEmail, toName, subject, title, message, ctaText, ctaURL})
	f.mu.Unlock()
	if f.failFor != "" {
		if f.failFor == toEmail {
			return errors.New("smtp refused")
		}
		return nil
	}
	return f.err
}

func (f *fakeAlertMailer) records() []sentAlert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentAlert(nil), f.sent...)
}

// The candidate names are the ones the matcher compared against the sender —
// real users whose legal names came from Didit. The prose in HoldReason names
// all of them plus the sender and the verbatim remark, which is why the alert
// must carry the code instead.
const (
	testCandidateNameA = "ADEMU EMMANUEL OKON"
	testCandidateNameB = "EMMANUELLA ADEMU JOSEPH"
)

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
		HoldReason: "two open claims matched NGN 2,000.00 and sender 'ADEMU EMMANUEL O.' " +
			"(Received Money from GTBANK|ADEMU EMMANUEL O.): " +
			testCandidateNameA + " and " + testCandidateNameB,
		HoldReasonCode: models.InboundHoldContested,
	}
}

func TestInboundHoldNotifierMailsEveryRecipientPerCredit(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test", "finance@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	n.Flush()

	sent := mailer.records()
	if len(sent) != 2 {
		t.Fatalf("sent %d emails, want one per recipient", len(sent))
	}
	if sent[0].toEmail != "ops@tsa.test" || sent[1].toEmail != "finance@tsa.test" {
		t.Errorf("recipients = %q, %q", sent[0].toEmail, sent[1].toEmail)
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
	n.Flush()

	got := mailer.records()[0]
	body := got.subject + "\n" + got.title + "\n" + got.message

	for _, want := range []string{
		"2,000.00",                  // gross, in naira
		"NGN",                       // currency
		credit.ProviderRef,          // the handle into the provider dashboard
		models.InboundHoldContested, // why it held, said without naming anybody
		"2026-09-10",                // when the money landed
	} {
		if !strings.Contains(body, want) {
			t.Errorf("email body missing %q\n---\n%s", want, body)
		}
	}
}

// The prose in HoldReason is the console's copy: it names the payer, quotes the
// statement remark verbatim and lists the Didit-verified legal name of every
// candidate user. Email has no access control, unknown forwarding and
// unbounded retention, and this repo's policy is that verified names stay out
// of logs — so not one word of that prose may reach the alert, whatever the
// matcher writes into it.
func TestInboundHoldNotifierNeverShipsTheHoldProse(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	credit := heldTestCredit()

	if err := n.NotifyHold(context.Background(), credit); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	n.Flush()

	got := mailer.records()[0]
	whole := strings.ToUpper(strings.Join(
		[]string{got.toName, got.subject, got.title, got.message, got.ctaText, got.ctaURL}, "\n"))

	leaks := []string{
		credit.HoldReason,  // the whole prose
		testCandidateNameA, // a candidate user's verified legal name
		testCandidateNameB, // and the rival's
		credit.SenderName,  // the payer, who never used the platform
		credit.SenderRaw,   // the raw statement remark
		credit.SenderBank,  // and where they bank
		"OKON", "JOSEPH", "GTBANK",
	}
	for _, leaked := range leaks {
		if strings.Contains(whole, strings.ToUpper(leaked)) {
			t.Errorf("alert leaked %q\n---\n%s", leaked, whole)
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
	n.Flush()

	got := mailer.records()[0]
	whole := strings.Join([]string{got.subject, got.title, got.message, got.ctaText, got.ctaURL}, "\n")

	for _, leaked := range []string{credit.SenderName, credit.SenderRaw, credit.SenderBank, "ADEMU", "EMMANUEL"} {
		if strings.Contains(strings.ToUpper(whole), strings.ToUpper(leaked)) {
			t.Errorf("email leaked sender identity %q\n---\n%s", leaked, whole)
		}
	}
}

// A hold written before hold_reason_code existed, or by a path that forgot to
// set it, must still produce a usable alert — and must still not reach for the
// prose to fill the gap.
func TestInboundHoldNotifierHandlesAMissingReasonCode(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	credit := heldTestCredit()
	credit.HoldReasonCode = ""

	if err := n.NotifyHold(context.Background(), credit); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	n.Flush()

	got := mailer.records()[0]
	if !strings.Contains(got.message, "not recorded") {
		t.Errorf("blank reason code should say so explicitly\n---\n%s", got.message)
	}
	if strings.Contains(strings.ToUpper(got.message), "ADEMU") {
		t.Errorf("fell back to the prose when the code was missing\n---\n%s", got.message)
	}
}

func TestInboundHoldNotifierDeepLinksToTheQueueEntry(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test/")
	credit := heldTestCredit()

	if err := n.NotifyHold(context.Background(), credit); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	n.Flush()

	sent := mailer.records()
	want := "https://admin.tsa.test/inbound/credits/" + credit.ID.String()
	if sent[0].ctaURL != want {
		t.Errorf("ctaURL = %q, want %q", sent[0].ctaURL, want)
	}
	if sent[0].ctaText == "" {
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
	n.Flush()

	if got := len(mailer.records()); got != 2 {
		t.Errorf("sent %d emails, want both attempted despite the first failing", got)
	}
}

// The mailer is a third-party client on a network path. A panic there must not
// take down the 30-second poller that is the only thing noticing money arrive.
func TestInboundHoldNotifierSurvivesAPanickingMailer(t *testing.T) {
	n := NewInboundHoldNotifier(&fakeAlertMailer{panics: true}, []string{"ops@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold returned %v, want nil", err)
	}
	n.Flush() // must return: a panicking send must not kill the sender goroutine
}

// The matcher calls this from inside the 30-second poll tick, the only thing
// that notices money arriving at all. A mail round-trip per recipient per
// credit on that path is how a provider change that holds a whole batch turns
// into minutes of dead poller — and Mailjet on a hung connection never returns
// at all. So the call must not wait for the network.
func TestInboundHoldNotifierDoesNotBlockTheTick(t *testing.T) {
	release := make(chan struct{})
	mailer := &fakeAlertMailer{block: release}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test", "finance@tsa.test"}, "https://admin.tsa.test")

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		for i := 0; i < 20; i++ {
			_ = n.NotifyHold(context.Background(), heldTestCredit())
		}
	}()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyHold blocked on the mailer — the poll tick is stalled behind email")
	}

	close(release)
	n.Flush()
	if len(mailer.records()) == 0 {
		t.Error("nothing was ever sent — the alert must be deferred, not discarded")
	}
}

// A cancelled tick is exactly when the alert matters most: the credit is
// already durably held and nobody has been told. Shutdown must not swallow it.
func TestInboundHoldNotifierSendsEvenWhenTheTickContextIsDead(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := n.NotifyHold(ctx, heldTestCredit()); err != nil {
		t.Fatalf("NotifyHold: %v", err)
	}
	n.Flush()

	if len(mailer.records()) != 1 {
		t.Error("a cancelled tick context dropped the alert for an already-held credit")
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
	noRecipients.Flush()
	if got := len(mailer.records()); got != 0 {
		t.Errorf("sent %d emails with no recipients configured", got)
	}

	noMailer := NewInboundHoldNotifier(nil, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	if err := noMailer.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("no mailer: %v", err)
	}

	var nilNotifier *InboundHoldNotifier
	if err := nilNotifier.NotifyHold(context.Background(), heldTestCredit()); err != nil {
		t.Fatalf("nil notifier: %v", err)
	}
	nilNotifier.Flush()

	blank := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "")
	if err := blank.NotifyHold(context.Background(), nil); err != nil {
		t.Fatalf("nil credit: %v", err)
	}
	blank.Flush()
	if got := len(mailer.records()); got != 0 {
		t.Errorf("sent %d emails for a nil credit", got)
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
	n.Flush()

	sent := mailer.records()
	if len(sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(sent))
	}
	if sent[0].ctaURL != "" || sent[0].ctaText != "" {
		t.Errorf("cta = %q/%q, want both empty with no console origin configured",
			sent[0].ctaText, sent[0].ctaURL)
	}
}

func gapAlert() InboundGapAlert {
	return InboundGapAlert{
		Currency:     "NGN",
		From:         time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC),
		To:           time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC),
		IngestedKobo: 800000,
		ProviderKobo: 1000000,
		GapKobo:      -200000,
		IngestedRows: 4,
		ProviderRows: 5,
		Streak:       3,
	}
}

// A negative gap means a customer paid and we have no record of it — nobody
// has their money and no held credit exists to be found in the queue. Until
// now the only alert was a log line.
func TestInboundGapAlertNamesTheDirectionAndTheWindow(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")

	if err := n.NotifyGap(context.Background(), gapAlert()); err != nil {
		t.Fatalf("NotifyGap: %v", err)
	}
	n.Flush()

	sent := mailer.records()
	if len(sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(sent))
	}
	body := sent[0].subject + "\n" + sent[0].title + "\n" + sent[0].message
	for _, want := range []string{
		"MISSING",    // the direction, because the two mean opposite things
		"2,000.00",   // the size of the gap
		"NGN",        //
		"8,000.00",   // what we recorded
		"10,000.00",  // what the provider says it credited
		"2026-09-10", // the window
	} {
		if !strings.Contains(body, want) {
			t.Errorf("gap alert missing %q\n---\n%s", want, body)
		}
	}
}

func TestInboundGapAlertNamesADuplicate(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")

	gap := gapAlert()
	gap.IngestedKobo, gap.ProviderKobo, gap.GapKobo = 1200000, 1000000, 200000
	gap.IngestedRows, gap.ProviderRows = 6, 5

	if err := n.NotifyGap(context.Background(), gap); err != nil {
		t.Fatalf("NotifyGap: %v", err)
	}
	n.Flush()

	if !strings.Contains(mailer.records()[0].message, "DOUBLE-COUNTED") {
		t.Errorf("a positive gap must not read like a missing credit\n---\n%s", mailer.records()[0].message)
	}
}

// The reconciler runs on the scheduler, not the poll tick, but it is the same
// mailer and the same failure mode — and a reconciler stalled on email stops
// checking for missing money.
func TestInboundGapAlertIsAlsoOffTheCriticalPath(t *testing.T) {
	release := make(chan struct{})
	n := NewInboundHoldNotifier(&fakeAlertMailer{block: release}, []string{"ops@tsa.test"}, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = n.NotifyGap(context.Background(), gapAlert())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyGap blocked on the mailer")
	}
	close(release)
	n.Flush()
}

func TestInboundGapAlertDormantWhenUnconfigured(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, nil, "")
	if err := n.NotifyGap(context.Background(), gapAlert()); err != nil {
		t.Fatalf("NotifyGap: %v", err)
	}
	n.Flush()
	if got := len(mailer.records()); got != 0 {
		t.Errorf("sent %d gap emails with no recipients configured", got)
	}

	var nilNotifier *InboundHoldNotifier
	if err := nilNotifier.NotifyGap(context.Background(), gapAlert()); err != nil {
		t.Fatalf("nil notifier: %v", err)
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
