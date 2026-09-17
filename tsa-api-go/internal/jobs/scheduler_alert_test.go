package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingFailureNotifier captures what the scheduler hands the alert channel.
// It can fail or panic on demand so the isolation guard has something to
// survive.
type recordingFailureNotifier struct {
	mu        sync.Mutex
	failing   []WorkerFailure
	recovered []WorkerFailure
	err       error
	panics    bool
}

func (r *recordingFailureNotifier) NotifyWorkerFailing(_ context.Context, f WorkerFailure) error {
	if r.panics {
		panic("mailer exploded")
	}
	r.mu.Lock()
	r.failing = append(r.failing, f)
	r.mu.Unlock()
	return r.err
}

func (r *recordingFailureNotifier) NotifyWorkerRecovered(_ context.Context, f WorkerFailure) error {
	if r.panics {
		panic("mailer exploded")
	}
	r.mu.Lock()
	r.recovered = append(r.recovered, f)
	r.mu.Unlock()
	return r.err
}

// scriptedWorker returns whatever the test sets before each tick.
type scriptedWorker struct {
	name     string
	interval time.Duration
	next     error
	panicMsg string
}

func (w *scriptedWorker) Name() string            { return w.name }
func (w *scriptedWorker) Interval() time.Duration { return w.interval }
func (w *scriptedWorker) Run(context.Context) error {
	if w.panicMsg != "" {
		panic(w.panicMsg)
	}
	return w.next
}

// alertHarness drives single ticks against a fake clock, so streak, re-alert
// and recovery behaviour is tested without a real ticker or a sleep.
type alertHarness struct {
	t        *testing.T
	s        *Scheduler
	w        *scriptedWorker
	reg      *registration
	notifier *recordingFailureNotifier
	now      time.Time
	logs     []string
}

var harnessStart = time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)

func newAlertHarness(t *testing.T, interval time.Duration, opts ...RegisterOption) *alertHarness {
	t.Helper()
	h := &alertHarness{
		t:        t,
		s:        NewScheduler(),
		w:        &scriptedWorker{name: "inbound-credit-poller", interval: interval},
		notifier: &recordingFailureNotifier{},
		now:      harnessStart,
	}
	h.s.now = func() time.Time { return h.now }
	h.s.logf = func(format string, args ...any) { h.logs = append(h.logs, fmt.Sprintf(format, args...)) }
	h.s.SetFailureNotifier(h.notifier)
	h.s.Register(h.w, opts...)
	h.reg = h.s.workers[0]
	return h
}

// tick runs one tick at the current clock, then advances the clock by one
// interval — the cadence the real ticker would give.
func (h *alertHarness) tick(err error) {
	h.w.next = err
	h.s.tick(context.Background(), h.reg)
	h.now = h.now.Add(h.w.interval)
}

func (h *alertHarness) failN(n int, err error) {
	for i := 0; i < n; i++ {
		h.tick(err)
	}
}

func (h *alertHarness) linesContaining(sub string) []string {
	var out []string
	for _, l := range h.logs {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}

var errStatement = errors.New("flutterwave wallet-statement: HTTP 400: page size must not exceed 50")

func TestSchedulerFailureAlertFiresOnceAtThreshold(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))

	h.failN(2, errStatement)
	if len(h.notifier.failing) != 0 || len(h.linesContaining("ALERT ")) != 0 {
		t.Fatalf("alerted before the threshold: notified=%d logs=%v", len(h.notifier.failing), h.logs)
	}

	h.tick(errStatement) // third consecutive failure
	if got := len(h.notifier.failing); got != 1 {
		t.Fatalf("notified %d times at the threshold, want 1", got)
	}
	f := h.notifier.failing[0]
	if f.Worker != "inbound-credit-poller" || f.ConsecutiveFailures != 3 {
		t.Errorf("alert = %+v, want worker inbound-credit-poller with 3 failures", f)
	}
	if !f.FailingSince.Equal(harnessStart) || f.FailingFor != time.Minute {
		t.Errorf("since=%s for=%s, want since=%s for=1m", f.FailingSince, f.FailingFor, harnessStart)
	}
	if f.LastError != errStatement.Error() {
		t.Errorf("last error = %q", f.LastError)
	}

	// The same unbroken streak must not alert again inside the re-alert window.
	h.failN(20, errStatement)
	if got := len(h.notifier.failing); got != 1 {
		t.Errorf("notified %d times for one streak inside the re-alert window, want 1", got)
	}
	if got := len(h.linesContaining("ALERT worker_failing_total=1")); got != 1 {
		t.Errorf("logged %d ALERT lines, want 1: %v", got, h.logs)
	}
}

func TestSchedulerFailureAlertLogLineIsGreppable(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
	h.failN(3, errStatement)

	want := `[jobs] ALERT worker_failing_total=1 worker=inbound-credit-poller consecutive_failures=3 failing_for=1m0s since=2026-09-11T08:00:00Z last_error="flutterwave wallet-statement: HTTP 400: page size must not exceed 50"`
	if got := h.linesContaining("ALERT "); len(got) != 1 || got[0] != want {
		t.Errorf("alert line:\n got %q\nwant %q", got, want)
	}
}

func TestSchedulerFailureAlertSuccessBeforeThresholdResetsSilently(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))

	h.failN(2, errStatement)
	h.tick(nil)
	if len(h.notifier.recovered) != 0 || len(h.linesContaining("RECOVERED")) != 0 {
		t.Fatalf("a streak that never alerted announced a recovery: %v", h.logs)
	}

	h.failN(2, errStatement)
	if len(h.notifier.failing) != 0 {
		t.Fatalf("2+2 failures split by a success alerted; the success must reset the count")
	}

	secondStreakStart := h.now
	h.tick(errStatement)
	if got := len(h.notifier.failing); got != 1 {
		t.Fatalf("notified %d times, want 1 once the new streak reaches 3", got)
	}
	if f := h.notifier.failing[0]; f.ConsecutiveFailures != 3 || !f.FailingSince.Equal(secondStreakStart.Add(-time.Minute)) {
		t.Errorf("alert = %+v; the streak must restart after a success", f)
	}
}

func TestSchedulerFailureAlertRepeatsAfterRealertInterval(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
	h.failN(3, errStatement) // alert #1 at 08:01:00

	// Ticks up to (but not including) an hour after the first alert stay quiet.
	for h.now.Before(harnessStart.Add(time.Minute + failureRealertInterval)) {
		h.tick(errStatement)
	}
	if got := len(h.notifier.failing); got != 1 {
		t.Fatalf("re-alerted inside the interval: %d notifications", got)
	}

	h.tick(errors.New("still broken")) // exactly one hour after alert #1
	if got := len(h.notifier.failing); got != 2 {
		t.Fatalf("notified %d times after the re-alert interval, want 2", got)
	}
	f := h.notifier.failing[1]
	if f.ConsecutiveFailures != 123 || f.FailingFor != time.Hour+time.Minute || f.LastError != "still broken" {
		t.Errorf("re-alert = %+v, want the running count, duration and the latest error", f)
	}
	if got := len(h.linesContaining("ALERT worker_failing_total=1")); got != 2 {
		t.Errorf("logged %d ALERT lines, want 2", got)
	}
}

func TestSchedulerFailureAlertRecoveryNotifiesOnceAndResets(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
	h.failN(4, errStatement)
	h.tick(nil)

	if got := len(h.notifier.recovered); got != 1 {
		t.Fatalf("recovery notified %d times, want 1", got)
	}
	r := h.notifier.recovered[0]
	if r.Worker != "inbound-credit-poller" || r.ConsecutiveFailures != 4 || r.FailingFor != 2*time.Minute {
		t.Errorf("recovery = %+v, want 4 failures over 2m", r)
	}
	want := "[jobs] RECOVERED worker_recovered_total=1 worker=inbound-credit-poller consecutive_failures=4 failed_for=2m0s since=2026-09-11T08:00:00Z"
	if got := h.linesContaining("RECOVERED"); len(got) != 1 || got[0] != want {
		t.Errorf("recovery line:\n got %q\nwant %q", got, want)
	}

	h.tick(nil)
	if got := len(h.notifier.recovered); got != 1 {
		t.Errorf("a second success re-announced recovery: %d", got)
	}

	// The recovered streak is gone: the next failures count from one.
	h.failN(2, errStatement)
	if got := len(h.notifier.failing); got != 1 {
		t.Errorf("failures after recovery alerted below the threshold: %d notifications", got)
	}
	h.tick(errStatement)
	if got := len(h.notifier.failing); got != 2 {
		t.Errorf("a fresh streak reaching the threshold after recovery must alert again: %d", got)
	}
}

func TestSchedulerFailureAlertSurvivesNotifierFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*recordingFailureNotifier)
	}{
		{"error", func(n *recordingFailureNotifier) { n.err = errors.New("mailjet down") }},
		{"panic", func(n *recordingFailureNotifier) { n.panics = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
			tc.setup(h.notifier)

			h.failN(3, errStatement)
			if got := len(h.linesContaining("ALERT worker_failing_total=1")); got != 1 {
				t.Fatalf("the log alert must not depend on the notifier: %v", h.logs)
			}
			if got := len(h.linesContaining("notify failed")); got != 1 {
				t.Errorf("a failed notification must be logged: %v", h.logs)
			}

			h.tick(nil)
			if got := len(h.linesContaining("RECOVERED")); got != 1 {
				t.Errorf("the worker must keep being tracked after a notifier failure: %v", h.logs)
			}
		})
	}
}

func TestSchedulerFailureAlertLogsWithoutNotifier(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
	h.s.failureNotifier = nil
	h.failN(3, errStatement)
	h.tick(nil)
	if len(h.linesContaining("ALERT worker_failing_total=1")) != 1 || len(h.linesContaining("RECOVERED")) != 1 {
		t.Errorf("with no notifier the log lines are the alert: %v", h.logs)
	}
}

func TestSchedulerFailureAlertOffUnlessOptedIn(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second)
	h.failN(500, errStatement)
	h.tick(nil)
	if len(h.notifier.failing)+len(h.notifier.recovered) != 0 || len(h.linesContaining("ALERT ")) != 0 {
		t.Errorf("a worker registered without an alert option alerted")
	}
}

func TestSchedulerFailureAlertCountsPanics(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
	h.w.panicMsg = "nil map write"
	h.failN(3, nil)
	if got := len(h.notifier.failing); got != 1 {
		t.Fatalf("a panicking tick is a failed tick: notified %d times", got)
	}
	if le := h.notifier.failing[0].LastError; !strings.Contains(le, "panic") || !strings.Contains(le, "nil map write") {
		t.Errorf("last error = %q, want the panic", le)
	}
}

// Threshold 4 with 2 + 1 real failures: only a shutdown tick wrongly counted
// as the fourth failure could alert.
func TestSchedulerFailureAlertIgnoresShutdownErrors(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(4))
	h.failN(2, errStatement)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.w.next = context.Canceled
	h.s.tick(ctx, h.reg)

	h.tick(errStatement)
	if got := len(h.notifier.failing); got != 0 {
		t.Errorf("a tick cut short by shutdown counted as a failure: %d notifications", got)
	}
}

func TestSchedulerFailureAlertRedactsErrorText(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertAfterFailures(3))
	leaky := errors.New(`Get "https://api.flutterwave.cloud/f4bexperience/wallets/statement?from=2026-09-11&size=100&client_secret=s3cr3t": ` +
		`Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.abc; rpc https://sonic.g.alchemy.com/v2/AlchemyKey123 refused`)
	h.failN(3, leaky)

	sent := h.notifier.failing[0].LastError
	logLine := h.linesContaining("ALERT ")[0]
	for _, secret := range []string{"s3cr3t", "eyJhbGciOiJIUzI1NiJ9", "AlchemyKey123", "wallets/statement?"} {
		if strings.Contains(sent, secret) || strings.Contains(logLine, secret) {
			t.Errorf("alert leaked %q\nnotified: %s\nlog: %s", secret, sent, logLine)
		}
	}
	if !strings.Contains(sent, "api.flutterwave.cloud") {
		t.Errorf("the host is diagnostic and not secret; keep it: %s", sent)
	}
}

func TestDefaultFailureAlertThreshold(t *testing.T) {
	for _, tc := range []struct {
		interval time.Duration
		want     int
	}{
		{20 * time.Second, 15}, // paycrest poller: 5 minutes
		{30 * time.Second, 10}, // inbound poller: 5 minutes
		{45 * time.Second, 7},
		{time.Minute, 5},
		{2 * time.Minute, 3},
		{5 * time.Minute, 3}, // floor: one blip on a slow reconciler is not an outage
		{time.Hour, 3},
	} {
		if got := defaultFailureAlertThreshold(tc.interval); got != tc.want {
			t.Errorf("defaultFailureAlertThreshold(%s) = %d, want %d", tc.interval, got, tc.want)
		}
	}
}

func TestAlertOnFailureStreakUsesTheIntervalDefault(t *testing.T) {
	h := newAlertHarness(t, 30*time.Second, AlertOnFailureStreak())
	if h.reg.alertThreshold != 10 {
		t.Errorf("threshold = %d, want 10 for a 30s worker", h.reg.alertThreshold)
	}
}

func TestAlertAfterFailuresRejectsNonPositive(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("AlertAfterFailures(0) must panic at Register, not silently disable alerting")
		}
	}()
	NewScheduler().Register(&scriptedWorker{name: "x", interval: time.Second}, AlertAfterFailures(0))
}

func TestSetFailureNotifierAfterStartPanics(t *testing.T) {
	s := NewScheduler()
	s.Start(context.Background())
	defer s.Stop()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when setting the notifier after Start")
		}
	}()
	s.SetFailureNotifier(&recordingFailureNotifier{})
}
