package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePoolFillRetrier struct {
	calls int
	n     int
	err   error
}

func (f *fakePoolFillRetrier) AutoRetryPoolFills(context.Context) (int, error) {
	f.calls++
	return f.n, f.err
}

func TestPoolFillAutoRetryJob_RunsTheRetryAndReportsItsError(t *testing.T) {
	f := &fakePoolFillRetrier{n: 2}
	j := NewPoolFillAutoRetryJob(f, 0)
	if err := j.Run(context.Background()); err != nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
	if j.Name() != "liquidity-pool-fill-auto-retry" || j.Interval() != 2*time.Minute {
		t.Fatalf("name=%s every=%s", j.Name(), j.Interval())
	}
	f.err = errors.New("vault unreadable")
	if err := j.Run(context.Background()); err == nil {
		t.Fatal("want the error reported so the failure streak pages")
	}
	s := NewScheduler()
	s.Register(j, AlertOnFailureStreak())
	if s.workers[0].alertThreshold <= 0 {
		t.Fatal("not alert-watched")
	}
}
