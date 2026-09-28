package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A refresh that commits chunks and then fails on the tail must not leave the
// old tail behind: committed now covers blocks that tail still counts. Before
// the fix, a following refresh whose (lagging) head had not passed the
// indexed head marked the index fresh and Bought was counted twice.
func TestOTCPosition_FailedTailDoesNotDoubleCount(t *testing.T) {
	f := &fakeOTCChain{head: 1000}
	f.add("Bought", otcWalletA, otcMCGP(100), 50)
	f.add("Bought", otcWalletA, otcMCGP(100), 900) // provisional at head 1000
	s := newTestPositions(f, 0, 0)

	if got := mustPosition(t, s, otcWalletA).Bought; got.Cmp(otcMCGP(200)) != 0 {
		t.Fatalf("bought = %s, want 200", got)
	}

	// Head moves on: blocks up to 1072 commit (including block 900, which the
	// old tail also holds), then the tail read fails.
	f.head = 1200
	f.failFrom = 1073
	if _, err := s.Position(context.Background(), otcWalletA.Hex(), 0); !errors.Is(err, ErrOTCPositionUnavailable) {
		t.Fatalf("expected unavailable while the tail cannot be read, got %v", err)
	}

	// The RPC recovers, but a lagging node reports the old head: the head
	// "does not advance".
	f.failFrom = 0
	f.head = 1000
	if got := mustPosition(t, s, otcWalletA).Bought; got.Cmp(otcMCGP(200)) != 0 {
		t.Fatalf("bought = %s after a failed tail, want 200 (no double count)", got)
	}

	// And once the head really moves on, the totals are still exact.
	f.head = 1300
	if got := mustPosition(t, s, otcWalletA).Bought; got.Cmp(otcMCGP(200)) != 0 {
		t.Fatalf("bought = %s at the next head, want 200", got)
	}
}

// After a failure the tail is rebuilt, and never to a head below one already
// indexed: that would silently drop the logs in between.
func TestOTCPosition_RebuildRefusesAHeadBehindTheIndex(t *testing.T) {
	f := &fakeOTCChain{head: 1000}
	f.add("Sold", otcWalletA, otcMCGP(5), 990)
	s := newTestPositions(f, 0, 0)
	mustPosition(t, s, otcWalletA)

	f.head = 1200
	f.failFrom = 1073
	if _, err := s.Position(context.Background(), otcWalletA.Hex(), 0); err == nil {
		t.Fatal("expected the tail failure to surface")
	}
	f.failFrom = 0
	f.head = 900 // behind the indexed head of 1000
	if _, err := s.Position(context.Background(), otcWalletA.Hex(), 0); !errors.Is(err, ErrOTCPositionUnavailable) {
		t.Fatalf("expected unavailable for a node behind the index, got %v", err)
	}
	f.head = 1000
	if got := mustPosition(t, s, otcWalletA).Sold; got.Cmp(otcMCGP(5)) != 0 {
		t.Fatalf("sold = %s, want 5", got)
	}
}

// A request never hangs on the index: a stuck RPC or a lock held by a long
// warm-up both end in "unavailable" within the budget.
func TestOTCPosition_RequestIsBoundedByItsBudget(t *testing.T) {
	budget, call := otcPositionBudget, otcRPCCallTimeout
	otcPositionBudget, otcRPCCallTimeout = 150*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { otcPositionBudget, otcRPCCallTimeout = budget, call })

	t.Run("stuck getLogs", func(t *testing.T) {
		f := &fakeOTCChain{head: 100_000, hang: true}
		s := newTestPositions(f, 0, 0)
		start := time.Now()
		_, err := s.Position(context.Background(), otcWalletA.Hex(), 0)
		if !errors.Is(err, ErrOTCPositionUnavailable) {
			t.Fatalf("expected unavailable, got %v", err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("Position took %s; it must give up within its budget", took)
		}
	})

	t.Run("lock held elsewhere", func(t *testing.T) {
		f := &fakeOTCChain{head: 1000}
		s := newTestPositions(f, 0, 0)
		if err := s.acquire(context.Background()); err != nil { // e.g. the boot warm-up
			t.Fatal(err)
		}
		defer s.release()
		start := time.Now()
		_, err := s.Position(context.Background(), otcWalletA.Hex(), 0)
		if !errors.Is(err, ErrOTCPositionUnavailable) {
			t.Fatalf("expected unavailable, got %v", err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("Position waited %s for the lock", took)
		}
	})
}

// One transient refusal halves the span; a run of successes grows it back to
// the configured maximum rather than leaving every later scan small.
func TestOTCPosition_ChunkRegrowsAfterTransientRefusal(t *testing.T) {
	f := &fakeOTCChain{head: 20_000, failNext: 1}
	s := newTestPositions(f, 0, 1000)
	mustPosition(t, s, otcWalletA)
	if s.chunk != 1000 {
		t.Fatalf("chunk = %d after catch-up, want it regrown to the configured 1000", s.chunk)
	}

	// A real range cap still holds the span down.
	f2 := &fakeOTCChain{head: 20_000, rangeCap: 500}
	s2 := newTestPositions(f2, 0, 1000)
	mustPosition(t, s2, otcWalletA)
	if s2.chunk > 1000 || s2.chunk < 500/2 {
		t.Fatalf("chunk = %d, want within the cap's range", s2.chunk)
	}
}
