package services

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// WorkerFailureAlert is the scheduler's finding that a money-path background
// worker has been failing every tick, restated here for the same import-cycle
// reason as InboundGapAlert: internal/jobs imports internal/services, so
// main.go copies jobs.WorkerFailure into this.
//
// LastError is expected to be redacted already (RedactAlertText); compose runs
// it through again because this is the channel with no access control.
type WorkerFailureAlert struct {
	Worker              string
	ConsecutiveFailures int
	FailingSince        time.Time
	FailingFor          time.Duration
	LastError           string
}

// NotifyWorkerFailing emails operators that a worker has failed every tick for
// long enough to be an outage rather than a blip.
//
// It rides the inbound alert channel on purpose. Until 2026-09-17 the
// inbound-credit-poller and inbound-reconciler failed every tick for six days
// and the only trace was a log line: customer payments sat unattributed and the
// reconciler built to catch exactly that was dead too. The people who must hear
// about that are the INBOUND_ALERT_EMAILS operators, and a second recipient
// list and send loop is a second thing to keep in step.
//
// Same contract as NotifyHold: never returns an error, never blocks on the
// network (the caller is the failing worker's own tick goroutine), quiet no-op
// when unconfigured.
func (n *InboundHoldNotifier) NotifyWorkerFailing(_ context.Context, a WorkerFailureAlert) error {
	if n == nil || n.mailer == nil || len(n.to) == 0 {
		return nil
	}
	lastError := RedactAlertText(a.LastError)

	var b strings.Builder
	fmt.Fprintf(&b, "The background job %q has failed on every run for %s.\n\n", a.Worker, workerAlertDuration(a.FailingFor))
	fmt.Fprintf(&b, "Consecutive failures: %d\n", a.ConsecutiveFailures)
	fmt.Fprintf(&b, "Failing since: %s UTC\n", a.FailingSince.UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Failing for: %s\n", workerAlertDuration(a.FailingFor))
	fmt.Fprintf(&b, "Latest error: %s\n\n", lastError)
	b.WriteString("Whatever this job exists to catch — payments to attribute, payouts to finish, money to reconcile — is not being caught while it fails. " +
		"This alert repeats hourly while the failure continues, and a recovery email follows the next successful run.")

	n.enqueue(alertJob{
		kind:    "failing worker",
		ref:     a.Worker,
		subject: fmt.Sprintf("Background job failing — %s (%d consecutive failures)", a.Worker, a.ConsecutiveFailures),
		title:   "A money-path background job is failing",
		message: b.String(),
	})
	return nil
}

// NotifyWorkerRecovered closes the loop on NotifyWorkerFailing, so nobody keeps
// chasing an outage that has ended — and so they know how long it lasted,
// which is the window to go back over for anything it missed.
func (n *InboundHoldNotifier) NotifyWorkerRecovered(_ context.Context, a WorkerFailureAlert) error {
	if n == nil || n.mailer == nil || len(n.to) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The background job %q succeeded again after %d consecutive failures.\n\n", a.Worker, a.ConsecutiveFailures)
	fmt.Fprintf(&b, "Failing since: %s UTC\n", a.FailingSince.UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Failed for: %s\n\n", workerAlertDuration(a.FailingFor))
	b.WriteString("Anything the job should have handled during that window is being picked up from now on. " +
		"Check that it was — a job that recovers does not undo what a customer saw while it was down.")

	n.enqueue(alertJob{
		kind:    "recovered worker",
		ref:     a.Worker,
		subject: fmt.Sprintf("Background job recovered — %s", a.Worker),
		title:   "A money-path background job has recovered",
		message: b.String(),
	})
	return nil
}

// workerAlertDuration rounds to the second: a failing-for of 96h0m0.000123s
// reads as noise.
func workerAlertDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}
