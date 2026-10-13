package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// laneNode is a broadcaster that records every nonce it is handed and answers
// with err; its pending view (what a build reads) is fixed by the test.
type laneNode struct {
	mu     sync.Mutex
	id     int64
	err    error
	nonces []uint64
}

func (n *laneNode) ChainID() *big.Int { return big.NewInt(n.id) }
func (n *laneNode) SendRawTransaction(raw []byte) (string, error) {
	var tx types.Transaction
	if err := rlp.DecodeBytes(raw, &tx); err != nil {
		return "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nonces = append(n.nonces, tx.Nonce())
	if n.err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", n.err)
	}
	return tx.Hash().Hex(), nil
}
func (n *laneNode) sent() []uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]uint64(nil), n.nonces...)
}

func TestSignerRegistry_SameKeyAndChainShareOneLane(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	a := reg.Lane("pm_release", key, &laneNode{id: 146})
	b := reg.Lane("sonic_gas", key, &laneNode{id: 146}) // another role's own client
	if a == nil || a != b {
		t.Fatalf("two roles on one key and chain got lanes %p and %p, want one", a, b)
	}
	other, _ := crypto.GenerateKey()
	if c := reg.Lane("it_operator", other, &laneNode{id: 146}); c == a {
		t.Fatal("a different key must get its own lane")
	}
	sum := reg.Summary()
	if len(sum) != 2 {
		t.Fatalf("summary = %+v, want two addresses", sum)
	}
	for _, s := range sum {
		if s.Address == crypto.PubkeyToAddress(key.PublicKey).Hex() {
			if len(s.Roles) != 2 || s.Roles[0] != "pm_release" || s.Roles[1] != "sonic_gas" || len(s.Chains) != 1 || s.Chains[0] != 146 {
				t.Fatalf("summary for the shared key = %+v", s)
			}
		}
	}
}

// Keyed by chain id, never chain key: BSC testnet (97) and mainnet (56) on one
// key are two nonce sequences.
func TestSignerRegistry_TestnetAndMainnetAreSeparateLanes(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	if reg.Lane("pm_release", key, &laneNode{id: 97}) == reg.Lane("ramp_sponsor", key, &laneNode{id: 56}) {
		t.Fatal("chain 97 and chain 56 must not share a lane")
	}
	var nilReg *SignerRegistry
	if l := nilReg.Lane("x", key, &laneNode{id: 56}); l == nil || l == nilReg.Lane("x", key, &laneNode{id: 56}) {
		t.Fatal("a nil registry hands out a fresh private lane each time")
	}
}

// Two roles on one key and chain, building concurrently against an RPC whose
// pending view never moves, still sign consecutive nonces.
func TestKeyLane_ConcurrentRolesOnALaggingRPCGetConsecutiveNonces(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	node := &laneNode{id: 146}
	release := NewChainReleaseSigner(reg, key, map[int64]ReleaseBroadcaster{146: node})
	sponsor := reg.Lane("sonic_gas", key, node)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			build := func() ([]byte, error) { time.Sleep(5 * time.Millisecond); return unsignedOn(146, 4) }
			var err error
			if i%2 == 0 {
				_, err = release.Send(context.Background(), 146, build)
			} else {
				_, _, err = sponsor.Send(context.Background(), build)
			}
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for _, n := range node.sent() {
		seen[n] = true
	}
	for n := uint64(4); n < 10; n++ {
		if !seen[n] {
			t.Fatalf("nonces sent = %v, want 4..9 each once", node.sent())
		}
	}
}

func TestKeyLane_DefiniteRejectionHandsTheNonceBack(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 56, err: errors.New("transaction underpriced")}
	lane := NewSignerRegistry().Lane("r", key, node)
	build := func() ([]byte, error) { return unsignedOn(56, 3) }
	if _, attempted, err := lane.Send(context.Background(), build); attempted || !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("attempted=%v err=%v, want a definite refusal", attempted, err)
	}
	node.err = nil
	if _, _, err := lane.Send(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); got[len(got)-1] != 3 {
		t.Fatalf("nonces %v, want 3 reused after the refusal", got)
	}
}

func TestKeyLane_UncertainBroadcastKeepsTheFloor(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 56, err: errors.New("context deadline exceeded")}
	lane := NewSignerRegistry().Lane("r", key, node)
	build := func() ([]byte, error) { return unsignedOn(56, 3) }
	if _, attempted, _ := lane.Send(context.Background(), build); !attempted {
		t.Fatal("a timeout must stay attempted")
	}
	node.err = nil
	if _, _, err := lane.Send(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); got[len(got)-1] != 4 {
		t.Fatalf("nonces %v, want 4 after an uncertain 3", got)
	}
}

func TestKeyLane_FloorExpiresAfterTTL(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 56}
	lane := NewSignerRegistry().Lane("r", key, node)
	now := time.Unix(1_700_000_000, 0)
	lane.now = func() time.Time { return now }
	build := func() ([]byte, error) { return unsignedOn(56, 3) }
	_, _, _ = lane.Send(context.Background(), build)
	now = now.Add(keyLaneNonceFloorTTL + time.Second)
	_, _, _ = lane.Send(context.Background(), build)
	if got := node.sent(); len(got) != 2 || got[1] != 3 {
		t.Fatalf("nonces %v, want the chain's 3 again once the floor expired", got)
	}
}

func TestKeyLane_RecordHookRunsBeforeBroadcastAndAFailureSendsNothing(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 56}
	lane := NewSignerRegistry().Lane("r", key, node)
	build := func() ([]byte, error) { return unsignedOn(56, 3) }
	_, attempted, err := lane.SendRecorded(context.Background(), build, func(uint64) error { return errors.New("db down") })
	if attempted || err == nil || len(node.sent()) != 0 {
		t.Fatalf("attempted=%v err=%v sent=%v, want nothing broadcast", attempted, err, node.sent())
	}
	var recorded uint64
	if _, _, err := lane.SendRecorded(context.Background(), build, func(n uint64) error { recorded = n; return nil }); err != nil {
		t.Fatal(err)
	}
	if recorded != 3 || node.sent()[0] != 3 {
		t.Fatalf("recorded %d sent %v, want the hook's nonce (3, handed back) to be the one signed", recorded, node.sent())
	}
}

func TestKeyLane_RefusesATxBuiltForAnotherChain(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 56}
	lane := NewSignerRegistry().Lane("r", key, node)
	_, attempted, err := lane.Send(context.Background(), func() ([]byte, error) { return unsignedOn(97, 0) })
	if attempted || !errors.Is(err, ErrTxNotBroadcast) || len(node.sent()) != 0 {
		t.Fatalf("attempted=%v err=%v sent=%v", attempted, err, node.sent())
	}
}

// Two treasuries on one key and chain (a pool-only treasury and the conversion
// treasury, or the company wallet once it moves onto the treasury key) share
// one KeyLane: a pool slice on one and a transfer on the other, while the
// RPC's pending view lags at 7, sign 7 then 8 — never one nonce twice.
func TestKeyLane_TwoTreasuriesOnOneKeyNeverReuseANonce(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 8453}
	shared := NewSignerRegistry().Lane("treasury", key, node)
	onShared := func() (*Treasury, *TreasuryLane) {
		tr, lane, native, _ := newTestTreasuryWithFakes(t)
		lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
		lane.Signer, lane.KeyLane = shared.Signer(), shared
		lane.Native = signableTransfers{native}
		return tr, lane
	}
	pool, poolLane := onShared()
	conv, convLane := onShared()
	ctx := context.Background()
	if _, _, err := pool.CallEscrowRecorded(ctx, poolLane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 7), nil); err != nil {
		t.Fatalf("slice: %v", err)
	}
	if _, _, err := conv.TransferTokenRecorded(ctx, convLane, "USDC", testRoutedEVM, big.NewInt(1), nil); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if got := node.sent(); len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("nonces %v, want 7 then 8", got)
	}
}

// A gas-sponsor lane and a ramp lane on the same key and chain share the
// lock: a sponsor top-up sent right after a release (both reading a lagging
// pending nonce) takes the next nonce.
func TestKeyLane_SponsorLaneSharesTheReleaseKeysLane(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	node := &laneNode{id: 8453}
	release := NewChainReleaseSigner(reg, key, map[int64]ReleaseBroadcaster{8453: node})
	kl := reg.Lane("gas_sponsor", key, node)
	sponsor := &SponsorLane{ChainKey: "base", Signer: kl.Signer(), KeyLane: kl}
	if _, err := release.Send(context.Background(), 8453, func() ([]byte, error) { return unsignedOn(8453, 2) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sponsor.send(context.Background(), func() ([]byte, error) { return unsignedOn(8453, 2) }); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("nonces %v, want 2 then 3", got)
	}
}
