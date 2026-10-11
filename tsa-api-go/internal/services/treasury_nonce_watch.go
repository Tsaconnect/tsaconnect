package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ErrTreasuryNonceStuck: a treasury transaction has sat unmined long enough
// that every later send on the lane is blocked (spec F24, S43). Pool opens
// are refused; the runbook replaces the nonce.
var ErrTreasuryNonceStuck = errors.New("conversion treasury: a transaction is stuck on this chain")

// TreasuryNonceReader is the slice of a chain client the watch reads;
// TreasuryTxProbe (blockchain.ProbeClient) satisfies it.
type TreasuryNonceReader interface {
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error)
}

// TreasuryNonceWatch watches one lane's address for a stuck nonce (plan C1
// P11): stuck = a pending-nonce gap with no progress of the latest nonce for
// stuckAfter.
type TreasuryNonceWatch struct {
	chain      string
	r          TreasuryNonceReader
	addr       common.Address
	stuckAfter time.Duration
	now        func() time.Time

	mu         sync.Mutex
	gapSince   time.Time
	lastLatest uint64
}

func NewTreasuryNonceWatch(chain string, r TreasuryNonceReader, addr string, stuckAfter time.Duration, now func() time.Time) *TreasuryNonceWatch {
	if now == nil {
		now = time.Now
	}
	if stuckAfter <= 0 {
		stuckAfter = 30 * time.Second
	}
	return &TreasuryNonceWatch{chain: chain, r: r, addr: common.HexToAddress(addr), stuckAfter: stuckAfter, now: now}
}

// Check reads the lane's nonces: nil, ErrTreasuryNonceStuck, or a redacted
// read error (the caller fails closed on both).
func (w *TreasuryNonceWatch) Check(ctx context.Context) error {
	pending, err := w.r.PendingNonceAt(ctx, w.addr)
	if err != nil {
		return fmt.Errorf("treasury nonce on %s: %s", w.chain, RedactAlertText(err.Error()))
	}
	latest, err := w.r.NonceAt(ctx, w.addr, nil)
	if err != nil {
		return fmt.Errorf("treasury nonce on %s: %s", w.chain, RedactAlertText(err.Error()))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if pending <= latest {
		w.gapSince = time.Time{}
		return nil
	}
	if w.gapSince.IsZero() || latest != w.lastLatest {
		w.gapSince, w.lastLatest = now, latest
		return nil
	}
	if since := now.Sub(w.gapSince); since >= w.stuckAfter {
		log.Printf("[liquidity] ALERT ramp_treasury_nonce_stuck chain=%s latest=%d pending=%d for=%s", w.chain, latest, pending, since)
		return ErrTreasuryNonceStuck
	}
	return nil
}
