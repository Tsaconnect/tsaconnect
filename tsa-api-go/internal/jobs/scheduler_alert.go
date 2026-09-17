package jobs

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Failure-streak alerting.
//
// Incident 2026-09-11..17: Flutterwave rejected every wallet-statement request,
// so every tick of inbound-credit-poller and inbound-reconciler failed for six
// days. The only signal was a "tick error" log line every 30 seconds, which
// nobody reads, and customer payments sat unattributed until the owner's own
// test buy did not complete. A worker that fails every tick is an outage; this
// is what turns one into a page.

const (
	// failureAlertWindow is roughly how long a worker must fail without a
	// single success before it alerts. A provider 5xx, a DB failover or a
	// deploy restart clears within a tick or two; five minutes of unbroken
	// failure does not, and five extra minutes is nothing next to six days.
	failureAlertWindow = 5 * time.Minute

	// failureAlertMinStreak is the floor for slow workers, where the window is
	// one or two ticks: a single blip on a 5-minute reconciler is not an
	// outage, three in a row (10 minutes) is.
	failureAlertMinStreak = 3

	// failureRealertInterval is how often an unbroken streak alerts again.
	// Hourly: one email is easily lost to a filter or a busy inbox, and the
	// outage this exists for lasted six days — at the 30s poll that is 17,000
	// failed ticks, which must be ~144 emails and not 17,000, but also not one.
	failureRealertInterval = time.Hour
)

// WorkerFailure is what an alert says about a failing worker.
type WorkerFailure struct {
	Worker              string
	ConsecutiveFailures int
	FailingSince        time.Time
	FailingFor          time.Duration
	// LastError is the latest tick's error, already passed through
	// services.RedactAlertText.
	LastError string
}

// WorkerFailureNotifier sends failure-streak alerts somewhere a human will see
// them. Implementations are called from the failing worker's own goroutine and
// must not block on the network; errors and panics are logged and otherwise
// ignored.
type WorkerFailureNotifier interface {
	NotifyWorkerFailing(ctx context.Context, f WorkerFailure) error
	NotifyWorkerRecovered(ctx context.Context, f WorkerFailure) error
}

// RegisterOption configures how the scheduler watches one worker.
type RegisterOption func(*registration)

// AlertOnFailureStreak alerts when the worker has failed every tick for about
// failureAlertWindow — see defaultFailureAlertThreshold. Use it for any worker
// whose silent death leaves money unattributed, unpaid or unreconciled.
func AlertOnFailureStreak() RegisterOption {
	return func(r *registration) {
		r.alertThreshold = defaultFailureAlertThreshold(r.worker.Interval())
	}
}

// AlertAfterFailures alerts after exactly n consecutive failed ticks. n must be
// positive: a zero that silently disabled alerting is the bug this exists for.
func AlertAfterFailures(n int) RegisterOption {
	return func(r *registration) {
		if n <= 0 {
			panic(fmt.Sprintf("jobs.AlertAfterFailures: %q needs a positive threshold, got %d", r.name, n))
		}
		r.alertThreshold = n
	}
}

// defaultFailureAlertThreshold converts the window into ticks for a worker's
// interval, never fewer than failureAlertMinStreak.
func defaultFailureAlertThreshold(interval time.Duration) int {
	n := int(math.Ceil(float64(failureAlertWindow) / float64(interval)))
	return max(n, failureAlertMinStreak)
}

// failureStreak is one worker's unbroken run of failed ticks.
type failureStreak struct {
	count       int
	since       time.Time
	alerted     bool
	lastAlertAt time.Time
}

// recordOutcome updates the streak for one finished tick and alerts, re-alerts
// or announces recovery as it crosses each line.
func (s *Scheduler) recordOutcome(ctx context.Context, reg *registration, err error) {
	if reg.alertThreshold == 0 {
		return
	}
	now := s.now()
	st := &reg.streak

	if err == nil {
		if st.alerted {
			f := reg.failure(now, "")
			s.logf("[jobs] RECOVERED worker_recovered_total=1 worker=%s consecutive_failures=%d failed_for=%s since=%s",
				f.Worker, f.ConsecutiveFailures, f.FailingFor, f.FailingSince.UTC().Format(time.RFC3339))
			s.notify(ctx, "recovered", f)
		}
		*st = failureStreak{}
		return
	}

	if st.count == 0 {
		st.since = now
	}
	st.count++
	if st.count < reg.alertThreshold {
		return
	}
	if st.alerted && now.Sub(st.lastAlertAt) < failureRealertInterval {
		return
	}
	st.alerted = true
	st.lastAlertAt = now

	f := reg.failure(now, services.RedactAlertText(err.Error()))
	s.logf("[jobs] ALERT worker_failing_total=1 worker=%s consecutive_failures=%d failing_for=%s since=%s last_error=%q",
		f.Worker, f.ConsecutiveFailures, f.FailingFor, f.FailingSince.UTC().Format(time.RFC3339), f.LastError)
	s.notify(ctx, "failing", f)
}

func (reg *registration) failure(now time.Time, lastError string) WorkerFailure {
	return WorkerFailure{
		Worker:              reg.name,
		ConsecutiveFailures: reg.streak.count,
		FailingSince:        reg.streak.since,
		FailingFor:          now.Sub(reg.streak.since).Round(time.Second),
		LastError:           lastError,
	}
}

// notify hands an alert to the notifier without letting it hurt the worker.
// The log line is already written, so a failed notification loses the email,
// never the alert.
func (s *Scheduler) notify(ctx context.Context, kind string, f WorkerFailure) {
	n := s.failureNotifier
	if n == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.logf("[jobs] worker %q %s notify failed: panic: %v", f.Worker, kind, r)
		}
	}()
	var err error
	if kind == "recovered" {
		err = n.NotifyWorkerRecovered(ctx, f)
	} else {
		err = n.NotifyWorkerFailing(ctx, f)
	}
	if err != nil {
		s.logf("[jobs] worker %q %s notify failed: %v", f.Worker, kind, err)
	}
}
