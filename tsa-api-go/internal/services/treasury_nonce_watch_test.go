package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type fakeNonces struct {
	mu              sync.Mutex
	pending, latest uint64
	err             error
}

func (f *fakeNonces) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending, f.err
}
func (f *fakeNonces) NonceAt(context.Context, common.Address, *big.Int) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest, f.err
}
func (f *fakeNonces) set(pending, latest uint64) {
	f.mu.Lock()
	f.pending, f.latest = pending, latest
	f.mu.Unlock()
}

// F24 / P11: a gap is normal for a moment after every send; stuck means the
// latest nonce has not advanced for 30 s while a gap stands.
func TestTreasuryNonceWatch_StuckOnlyWhenLatestDoesNotMove(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	r := &fakeNonces{}
	w := NewTreasuryNonceWatch("bsc", r, sponsorTestSigner, 30*time.Second, clock)
	ctx := context.Background()
	r.set(5, 5)
	if err := w.Check(ctx); err != nil {
		t.Fatalf("no gap: %v", err)
	}
	r.set(7, 5) // two in flight
	if err := w.Check(ctx); err != nil {
		t.Fatalf("a fresh gap: %v", err)
	}
	now = now.Add(20 * time.Second)
	r.set(8, 6) // progress: the clock restarts
	if err := w.Check(ctx); err != nil {
		t.Fatalf("progressing: %v", err)
	}
	now = now.Add(29 * time.Second)
	if err := w.Check(ctx); err != nil {
		t.Fatalf("29 s without progress: %v", err)
	}
	now = now.Add(2 * time.Second)
	if err := w.Check(ctx); !errors.Is(err, ErrTreasuryNonceStuck) {
		t.Fatalf("31 s without progress: %v", err)
	}
	r.set(8, 8) // cleared (the runbook's same-nonce replacement landed)
	if err := w.Check(ctx); err != nil {
		t.Fatalf("cleared: %v", err)
	}
}

func TestTreasuryNonceWatch_UnreadableIsAnErrorNotStuck(t *testing.T) {
	r := &fakeNonces{err: errors.New("dial tcp https://bsc.example/key123: timeout")}
	err := NewTreasuryNonceWatch("bsc", r, sponsorTestSigner, 30*time.Second, nil).Check(context.Background())
	if err == nil || errors.Is(err, ErrTreasuryNonceStuck) || strings.Contains(err.Error(), "key123") {
		t.Fatalf("err = %v, want a redacted read error (the RPC URL carries a key)", err)
	}
}
