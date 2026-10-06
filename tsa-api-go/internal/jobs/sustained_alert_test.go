package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type recordingNotifier struct {
	failing, recovered []WorkerFailure
}

func (r *recordingNotifier) NotifyWorkerFailing(_ context.Context, f WorkerFailure) error {
	r.failing = append(r.failing, f)
	return nil
}
func (r *recordingNotifier) NotifyWorkerRecovered(_ context.Context, f WorkerFailure) error {
	r.recovered = append(r.recovered, f)
	return nil
}

func TestSustainedAlert_WaitsRepeatsHourlyAndRecovers(t *testing.T) {
	n := &recordingNotifier{}
	a := &sustainedAlert{worker: "w", metric: "m", what: "%d waiting", after: 5 * time.Minute, every: time.Hour, notifier: n}
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	a.track(ctx, 1, start)
	if len(n.failing) != 0 {
		t.Fatal("alerted before the condition lasted 5 minutes")
	}
	a.track(ctx, 3, start.Add(6*time.Minute))
	if len(n.failing) != 1 || n.failing[0].Worker != "w" || n.failing[0].ConsecutiveFailures != 3 || n.failing[0].LastError != "3 waiting" {
		t.Fatalf("after 6m: %+v", n.failing)
	}
	a.track(ctx, 3, start.Add(30*time.Minute))
	if len(n.failing) != 1 {
		t.Fatal("re-alerted inside the hour")
	}
	a.track(ctx, 2, start.Add(67*time.Minute))
	if len(n.failing) != 2 {
		t.Fatalf("no hourly repeat: %d", len(n.failing))
	}
	a.track(ctx, 0, start.Add(68*time.Minute))
	if len(n.recovered) != 1 || n.recovered[0].FailingFor != 68*time.Minute {
		t.Fatalf("recovered = %+v", n.recovered)
	}
	a.track(ctx, 0, start.Add(69*time.Minute))
	if len(n.recovered) != 1 {
		t.Fatal("a second recovery notice for one incident")
	}
}

func TestSustainedAlert_ZeroDelayAlertsAtOnceAndNilNotifierOnlyLogs(t *testing.T) {
	a := &sustainedAlert{worker: "w", metric: "m", what: "%d", every: time.Hour}
	a.track(context.Background(), 1, time.Now()) // must not panic without a notifier
	n := &recordingNotifier{}
	b := &sustainedAlert{worker: "w", metric: "m", what: "%d", every: time.Hour, notifier: n}
	b.track(context.Background(), 1, time.Now())
	if len(n.failing) != 1 {
		t.Fatal("after=0 must alert on the first sighting")
	}
}

// Decision D16, through the watcher: a short float alerts on its own channel
// and never fails the tick.
func TestStableBuyWatcher_ASustainedShortFloatAlertsOnItsOwnChannel(t *testing.T) {
	f := &fakeBuyDriver{delivering: []uuid.UUID{uuid.New(), uuid.New()},
		deliverErr: fmt.Errorf("%w: USDC available=1 needed=97.5", services.ErrStableFloatShort)}
	n := &recordingNotifier{}
	w := NewStableBuyWatcher(f, 0)
	w.SetAlerter(n)
	start := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	clock := start
	w.now = func() time.Time { return clock }
	tick := func(at time.Duration) {
		t.Helper()
		clock = start.Add(at)
		if err := w.Run(context.Background()); err != nil {
			t.Fatalf("tick at %s: %v — a short float must never fail the tick", at, err)
		}
	}
	tick(0)
	tick(6 * time.Minute)
	if len(n.failing) != 1 || n.failing[0].Worker != "it-stable-buy-float" || n.failing[0].ConsecutiveFailures != 2 {
		t.Fatalf("after 6m: %+v", n.failing)
	}
	f.deliverErr = nil
	tick(7 * time.Minute)
	if len(n.recovered) != 1 {
		t.Fatal("no recovery notice once the float covers deliveries")
	}
}

func TestStableBuyWatcher_OtherDeliveryErrorsStaySwallowed(t *testing.T) {
	n := &recordingNotifier{}
	w := NewStableBuyWatcher(&fakeBuyDriver{delivering: []uuid.UUID{uuid.New()}, deliverErr: errors.New("boom")}, 0)
	w.SetAlerter(n)
	if err := w.Run(context.Background()); err != nil || len(n.failing) != 0 {
		t.Fatalf("err = %v alerts = %d, want nil and none", err, len(n.failing))
	}
}
