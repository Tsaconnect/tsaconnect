package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Re-review item 4: a slow older count finishing after a fresh one never
// overwrites it, and the read runs on its own context — the first caller
// giving up does not fail the others.
func TestLaneCommitments_OlderReadNeverOverwritesAFresherCount(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := newLaneCommitments("test", func(ctx context.Context) (uint64, error) {
		if calls.Add(1) == 1 {
			close(started)
			select { // the slow first read
			case <-release:
				return 5, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return 9, nil
	})
	// The first caller gives up at once; its read carries on regardless.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Units(gone); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled caller: err=%v", err)
	}
	<-started // the old read is in flight
	if u, err := c.UnitsFresh(context.Background()); err != nil || u != 9 {
		t.Fatalf("fresh count = %d, %v; want 9", u, err)
	}
	close(release)                     // the old read now finishes with 5
	time.Sleep(100 * time.Millisecond) // let the old read store (or not) its 5
	if u, err := c.Units(context.Background()); err != nil || u != 9 {
		t.Fatalf("cached count = %d, %v; want the fresher 9, never the older 5", u, err)
	}
}

// A failed count is never cached and is an error, never zero.
func TestLaneCommitments_FailureIsAnErrorAndNotCached(t *testing.T) {
	fail := true
	c := newLaneCommitments("test", func(context.Context) (uint64, error) {
		if fail {
			return 0, errors.New("db down")
		}
		return 3, nil
	})
	if _, err := c.Units(context.Background()); err == nil {
		t.Fatal("a failed count must be an error")
	}
	fail = false
	if u, err := c.Units(context.Background()); err != nil || u != 3 {
		t.Fatalf("after recovery = %d, %v", u, err)
	}
}
