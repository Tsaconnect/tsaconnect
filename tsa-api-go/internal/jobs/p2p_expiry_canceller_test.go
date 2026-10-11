package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

type stubExpiryRunner struct{ tick services.ExpiryTick }

func (s *stubExpiryRunner) RunOnce(context.Context) (services.ExpiryTick, error) { return s.tick, nil }

func TestP2PExpiryCanceller_EveryMinuteAndSustainedFailureAlert(t *testing.T) {
	r := &stubExpiryRunner{tick: services.ExpiryTick{Failed: 2}}
	j := NewP2PExpiryCanceller(r, nil)
	if j.Name() != "p2p-expiry-canceller" || j.Interval() != time.Minute {
		t.Fatalf("job = %s %s", j.Name(), j.Interval())
	}
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j.failing.since.IsZero() {
		t.Fatal("failed cancels were not tracked for the sustained p2p_expiry_cancel_failed alert")
	}
}
