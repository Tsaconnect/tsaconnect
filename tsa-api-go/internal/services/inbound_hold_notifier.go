package services

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// InboundAlertMailer is the slice of *EmailService the hold alert needs.
// Declared here rather than taking the concrete type so tests can assert on
// what was sent without a Mailjet client.
type InboundAlertMailer interface {
	Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error
}

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
type InboundHoldNotifier struct {
	mailer InboundAlertMailer
	// to is the operator mailboxes from INBOUND_ALERT_EMAILS. Empty is a valid
	// production state: the queue fills, nobody is paged.
	to []string
	// consoleBaseURL is the admin origin the CTA deep-links into. Empty drops
	// the button rather than emitting a half-formed URL.
	consoleBaseURL string
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
func (n *InboundHoldNotifier) NotifyHold(ctx context.Context, credit *models.InboundCredit) error {
	if n == nil || credit == nil || n.mailer == nil || len(n.to) == 0 {
		return nil
	}

	subject, title, message := n.compose(credit)
	ctaText, ctaURL := n.cta(credit)

	for _, addr := range n.to {
		if err := ctx.Err(); err != nil {
			log.Printf("[inbound-hold-alert] credit=%s aborted before %s: %v", credit.ID, addr, err)
			return nil
		}
		n.sendOne(addr, subject, title, message, ctaText, ctaURL, credit)
	}
	return nil
}

// sendOne isolates a single send so a failure — or a panic out of the mail
// client, which is third-party code on a network path — cannot escape past the
// recipient it belongs to.
func (n *InboundHoldNotifier) sendOne(addr, subject, title, message, ctaText, ctaURL string, credit *models.InboundCredit) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[inbound-hold-alert] PANIC sending to %s for credit=%s: %v", addr, credit.ID, r)
		}
	}()
	if err := n.mailer.Send(addr, "TSA Operations", subject, title, message, ctaText, ctaURL); err != nil {
		log.Printf("[inbound-hold-alert] FAILED to alert %s about held credit=%s (%s %s): %v — the credit is still held and still in the admin queue",
			addr, credit.ID, credit.Currency, inboundAlertNairaText(credit.GrossKobo), err)
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
// What is left is exactly what decides whether to act now: how much, in what
// currency, when it landed, why the matcher declined, and the provider
// reference that finds the same payment in Flutterwave's dashboard. The
// identity evidence lives one click away, where it is access-controlled and
// where acting on it is possible.
func (n *InboundHoldNotifier) compose(credit *models.InboundCredit) (subject, title, message string) {
	amount := credit.Currency + " " + inboundAlertNairaText(credit.GrossKobo)

	subject = fmt.Sprintf("Unattributed payment held — %s", amount)
	title = "Inbound payment needs an operator"

	reason := strings.TrimSpace(credit.HoldReason)
	if reason == "" {
		// The matcher always sets one; an empty reason means something upstream
		// changed, and saying so is more useful than an alert with a blank line.
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

// inboundAlertNairaText renders minor units as grouped major units, e.g.
// 200000 → "2,000.00". Integer arithmetic throughout: these are money figures
// an operator compares against a bank statement, and float formatting is how a
// kobo goes missing.
func inboundAlertNairaText(kobo int64) string {
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
