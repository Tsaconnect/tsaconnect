package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// gatedNode is a lane broadcaster with a balance and, optionally, a
// transaction lookup — what *blockchain.EVMClient gives a registry lane.
type gatedNode struct {
	*laneNode
	mu      sync.Mutex
	balance *big.Int
	known   map[string]bool // nil: no lookup
}

func (n *gatedNode) GetBalance(string) (*big.Int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return new(big.Int).Set(n.balance), nil
}

type lookupNode struct{ *gatedNode }

func (n lookupNode) TransactionKnown(_ context.Context, hash string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.known[strings.ToLower(hash)], nil
}

// pricedOn is a priced legacy tx (21k gas at 1 gwei, so its buffered fee is
// 4.2e13) at nonce on chain, sending value.
func pricedOn(chain int64, nonce uint64, value int64) func() ([]byte, error) {
	return func() ([]byte, error) {
		u, _ := unsignedOn(chain, nonce)
		return []byte(strings.Replace(strings.Replace(string(u), `"gasPrice":"1"`, `"gasPrice":"1000000000"`, 1),
			`"value":"0"`, `"value":"`+big.NewInt(value).String()+`"`, 1)), nil
	}
}

// Review item 1: an uncertain broadcast that never reached the node leaves a
// hole at its nonce. Steady sends from another role keep the floor busy, but
// the floor is aged from the hole's own send: once keyLaneGapGrace has passed
// and the node says it does not have that transaction, the next send reuses
// the nonce and the queue behind it can mine.
func TestKeyLane_UncertainBroadcastThatNeverLandedIsRecoveredWithinTheGrace(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &gatedNode{laneNode: &laneNode{id: 146}, balance: big.NewInt(1e18), known: map[string]bool{}}
	reg := NewSignerRegistry()
	release := reg.Lane("pm_release", key, lookupNode{node})
	sponsor := reg.Lane("gas_sponsor", key, lookupNode{node})
	clock := time.Unix(1_700_000_000, 0)
	release.now = func() time.Time { return clock }

	node.laneNode.err = errors.New("context deadline exceeded") // never reached the node
	if _, attempted, _ := release.Send(context.Background(), pricedOn(146, 5, 0)); !attempted {
		t.Fatal("a timeout is an uncertain broadcast")
	}
	node.laneNode.err = nil
	recovered := false
	for i := 1; i <= 12; i++ { // a send every 2 s from another role; the RPC still says 5
		clock = clock.Add(2 * time.Second)
		if _, _, err := sponsor.Send(context.Background(), pricedOn(146, 5, 0)); err != nil {
			t.Fatal(err)
		}
		if got := node.sent(); got[len(got)-1] == 5 {
			recovered = true
			if elapsed := time.Duration(i) * 2 * time.Second; elapsed > keyLaneGapGrace+2*time.Second {
				t.Fatalf("recovered after %v, want within the %v grace", elapsed, keyLaneGapGrace)
			}
			break
		}
	}
	if !recovered {
		t.Fatalf("nonces %v: the hole at 5 was never refilled", node.sent())
	}
}

// The node HAS the floor-defining transaction (it is only slow to count it):
// the floor holds past the grace.
func TestKeyLane_FloorHoldsWhileTheNodeKnowsTheTransaction(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &gatedNode{laneNode: &laneNode{id: 146}, balance: big.NewInt(1e18), known: map[string]bool{}}
	lane := NewSignerRegistry().Lane("r", key, lookupNode{node})
	clock := time.Unix(1_700_000_000, 0)
	lane.now = func() time.Time { return clock }
	hash, _, err := lane.Send(context.Background(), pricedOn(146, 5, 0))
	if err != nil {
		t.Fatal(err)
	}
	node.known[strings.ToLower(hash)] = true
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(context.Background(), pricedOn(146, 5, 0)); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); got[1] != 6 {
		t.Fatalf("nonces %v, want 6: the node holds 5", got)
	}
}

// Review item 2: the release key's lane keeps back what the pool's live
// slices still owe. A gas top-up on the same key that would dip under it is
// refused; the pool's own release (a ReservedSend) still goes.
func TestKeyLane_TopUpRefusedUnderThePoolReservationReleaseStillGoes(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	node := &gatedNode{laneNode: &laneNode{id: 56}, balance: big.NewInt(1e15)}
	release := NewChainReleaseSigner(reg, key, map[int64]ReleaseBroadcaster{56: node})
	kl := reg.Lane("gas_sponsor", key, node)
	owedSlices := uint64(1)
	kl.AddReservation("pool_releases", func(_ context.Context, price *big.Int) (*big.Int, error) {
		return feeNeed(owedSlices*poolReleaseCallGas, price, nil), nil // 1 × 300k × 1 gwei × 2 = 6e14
	})
	// A 5e14 top-up: 5e14 + 4.2e13 fee alone fits 1e15; with the 6e14 kept back it does not.
	if _, attempted, err := kl.Send(context.Background(), pricedOn(56, 0, 5e14)); !errors.Is(err, ErrRelayerUnderfunded) || attempted {
		t.Fatalf("top-up under the reservation: attempted=%v err=%v, want refused", attempted, err)
	}
	if _, err := release.Send(ReservedSend(context.Background()), 56, pricedOn(56, 0, 0)); err != nil {
		t.Fatalf("the reserved release itself must go: %v", err)
	}
	// A small top-up that leaves the reservation intact still goes.
	if _, _, err := kl.Send(context.Background(), pricedOn(56, 1, 1e14)); err != nil {
		t.Fatalf("a top-up above the reservation: %v", err)
	}
	// An unreadable reservation fails closed.
	kl.AddReservation("pool_releases", func(context.Context, *big.Int) (*big.Int, error) { return nil, errors.New("db down") })
	if _, attempted, err := kl.Send(context.Background(), pricedOn(56, 2, 1)); err == nil || attempted {
		t.Fatalf("an unreadable reservation must refuse: attempted=%v err=%v", attempted, err)
	}
}

// Review item 5: the lane's one gate fails closed on a tx it cannot price.
func TestKeyLane_FeeGateFailsClosedOnAnUnpricedTx(t *testing.T) {
	key, _ := crypto.GenerateKey()
	node := &gatedNode{laneNode: &laneNode{id: 56}, balance: big.NewInt(1e18)}
	lane := NewSignerRegistry().Lane("r", key, node)
	_, attempted, err := lane.Send(context.Background(), func() ([]byte, error) {
		return []byte(`{"nonce":0,"gasPrice":"","gasLimit":21000,"to":"0x000000000000000000000000000000000000dEaD","value":"0","chainId":"56"}`), nil
	})
	if attempted || !errors.Is(err, ErrTxNotBroadcast) || len(node.sent()) != 0 {
		t.Fatalf("attempted=%v err=%v sent=%v, want refused before signing", attempted, err, node.sent())
	}
}

// Owner decision B: the pool's treasury lane keeps back the delivery every
// earlier pool order is owed. Two orders whose owed treasury calls together
// exceed the balance: the second open is refused; the first order's owed
// delivery itself still goes.
func TestPoolOpen_TreasuryKeepsBackWhatEarlierOrdersAreOwed(t *testing.T) {
	r := newPoolOpenRig(t)
	r.addLP(t, 50)
	r.addLP(t, 50)
	if _, _, err := r.open(t, "5000"); err != nil {
		t.Fatalf("first open: %v", err)
	}
	tr := r.desk.c.Treasury
	lane := tr.Lane("bsc")
	sc := lane.Chain.(*fakeSponsorChain)
	price := big.NewInt(1_000_000_000)
	slices := uint64(CurrentPoolSettings().SliceCap + 1)
	// Enough for the second order's slice opens alone, not for those plus the
	// first order's owed delivery.
	sc.balances[strings.ToLower(lane.Signer.Address())] = feeNeed(treasuryEscrowCallGas*slices, price, nil)
	tr.gasMu.Lock()
	tr.gasCache = map[string]gasReading{}
	tr.gasMu.Unlock()
	if _, _, err := r.open(t, "5000"); !errors.Is(err, ErrPoolClosed) || !strings.Contains(err.Error(), "treasury cannot pay") {
		t.Fatalf("second open with the first's delivery owed = %v, want ErrPoolClosed (treasury cannot pay)", err)
	}
	// The owed delivery itself spends its reservation: a balance covering its
	// own fee, but not fee + reservation, still sends it.
	sc.balances[strings.ToLower(lane.Signer.Address())] = feeNeed(treasuryERC20Gas, price, nil)
	lane.Signer = &fakeRelaySigner{addr: lane.Signer.Address(), txHash: "0xdelivered"} // the rig's chain only signs escrow calls
	if _, _, err := tr.TransferToken(context.Background(), lane, "USDT", treasuryTestRecipient, big.NewInt(1)); err != nil {
		t.Fatalf("the owed delivery must go: %v", err)
	}
}

// Review item 6: a quote reuses the release-gas reading for a few seconds and,
// when a read fails (a 429), falls back to a recent one rather than closing.
func TestPoolGates_ReleaseGasReadingIsReusedAndStaleOnError(t *testing.T) {
	c := healthyPoolChain()
	g := NewPoolChainGates(healthyGates(c))
	if !g.ReleaseGasOK(context.Background(), 1) {
		t.Fatal("setup")
	}
	c.balanceErr = errors.New("429 Too Many Requests")
	if !g.ReleaseGasOK(context.Background(), 1) {
		t.Fatal("a fresh reading must be reused")
	}
	g.gasMu.Lock()
	g.gasRead.at = g.gasRead.at.Add(-10 * time.Second) // past fresh, inside stale
	g.gasMu.Unlock()
	if !g.ReleaseGasOK(context.Background(), 1) {
		t.Fatal("a failed read must fall back to a recent reading")
	}
	g.gasMu.Lock()
	g.gasRead.at = g.gasRead.at.Add(-time.Minute)
	g.gasMu.Unlock()
	if g.ReleaseGasOK(context.Background(), 1) {
		t.Fatal("no recent reading and a failed read must close")
	}
}
