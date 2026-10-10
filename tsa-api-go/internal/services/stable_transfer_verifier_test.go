package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

// The shared verifier reads who signed a transfer, when it mined and how deep
// it is — on any chain it was given a verifier and a reader for, and no other.
func TestStableTransferVerifier_ReportsSignerTimeAndDepth(t *testing.T) {
	tx, from := signedFeeTx(t)
	minedAt := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	v := NewStableTransferVerifier(
		map[string]ERC20TransferVerifier{"bsc": fakeSourcedVerifier{credited: big.NewInt(12_500_000)}},
		map[string]FeeTxReader{"bsc": &fakeFeeTxReader{tx: tx, block: 200, head: 204, blockAt: minedAt}})

	p, err := v.Verify(context.Background(), "BSC", "0x1", "0xtoken", from, "0xto")
	if err != nil {
		t.Fatal(err)
	}
	if p.Sender != from || p.Confirmations != 5 || !p.BlockTime.Equal(minedAt) || p.Credited.String() != "12500000" {
		t.Fatalf("payment %+v", p)
	}
	if _, err := v.Verify(context.Background(), "base", "0x1", "0xtoken", from, "0xto"); err == nil {
		t.Fatal("a chain with no verifier must not prove anything")
	}
	known, sender, err := v.TransactionKnown(context.Background(), "bsc", "0x1")
	if err != nil || !known || sender != from {
		t.Fatalf("known=%v sender=%s err=%v", known, sender, err)
	}
}

// The policy every stablecoin proof is held to: the right sender, not mined
// before it was asked for, deep enough — and a mined transfer that moved
// nothing is final only once it is as deep as a payment must be.
func TestProveStableTransfer_HoldsATransferToSenderTimeAndDepth(t *testing.T) {
	opened := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	want := StableTransferWant{From: "0xAbC0000000000000000000000000000000000001", NotBefore: opened, MinConf: 5}
	paid := func(credited int64, sender string, at time.Time, conf uint64) *StablePayment {
		return &StablePayment{Credited: big.NewInt(credited), Sender: sender, BlockTime: at, Confirmations: conf}
	}
	noMatch := fmt.Errorf("%w: tx=0x1", ErrTransferNoMatch)
	for name, tc := range map[string]struct {
		p    *StablePayment
		err  error
		want StableTransferOutcome
	}{
		"deep, right sender, after opening":  {paid(1, want.From, opened, 5), nil, StableTransferOutcome{StableTransferProven, StableReasonNone}},
		"sender compared case-insensitively": {paid(1, strings.ToLower(want.From), opened, 9), nil, StableTransferOutcome{StableTransferProven, StableReasonNone}},
		"not deep enough":                    {paid(1, want.From, opened, 4), nil, StableTransferOutcome{StableTransferPending, StableReasonShallow}},
		"signed by another wallet":           {paid(1, "0x0000000000000000000000000000000000000002", opened, 9), nil, StableTransferOutcome{StableTransferRefuted, StableReasonWrongSender}},
		"mined before it was asked for":      {paid(1, want.From, opened.Add(-time.Second), 9), nil, StableTransferOutcome{StableTransferRefuted, StableReasonTooEarly}},
		"nothing matched, shallow":           {paid(0, want.From, opened, 2), noMatch, StableTransferOutcome{StableTransferPending, StableReasonShallow}},
		"nothing matched, deep":              {paid(0, want.From, opened, 5), noMatch, StableTransferOutcome{StableTransferRefuted, StableReasonNothingMoved}},
		"reverted":                           {nil, fmt.Errorf("%w: 0x1", ErrTransferReverted), StableTransferOutcome{StableTransferRefuted, StableReasonReverted}},
		"not mined or unreadable":            {nil, errors.New("verify: not found"), StableTransferOutcome{StableTransferPending, StableReasonUnreadable}},
	} {
		if got := ProveStableTransfer(tc.p, tc.err, want); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
	// No NotBefore: the time is not held against it.
	if got := ProveStableTransfer(paid(1, want.From, opened.Add(-time.Hour), 9), nil, StableTransferWant{From: want.From, MinConf: 5}); got.Verdict != StableTransferProven {
		t.Fatalf("zero NotBefore: %+v", got)
	}
}

// A reverted receipt is final for a name change at once, but a refund only
// acts on it once it is as deep as a payment must be.
func TestProveStableTransfer_RevertNeedsDepth(t *testing.T) {
	reverted := fmt.Errorf("%w: 0x1", ErrTransferReverted)
	at := func(conf uint64) *StablePayment { return &StablePayment{Credited: new(big.Int), Confirmations: conf} }
	for name, tc := range map[string]struct {
		p    *StablePayment
		want StableTransferWant
		out  StableTransferOutcome
	}{
		"off: shallow revert is refuted":      {at(1), StableTransferWant{MinConf: 5}, StableTransferOutcome{StableTransferRefuted, StableReasonReverted}},
		"off: no depth is refuted":            {nil, StableTransferWant{MinConf: 5}, StableTransferOutcome{StableTransferRefuted, StableReasonReverted}},
		"on: shallow revert is not yet final": {at(4), StableTransferWant{MinConf: 5, RevertNeedsDepth: true}, StableTransferOutcome{StableTransferPending, StableReasonShallow}},
		"on: deep revert is refuted":          {at(5), StableTransferWant{MinConf: 5, RevertNeedsDepth: true}, StableTransferOutcome{StableTransferRefuted, StableReasonReverted}},
		"on: unreadable depth is pending":     {nil, StableTransferWant{MinConf: 5, RevertNeedsDepth: true}, StableTransferOutcome{StableTransferPending, StableReasonUnreadable}},
	} {
		if got := ProveStableTransfer(tc.p, reverted, tc.want); got != tc.out {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.out)
		}
	}
}

// Verify reads how deep a reverted receipt is, so RevertNeedsDepth can act on it.
func TestStableTransferVerifier_RevertedReportsDepth(t *testing.T) {
	tx, from := signedFeeTx(t)
	v := NewStableTransferVerifier(
		map[string]ERC20TransferVerifier{"bsc": fakeSourcedVerifier{err: ErrTransferReverted}},
		map[string]FeeTxReader{"bsc": &fakeFeeTxReader{tx: tx, block: 200, head: 202}})
	p, err := v.Verify(context.Background(), "bsc", "0x1", "0xtoken", from, "0xto")
	if !errors.Is(err, ErrTransferReverted) || p == nil || p.Confirmations != 3 {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	// Depth unreadable (no reader tx/receipt): the revert is still reported.
	v = NewStableTransferVerifier(
		map[string]ERC20TransferVerifier{"bsc": fakeSourcedVerifier{err: ErrTransferReverted}},
		map[string]FeeTxReader{"bsc": &fakeFeeTxReader{tx: tx}})
	if _, err := v.Verify(context.Background(), "bsc", "0x1", "0xtoken", from, "0xto"); !errors.Is(err, ErrTransferReverted) {
		t.Fatalf("err=%v", err)
	}
}
