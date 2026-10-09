package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubNonceChecker struct{ err error }

func (s stubNonceChecker) Check(context.Context) error { return s.err }

func TestTreasuryNonceWatchJob_ReturnsTheStuckError(t *testing.T) {
	stuck := errors.New("stuck")
	j := NewTreasuryNonceWatchJob(stubNonceChecker{err: stuck}, 15*time.Second)
	if j.Name() != "liquidity-treasury-nonce-watch" || j.Interval() != 15*time.Second || !errors.Is(j.Run(context.Background()), stuck) {
		t.Fatalf("job = %s %s", j.Name(), j.Interval())
	}
}
