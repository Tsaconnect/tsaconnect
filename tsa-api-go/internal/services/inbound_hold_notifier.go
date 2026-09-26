package services

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// InboundAlertMailer is the slice of *EmailService the hold alert needs.
// Declared here rather than taking the concrete type so tests can assert on
// what was sent without a Mailjet client.
type InboundAlertMailer interface {
	Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error
}

// alertQueueDepth is how many composed alerts may wait for the sender.
//
// Deep enough to absorb the batch this exists for — a provider format change
// that makes every sender name unparsable holds an entire poll's worth of
// credits at once — and bounded so a mail outage cannot grow the queue until
// the process dies. Past the bound an alert is dropped and logged, which is
// the honest outcome: the credit is already durably held, and a dropped alert
// is a line in the log rather than memory nobody can reclaim.
const alertQueueDepth = 256

// InboundHoldNotifier emails operators the moment a credit is held.
//
// A held credit is real customer money sitting unattributed, and by decision 9
// it has no automatic exit: nothing expires it, nothing refunds it, and the
// person who sent it is already waiting. The queue is otherwise only
// discoverable by an operator choosing to open it, so this email IS the
// discovery mechanism — which is why it fires per credit rather than as a
// digest. A batch turns "money is stuck right now" into a number in a nightly
// summary.
//
// The matcher owns the consumer-side interface and the wiring; build one with
//
//	services.NewInboundHoldNotifier(emailService, cfg.InboundAlertEmails, cfg.AdminConsoleBaseURL())
//
// and hand it over. It is safe to construct unconditionally — an unconfigured
// notifier is a no-op, never a nil to guard against.
//
// It also carries the reconciler's gap alert (NotifyGap) and the scheduler's
// money-path worker failure alerts (NotifyWorkerFailing/Recovered, in
// worker_failure_alert.go). One channel on purpose: all of them mean money is
// unaccounted for, all go to the same INBOUND_ALERT_EMAILS mailboxes, and a
// second copy of the recipient parsing and the send loop is a second thing to
// keep in step.
type InboundHoldNotifier struct {
	mailer InboundAlertMailer
	// to is the operator mailboxes from INBOUND_ALERT_EMAILS. Empty is a valid
	// production state: the queue fills, nobody is paged.
	to []string
	// consoleBaseURL is the admin origin the CTA deep-links into. Empty drops
	// the button rather than emitting a half-formed URL.
	consoleBaseURL string

	// queue carries composed alerts to the sender goroutine. It exists so the
	// network never touches the caller's goroutine — see NotifyHold.
	queue chan alertJob
	// start defers the sender goroutine to the first alert, so the notifiers
	// that are constructed and never used (unconfigured deployments, every
	// test that only checks the no-op paths) cost nothing.
	start sync.Once
	// pending counts alerts accepted but not yet attempted, so Flush can wait
	// for them.
	pending sync.WaitGroup
}

// alertJob is one composed message and everyone it goes to. Composition
// happens on the caller's goroutine — it is pure string building — and only
// the sending is deferred, so nothing about the credit is read after it has
// moved on.
type alertJob struct {
	kind                    string // for the log line when a send fails
	ref                     string // credit ref or gap window, likewise
	subject, title, message string
	ctaText, ctaURL         string
}

// NewInboundHoldNotifier builds the notifier. It never returns nil: an
// unconfigured notifier is a silent no-op, because the matcher must be able to
// hold credits whether or not anyone can be emailed about them.
func NewInboundHoldNotifier(mailer InboundAlertMailer, recipients []string, consoleBaseURL string) *InboundHoldNotifier {
	to := make([]string, 0, len(recipients))
	for _, addr := range recipients {
		if addr = strings.TrimSpace(addr); addr != "" {
			to = append(to, addr)
		}
	}
	return &InboundHoldNotifier{
		mailer:         mailer,
		to:             to,
		consoleBaseURL: strings.TrimRight(strings.TrimSpace(consoleBaseURL), "/"),
		queue:          make(chan alertJob, alertQueueDepth),
	}
}

// NotifyHold emails every configured operator about one held credit.
//
// It ALWAYS returns nil, deliberately. The matcher's interface returns an
// error, but there is no useful thing a caller could do with one: its only
// options would be to fail the hold — which loses the durable record that
// unattributed money is sitting there, the exact outcome the whole design
// exists to prevent — or to ignore it, which is what this does already,
// minus the temptation. Failures go to the log, loudly, where the on-call can
// see that alerting is broken; the credit is still in the queue either way.
//
// One bad recipient does not stop the rest, and a panic inside the mail client
// does not reach the 30-second poller that is the only thing noticing money
// arrive at all.
//
// It does not wait for the network. The matcher calls this from inside that
// poll tick, and a send is a serial round-trip per recipient against a
// third-party API: one provider change that makes every sender name unparsable
// holds a whole batch, and a hundred credits times three recipients at ~300ms
// is a minute and a half of a poller doing nothing but email. Worse, a hung
// connection never returns at all. The credit is already durably `held` before
// this is called and a dropped alert is explicitly tolerated, so the work that
// can block goes to a bounded queue and the tick moves on.
//
// ctx is deliberately unused. It belongs to the tick, and the tick ending is
// exactly the moment an alert about already-held money still has to go out.
func (n *InboundHoldNotifier) NotifyHold(_ context.Context, credit *models.InboundCredit) error {
	if n == nil || credit == nil || n.mailer == nil || len(n.to) == 0 {
		return nil
	}

	subject, title, message := n.compose(credit)
	ctaText, ctaURL := n.cta(credit)

	n.enqueue(alertJob{
		kind:    "held credit",
		ref:     fmt.Sprintf("%s (%s %s)", credit.ID, credit.Currency, koboAsNairaText(credit.GrossKobo)),
		subject: subject, title: title, message: message,
		ctaText: ctaText, ctaURL: ctaURL,
	})
	return nil
}

// Flush waits for every alert accepted so far to have been attempted.
//
// For shutdown and for tests. Nothing on the hot path calls it: the point of
// the queue is that the poll tick never waits.
func (n *InboundHoldNotifier) Flush() {
	if n == nil {
		return
	}
	n.pending.Wait()
}

// enqueue hands a composed alert to the sender, or drops it loudly.
//
// A full queue means the mailer has been failing or hanging for long enough to
// back up 256 alerts. Dropping is the deliberate choice — the alternative is
// blocking the caller, which is the thing this exists to prevent, or growing
// without bound — and the log line is what stops the drop being silent: every
// one of these is money sitting in the queue that nobody has been told about.
func (n *InboundHoldNotifier) enqueue(job alertJob) {
	n.start.Do(func() { go n.sender() })

	n.pending.Add(1)
	select {
	case n.queue <- job:
	default:
		n.pending.Done()
		log.Printf("[inbound-alert] DROPPED alert for %s %s — %d alerts already queued and unsent; mail delivery is broken and nobody has been paged",
			job.kind, job.ref, alertQueueDepth)
	}
}

// sender drains the queue one alert at a time.
//
// Serial on purpose: the alternative is a goroutine per credit, which turns a
// held batch into a burst against Mailjet's rate limit and gets the whole
// batch refused. Serial and behind a bounded queue, a slow mailer costs
// latency and then drops, both of which are visible in the log.
func (n *InboundHoldNotifier) sender() {
	for job := range n.queue {
		for _, addr := range n.to {
			n.sendOne(addr, job)
		}
		n.pending.Done()
	}
}

// sendOne isolates a single send so a failure — or a panic out of the mail
// client, which is third-party code on a network path — cannot escape past the
// recipient it belongs to. A panic here would otherwise take the sender
// goroutine with it and silence every later alert.
func (n *InboundHoldNotifier) sendOne(addr string, job alertJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[inbound-alert] PANIC sending to %s about %s %s: %v", addr, job.kind, job.ref, r)
		}
	}()
	if err := n.mailer.Send(addr, "TSA Operations", job.subject, job.title, job.message, job.ctaText, job.ctaURL); err != nil {
		log.Printf("[inbound-alert] FAILED to alert %s about %s %s: %v — the record is still in the admin queue",
			addr, job.kind, job.ref, err)
	}
}

// compose builds the alert.
//
// What it deliberately does NOT carry: the sender's name, the raw statement
// remark, and the sending bank. Those name a real third party who has not
// consented to being in anyone's inbox, and email is a channel with no access
// control, unknown forwarding and unbounded retention — quite unlike the
// console, which is behind adminAuth. Nor does it name any candidate user: the
// whole reason this credit held is that we do not know whose it is, and putting
// a guess in an email is how an operator ends up anchored on it.
//
// That is why the reason here is HoldReasonCode and never HoldReason. The
// prose is the console's copy and contains, in one string, all three of the
// things above: the payer's bank account name, the remark verbatim, and the
// Didit-verified legal name of every candidate user. Rendering it would ship
// platform users' legal identities and uninvolved third parties' account names
// to an uncontrolled mailbox, and would contradict this repo's own rule that
// verified names stay out of logs. The codes exist to be read cold and say the
// same thing about the decision without naming anybody.
//
// What is left is exactly what decides whether to act now: how much, in what
// currency, when it landed, why the matcher declined, and the provider
// reference that finds the same payment in Flutterwave's dashboard. The
// identity evidence lives one click away, where it is access-controlled and
// where acting on it is possible.
func (n *InboundHoldNotifier) compose(credit *models.InboundCredit) (subject, title, message string) {
	amount := credit.Currency + " " + koboAsNairaText(credit.GrossKobo)

	subject = fmt.Sprintf("Unattributed payment held — %s", amount)
	title = "Inbound payment needs an operator"

	reason := strings.TrimSpace(credit.HoldReasonCode)
	if reason == "" {
		// The matcher always sets one alongside the prose; a blank code means
		// a hold reached the queue by some other path. Saying so beats both a
		// blank line and reaching for the prose, which is the one string that
		// must never be here.
		reason = "not recorded"
	}

	var b strings.Builder
	b.WriteString("A payment into the central account could not be attributed automatically and is being held.\n\n")
	fmt.Fprintf(&b, "Amount: %s\n", amount)
	fmt.Fprintf(&b, "Received: %s UTC\n", credit.TransactionDate.UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Reason held: %s\n", reason)
	fmt.Fprintf(&b, "Provider reference: %s\n\n", credit.ProviderRef)
	b.WriteString("Nothing releases this on its own — it stays held until an operator assigns, refunds or ignores it. " +
		"Sender details and the near-miss candidates are in the admin console.")

	return subject, title, b.String()
}

// cta deep-links to the one queue entry rather than the queue, so an operator
// reading on a phone lands on the credit the email is about instead of hunting
// for it in a list. No configured origin means no button — a link to nowhere is
// worse than none.
func (n *InboundHoldNotifier) cta(credit *models.InboundCredit) (text, url string) {
	if n.consoleBaseURL == "" {
		return "", ""
	}
	return "Review this payment", n.consoleBaseURL + "/inbound/credits/" + credit.ID.String()
}

// InboundGapAlert is the inbound reconciler's finding, restated here.
//
// It duplicates jobs.InboundGap field for field, and it has to: internal/jobs
// imports internal/services, so naming that type here would be an import
// cycle. main.go copies one into the other where both are already in scope.
// The alternative — moving the notifier into internal/jobs — would put the
// mail composition somewhere the matcher, which is the primary caller, cannot
// reach.
//
// Unlike a hold, nothing in a gap names a person: it is two sums, two row
// counts and a window. It is safe for email in a way HoldReason never is.
type InboundGapAlert struct {
	Currency string
	From, To time.Time
	// IngestedKobo is what we recorded, ProviderKobo what the provider says it
	// credited, GapKobo the first minus the second.
	IngestedKobo int64
	ProviderKobo int64
	GapKobo      int64
	IngestedRows int
	ProviderRows int
	// Streak is how many consecutive ticks have seen this gap.
	Streak int
}

// NotifyGap emails operators about a confirmed inbound reconciliation gap.
//
// A negative gap is the urgent one and has no other discovery path at all: a
// customer paid, the credit never reached inbound_credits, and so there is no
// held row for anyone to find in the admin queue. Until this existed the only
// alert was a log line, which is a channel nobody is subscribed to at 3am.
//
// Same contract as NotifyHold: never returns an error (the reconciler must not
// start reporting a mail failure as a reconcile failure), never blocks on the
// network, and is a quiet no-op when unconfigured.
func (n *InboundHoldNotifier) NotifyGap(_ context.Context, gap InboundGapAlert) error {
	if n == nil || n.mailer == nil || len(n.to) == 0 {
		return nil
	}

	subject, title, message := n.composeGap(gap)
	ctaText, ctaURL := "", ""
	if n.consoleBaseURL != "" {
		ctaText, ctaURL = "Open the inbound queue", n.consoleBaseURL+"/inbound/credits"
	}

	n.enqueue(alertJob{
		kind:    "reconciliation gap",
		ref:     gap.Currency + " " + gap.From.UTC().Format(time.RFC3339),
		subject: subject, title: title, message: message,
		ctaText: ctaText, ctaURL: ctaURL,
	})
	return nil
}

// composeGap names the direction first and in capitals.
//
// The two directions mean opposite things and need opposite responses — short
// is a customer with no money and no queue entry, long is a user credited
// twice — and a signed number in a wall of figures is exactly what gets read
// the wrong way round in a hurry.
func (n *InboundHoldNotifier) composeGap(gap InboundGapAlert) (subject, title, message string) {
	direction, meaning := "MISSING", "a payment reached Flutterwave and never reached us — somebody has paid and nobody has their money. There is no held credit to find: the row does not exist on our side."
	if gap.GapKobo > 0 {
		direction, meaning = "DOUBLE-COUNTED", "we recorded more than Flutterwave credited — one payment has most likely been ingested twice, which credits a user twice."
	}
	size := gap.Currency + " " + koboAsNairaText(abs64(gap.GapKobo))

	subject = fmt.Sprintf("Inbound reconciliation gap — %s %s", size, direction)
	title = "Inbound ledger disagrees with the provider"

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n\n", direction, meaning)
	fmt.Fprintf(&b, "Gap: %s\n", size)
	fmt.Fprintf(&b, "Recorded by us: %s %s over %d rows\n",
		gap.Currency, koboAsNairaText(gap.IngestedKobo), gap.IngestedRows)
	fmt.Fprintf(&b, "Credited by provider: %s %s over %d rows\n",
		gap.Currency, koboAsNairaText(gap.ProviderKobo), gap.ProviderRows)
	fmt.Fprintf(&b, "Window: %s to %s UTC\n",
		gap.From.UTC().Format("2006-01-02 15:04:05"), gap.To.UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Consecutive ticks seeing this: %d\n\n", gap.Streak)
	b.WriteString("Equal row counts point at one wrong amount; unequal counts at a whole row missing or duplicated. " +
		"The window already excludes credits too recent to have been polled, so this is not an in-flight payment.")

	return subject, title, b.String()
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// koboAsNairaText renders minor units as grouped major units, e.g.
// 200000 → "2,000.00". Integer arithmetic throughout: these are money figures
// people compare against a bank statement or their own prices, and float
// formatting is how a kobo goes missing.
func koboAsNairaText(kobo int64) string {
	sign := ""
	if kobo < 0 {
		sign = "-"
		kobo = -kobo
	}
	major := kobo / 100
	minor := kobo % 100

	digits := strconv.FormatInt(major, 10)
	var grouped strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(r)
	}
	return fmt.Sprintf("%s%s.%02d", sign, grouped.String(), minor)
}
