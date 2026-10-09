package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

const escrowCallTarget = "0x4C7199285A2c84af77594ab172208637f9872717"

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
		escrowCallBuild(treasuryTestToken, 1), func(uint64) error { hooked = true; return nil })
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
		func(n uint64) error {
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
	_, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 3),
		func(uint64) error { return errors.New("db down") })
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
	record := func(n uint64) error { got = append(got, n); return nil }
	stale := escrowCallBuild(escrowCallTarget, 7) // the RPC answers 7 both times
	for i := 0; i < 3; i++ {
		if _, attempted, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, stale, record); err != nil || !attempted {
			t.Fatalf("call %d: err=%v attempted=%v", i, err, attempted)
		}
	}
	if len(got) != 3 || got[0] != 7 || got[1] != 8 || got[2] != 9 {
		t.Fatalf("nonces = %v, want 7 8 9", got)
	}
	// A refusal that provably never broadcast hands exactly that nonce back
	// (stepBackNonce, re-review N3) — the floor is not cleared.
	lane.Signer = &orderSigner{log: &log, addr: sponsorTestSigner, err: ErrTxNotBroadcast}
	if _, attempted, _ := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 10), record); attempted {
		t.Fatal("ErrTxNotBroadcast must report not attempted")
	}
	lane.Signer = &orderSigner{log: &log, addr: sponsorTestSigner}
	if _, _, err := tr.CallEscrowRecorded(context.Background(), lane, escrowCallTarget, escrowCallBuild(escrowCallTarget, 10), record); err != nil || got[len(got)-1] != 10 {
		t.Fatalf("after a non-broadcast the nonce is reused: %v %v", got, err)
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
