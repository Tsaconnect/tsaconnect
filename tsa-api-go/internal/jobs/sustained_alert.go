package jobs

import (
	"context"
	"fmt"
	"log"
	"time"
)

// sustainedAlert reports a condition that persists across ticks — a short
// Flutterwave float, company stock short at send, a stalled send — on its own
// alert channel, apart from the worker's failure streak (Decisions D16, D43).
// It never fails the tick: it alerts once the condition has lasted `after`,
// repeats at most every `every`, and sends one recovery notice when it clears.
// Only the scheduler's single goroutine for the owning worker calls track.
type sustainedAlert struct {
	worker   string // the channel name operators see ("it-stable-buy-float")
	metric   string // the log metric stem ("ramp_fw_float_short")
	what     string // LastError wording; %d is how many are affected
	after    time.Duration
	every    time.Duration
	notifier WorkerFailureNotifier // nil logs only

	since, alerted time.Time
}

// track records that n items are in the condition right now.
func (a *sustainedAlert) track(ctx context.Context, n int, now time.Time) {
	if n == 0 {
		if !a.alerted.IsZero() {
			f := WorkerFailure{Worker: a.worker, FailingSince: a.since, FailingFor: now.Sub(a.since)}
			log.Printf("[%s] RECOVERED %s_recovered_total=1 for=%s", a.worker, a.metric, f.FailingFor)
			a.notify(ctx, f, true)
		}
		a.since, a.alerted = time.Time{}, time.Time{}
		return
	}
	if a.since.IsZero() {
		a.since = now
	}
	f := WorkerFailure{
		Worker: a.worker, ConsecutiveFailures: n,
		FailingSince: a.since, FailingFor: now.Sub(a.since),
		LastError: fmt.Sprintf(a.what, n),
	}
	if f.FailingFor < a.after {
		return
	}
	if !a.alerted.IsZero() && now.Sub(a.alerted) < a.every {
		return
	}
	a.alerted = now
	log.Printf("[%s] ALERT %s_sustained_total=1 count=%d for=%s", a.worker, a.metric, n, f.FailingFor)
	a.notify(ctx, f, false)
}

func (a *sustainedAlert) notify(ctx context.Context, f WorkerFailure, recovered bool) {
	if a.notifier == nil {
		return
	}
	var err error
	if recovered {
		err = a.notifier.NotifyWorkerRecovered(ctx, f)
	} else {
		err = a.notifier.NotifyWorkerFailing(ctx, f)
	}
	if err != nil {
		log.Printf("[%s] alert notify failed: %v", a.worker, err)
	}
}
