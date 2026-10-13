package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

const escrowCallTarget = "0x2B19e2e2194880A1a8AF9135923e3AeE0bD1adE5"

// orderSigner records broadcasts in one ordered log shared with the hook.
type orderSigner struct {
	mu   sync.Mutex
	log  *[]string
	addr string
	err  error
}

func (o *orderSigner) Address() string { return o.addr }
func (o *orderSigner) SignAndBroadcast(_ context.Context, _ []byte) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	*o.log = append(*o.log, "broadcast")
	if o.err != nil {
		return "", o.err
	}
	return "0x" + strings.Repeat("ab", 32), nil
}

// signRaw / broadcastRaw make orderSigner a raw signer: the key lane runs its
// real outbox (hook inside the allocation transaction, then the broadcast).
func (o *orderSigner) signRaw(unsigned []byte) ([]byte, string, error) {
	return unsigned, crypto.Keccak256Hash(unsigned).Hex(), nil
}
func (o *orderSigner) broadcastRaw(raw []byte, hash string) (string, error) {
	if _, err := o.SignAndBroadcast(context.Background(), raw); err != nil {
		return "", err
	}
	return hash, nil
}

// rawRelaySigner is fakeRelaySigner as a raw signer (outbox ordering tests).
type rawRelaySigner struct{ *fakeRelaySigner }

func (r rawRelaySigner) signRaw(unsigned []byte) ([]byte, string, error) {
	return unsigned, crypto.Keccak256Hash(unsigned).Hex(), nil
}
func (r rawRelaySigner) broadcastRaw(raw []byte, hash string) (string, error) {
	if _, err := r.SignAndBroadcast(context.Background(), raw); err != nil {
		return "", err
	}
	return hash, nil
}

func escrowCallBuild(to string, nonce uint64) func() ([]byte, error) {
	return func() ([]byte, error) {
		return json.Marshal(blockchain.UnsignedTx{Nonce: nonce, To: to, Value: "0", GasPrice: "1", GasLimit: 200_000, ChainID: "8453", Data: "deadbeef"})
	}
}

// S21 / Plan A: an escrow call goes only to an address on EscrowCalls.
func TestCallEscrowRecorded_RefusesTargetOutsideEscrowCalls(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	built := false
	build := func() ([]byte, error) { built = true; return escrowCallBuild(escrowCallTarget, 1)() }
	if _, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, build, nil); !errors.Is(err, ErrTreasuryInvalidRoute) || attempted {
		t.Fatalf("no EscrowCalls: err=%v attempted=%v", err, attempted)
	}
	// A LiFi router is not an escrow, even though the lane may call it as a route.
	if _, _, err := tr.CallEscrowRecorded(context.Background(), lane, treasuryTestRouter, build, nil); !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("router as escrow: %v", err)
	}
	if built || signer.calls() != 0 {
		t.Fatalf("built=%v broadcasts=%d, want nothing", built, signer.calls())
	}
}

// The prepared transaction itself must target the escrow (a wrong build never broadcasts).
func TestCallEscrowRecorded_RefusesTxToAnotherAddress(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	hooked := false
	_, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget,
		escrowCallBuild(treasuryTestToken, 1), func(context.Context, uint64, string) error { hooked = true; return nil })
	if !errors.Is(err, ErrTreasuryInvalidRoute) || attempted || hooked || signer.calls() != 0 {
		t.Fatalf("err=%v attempted=%v hooked=%v broadcasts=%d", err, attempted, hooked, signer.calls())
	}
}

// S43: the nonce is recorded before anything is broadcast.
func TestCallEscrowRecorded_RecordsNonceBeforeBroadcast(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	var log []string
	lane.Signer = &orderSigner{log: &log, addr: sponsorTestSigner}
	hash, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 42),
		func(_ context.Context, n uint64, _ string) error {
			log = append(log, "nonce")
			if n != 42 {
				t.Errorf("nonce %d", n)
			}
			return nil
		})
	if err != nil || !attempted || hash == "" || len(log) != 2 || log[0] != "nonce" || log[1] != "broadcast" {
		t.Fatalf("err=%v attempted=%v hash=%q order=%v", err, attempted, hash, log)
	}
}

func TestCallEscrowRecorded_HookFailureBroadcastsNothing(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	lane.Signer = rawRelaySigner{signer}
	_, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 3),
		func(context.Context, uint64, string) error { return errors.New("db down") })
	if err == nil || attempted || signer.calls() != 0 {
		t.Fatalf("err=%v attempted=%v broadcasts=%d", err, attempted, signer.calls())
	}
}

func TestCallEscrowRecorded_KilledLaneRefuses(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	tr.SetKillSwitch(true)
	if _, _, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 1), nil); !errors.Is(err, ErrTreasuryKilled) || signer.calls() != 0 {
		t.Fatalf("killed: %v", err)
	}
}

// Review M3: a prepared call must name this lane's chain. An empty chain id is
// what a client-less build returns under ENV=development (nonce 0, no chain),
// which prod runs.
func TestCallEscrowRecorded_RequiresTheLanesChainID(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	noChain := func() ([]byte, error) {
		return json.Marshal(blockchain.UnsignedTx{Nonce: 0, To: escrowCallTarget, Value: "0", GasPrice: "1", GasLimit: 200_000, Data: "deadbeef"})
	}
	if _, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, noChain, nil); !errors.Is(err, ErrTreasuryInvalidRoute) || attempted || signer.calls() != 0 {
		t.Fatalf("empty chain id: err=%v attempted=%v broadcasts=%d", err, attempted, signer.calls())
	}
}

// Review I2: a burst's slices are built back to back, and the escrow
// adapter's nonce is the RPC's pending view, which may not yet include the
// slice just sent. The lane's own floor (held under its send lock) gives the
// second call the next nonce, never a replacement of the first.
func TestCallEscrowRecorded_BackToBackSlicesGetDistinctNonces(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	var log []string
	lane.Signer = &orderSigner{log: &log, addr: sponsorTestSigner}
	var got []uint64
	record := func(_ context.Context, n uint64, _ string) error { got = append(got, n); return nil }
	stale := escrowCallBuild(escrowCallTarget, 7) // the RPC answers 7 both times
	for i := 0; i < 3; i++ {
		if _, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, stale, record); err != nil || !attempted {
			t.Fatalf("call %d: err=%v attempted=%v", i, err, attempted)
		}
	}
	if len(got) != 3 || got[0] != 7 || got[1] != 8 || got[2] != 9 {
		t.Fatalf("nonces = %v, want 7 8 9", got)
	}
	// A refusal of a committed call keeps it in flight and does NOT hand its
	// nonce to another call (outbox rule C): the next call takes the next nonce.
	lane.Signer = &orderSigner{log: &log, addr: sponsorTestSigner, err: ErrTxNotBroadcast}
	if _, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 10), record); !attempted || !errors.Is(err, ErrPendingBroadcast) {
		t.Fatalf("a refused committed call: attempted=%v err=%v, want in flight", attempted, err)
	}
	lane.Signer = &orderSigner{log: &log, addr: sponsorTestSigner}
	if _, _, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 10), record); err != nil || got[len(got)-1] != 11 {
		t.Fatalf("after a non-broadcast: %v %v, want 11 (10 is never reused by another call)", got, err)
	}
}

// S21 the other way round: a LiFi route can never call the escrow.
func TestExecuteRoute_CannotCallTheEscrow(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	r := validRoute()
	r.To, r.ApprovalAddress = escrowCallTarget, ""
	if _, _, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(1), r); !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("route to the escrow: %v", err)
	}
}

// nonceBroadcaster records the nonce of every raw transaction a real local
// signer hands it, and answers with err.
type nonceBroadcaster struct {
	nonces []uint64
	err    error
}

func (b *nonceBroadcaster) ChainID() *big.Int { return big.NewInt(8453) }
func (b *nonceBroadcaster) SendRawTransaction(raw []byte) (string, error) {
	var tx types.Transaction
	if err := rlp.DecodeBytes(raw, &tx); err != nil {
		return "", err
	}
	b.nonces = append(b.nonces, tx.Nonce())
	if b.err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", b.err)
	}
	return tx.Hash().Hex(), nil
}

// treasuryLaneOnNode puts a real local signer on the lane, over a node that
// answers with bc.err, and an RPC whose pending view stays at 7.
func treasuryLaneOnNode(t *testing.T) (*Treasury, *TreasuryLane, *nonceBroadcaster) {
	t.Helper()
	tr, lane, native, _ := newTestTreasuryWithFakes(t)
	lane.EscrowCalls = []common.Address{common.HexToAddress(escrowCallTarget)}
	key, _ := crypto.GenerateKey()
	bc := &nonceBroadcaster{}
	lane.Signer = NewLocalKeyReleaseSigner(key, bc)
	lane.Native = signableTransfers{native}
	return tr, lane, bc
}

// signableTransfers prepares a transfer a real signer accepts, at the RPC's
// pending nonce 7.
type signableTransfers struct{ *fakeSponsorNative }

func (signableTransfers) PrepareERC20Transfer(token, _, _ string, _ *big.Int) ([]byte, error) {
	return json.Marshal(blockchain.UnsignedTx{Nonce: 7, To: token, Value: "0", GasPrice: "1", GasLimit: 60_000, ChainID: "8453", Data: "a9059cbb"})
}

func (b *nonceBroadcaster) last(t *testing.T) uint64 {
	t.Helper()
	if len(b.nonces) == 0 {
		t.Fatal("nothing broadcast")
	}
	return b.nonces[len(b.nonces)-1]
}

// Pre-merge review M-1 (a): the floor holds for every send on the lane. A
// transfer right after a pool slice, while the RPC's pending view still lags
// at the slice's nonce, signs the next nonce — never a replacement of the
// slice, which would then never land.
func TestTreasury_TransferAfterASliceNeverReusesItsNonce(t *testing.T) {
	tr, lane, bc := treasuryLaneOnNode(t)
	ctx := context.Background()
	if _, _, err := tr.CallEscrowRecorded(ctx, lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 7), nil); err != nil {
		t.Fatalf("slice: %v", err)
	}
	if _, _, err := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if len(bc.nonces) != 2 || bc.nonces[0] != 7 || bc.nonces[1] != 8 {
		t.Fatalf("nonces %v, want the slice at 7 and the transfer at 8", bc.nonces)
	}
}

// M-1 (b), outbox rule C: a send the node definitely refused (underpriced) is
// committed and in flight (the lane re-sends the same call), and its nonce is
// never given to another call — the next send takes the next nonce.
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

// M-1 (c): an uncertain broadcast (a timeout: the node may hold it) keeps
// its nonce taken — the next send signs the one after it.
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
