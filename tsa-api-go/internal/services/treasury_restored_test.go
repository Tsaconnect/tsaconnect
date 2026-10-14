package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
)

// Restored from treasury_escrow_call_test.go (deleted with the slice pool in
// 4cf9bb39). TransferTokenRecorded is still the company wallet's send path.

// Outbox rule C: a send the node definitely refused (underpriced) is committed
// and in flight (the lane re-sends the same call), and its nonce is never given
// to another call — the next send takes the next nonce.
func TestTreasury_DefiniteRejectionNeverReusesTheNonce(t *testing.T) {
	tr, lane, bc := treasuryLaneOnNode(t)
	ctx := context.Background()
	bc.err = errors.New("transaction underpriced")
	if hash, attempted, err := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil); !attempted || hash == "" || !errors.Is(err, ErrPendingBroadcast) {
		t.Fatalf("underpriced: hash=%q attempted=%v err=%v, want in flight", hash, attempted, err)
	}
	bc.err = nil
	if _, _, err := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil); err != nil || bc.last(t) != 8 {
		t.Fatalf("next send: nonce %d err=%v, want 8", bc.last(t), err)
	}
}

// An uncertain broadcast (a timeout: the node may hold it) keeps its nonce
// taken — the next send signs the one after it.
func TestTreasury_UncertainBroadcastKeepsTheFloor(t *testing.T) {
	tr, lane, bc := treasuryLaneOnNode(t)
	ctx := context.Background()
	bc.err = errors.New("context deadline exceeded")
	if _, attempted, _ := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil); !attempted {
		t.Fatal("a timeout must stay attempted")
	}
	bc.err = nil
	if _, _, err := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil); err != nil || bc.last(t) != 8 {
		t.Fatalf("next send: nonce %d err=%v, want 8", bc.last(t), err)
	}
}
