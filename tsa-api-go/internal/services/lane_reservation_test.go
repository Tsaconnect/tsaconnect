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

// gatedNode is a lane broadcaster with a balance — what *blockchain.EVMClient
// gives a registry lane. hang, when set, makes the first balance read block
// until its ctx ends.
type gatedNode struct {
	*laneNode
	mu      sync.Mutex
	balance *big.Int
	known   map[string]bool
	lookErr error
	hang    chan struct{}
}

func (n *gatedNode) NativeBalance(ctx context.Context, _ string) (*big.Int, error) {
	n.mu.Lock()
	hang := n.hang
	n.hang = nil
	n.mu.Unlock()
	if hang != nil {
		close(hang)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return new(big.Int).Set(n.balance), nil
}

// lookupNode adds eth_getTransactionByHash.
type lookupNode struct{ *gatedNode }

func (n lookupNode) TransactionKnown(_ context.Context, hash string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lookErr != nil {
		return false, n.lookErr
	}
	return n.known[strings.ToLower(hash)], nil
}

func (n *gatedNode) markKnown(hash string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.known[strings.ToLower(hash)] = true
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

func lookupLane(t *testing.T) (*KeyLane, *gatedNode, *time.Time) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	node := &gatedNode{laneNode: &laneNode{id: 146}, balance: big.NewInt(1e18), known: map[string]bool{}}
	lane := NewSignerRegistry().Lane("pm_release", key, lookupNode{node})
	clock := time.Unix(1_700_000_000, 0)
	lane.now = func() time.Time { return clock }
	return lane, node, &clock
}

// Re-review: tx 5 was dropped (never reached the node) while 6 and 7 queue
// behind it. Once the grace has passed and the node says it lacks 5, the lane
// RE-BROADCASTS 5's exact signed bytes — 6 and 7 are untouched, and no other
// transaction is ever signed at 5.
func TestKeyLane_DroppedTxIsRebroadcastIdenticallyNeverResigned(t *testing.T) {
	lane, node, clock := lookupLane(t)
	ctx := context.Background()
	node.laneNode.err = errors.New("context deadline exceeded") // 5 never reached the node
	if _, attempted, _ := lane.Send(ctx, pricedOn(146, 5, 0)); !attempted {
		t.Fatal("a timeout is an uncertain broadcast")
	}
	raw5 := append([]byte(nil), node.raws[0]...)
	node.laneNode.err = nil
	for i := 0; i < 2; i++ { // 6 and 7, the RPC still reporting 5
		h, _, err := lane.Send(ctx, pricedOn(146, 5, 0))
		if err != nil {
			t.Fatal(err)
		}
		node.markKnown(h)
	}
	*clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(ctx, pricedOn(146, 5, 0)); err != nil {
		t.Fatal(err)
	}
	got := node.sent()
	// 5, 6, 7, then the re-broadcast of 5, then the new send at 8.
	if len(got) != 5 || got[0] != 5 || got[1] != 6 || got[2] != 7 || got[3] != 5 || got[4] != 8 {
		t.Fatalf("nonces %v, want 5 6 7 then 5 re-broadcast and the new send at 8", got)
	}
	if string(node.raws[3]) != string(raw5) {
		t.Fatal("the re-broadcast must be the exact bytes first signed at 5")
	}
}

// A signer that cannot hand over its bytes (here a fake; in production a
// record lost to a restart): the lane holds and alerts, never reusing 5, and
// an operator's ClearHold lets it go on at the floor.
func TestKeyLane_MissingBytesHoldsTheLaneNoReuse(t *testing.T) {
	var log []string
	clock := time.Unix(1_700_000_000, 0)
	lane := soloLane(nonRawSigner{&orderSigner{log: &log, addr: sponsorTestSigner}}, nil)
	lane.now = func() time.Time { return clock }
	ctx := context.Background()
	build := func() ([]byte, error) { u, _ := unsignedOn(0, 5); return u, nil }
	if _, _, err := lane.Send(ctx, build); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, attempted, err := lane.Send(ctx, build); !errors.Is(err, ErrKeyLaneHeld) || attempted {
		t.Fatalf("attempted=%v err=%v, want the lane held", attempted, err)
	}
	if _, _, err := lane.Send(ctx, build); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("still held: err=%v", err)
	}
	if len(log) != 1 {
		t.Fatalf("broadcasts = %d, want only the first", len(log))
	}
	_ = lane.ClearHold(context.Background())
	var pinned uint64
	if _, _, err := lane.SendRecorded(ctx, build, func(_ context.Context, n uint64, _ string) error { pinned = n; return nil }); err != nil || pinned != 6 {
		t.Fatalf("after the hold: nonce %d err=%v, want 6 (never 5 again)", pinned, err)
	}
}

// nonRawSigner hides a fake's raw signing: a signer that can only sign and
// broadcast together, so the lane never holds its bytes.
type nonRawSigner struct{ o *orderSigner }

func (n nonRawSigner) Address() string { return n.o.Address() }
func (n nonRawSigner) SignAndBroadcast(ctx context.Context, u []byte) (string, error) {
	return n.o.SignAndBroadcast(ctx, u)
}

// A lagging, load-balanced RPC reporting an old pending nonce after the lane
// sat idle: its transactions are known, so the floor is unchanged and every
// record kept.
func TestKeyLane_LaggingRPCAfterIdleLeavesFloorAndRecords(t *testing.T) {
	lane, node, clock := lookupLane(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		h, _, err := lane.Send(ctx, pricedOn(146, 5, 0))
		if err != nil {
			t.Fatal(err)
		}
		node.markKnown(h)
	}
	*clock = clock.Add(time.Minute)
	if _, _, err := lane.Send(ctx, pricedOn(146, 5, 0)); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); len(got) != 3 || got[2] != 7 {
		t.Fatalf("nonces %v, want 5 6 7", got)
	}
}

// The node's lookup answers 429: the lane cannot tell, so it neither reuses
// nor re-broadcasts — the next send takes the floor.
func TestKeyLane_LookupErrorNeverReuses(t *testing.T) {
	lane, node, clock := lookupLane(t)
	ctx := context.Background()
	node.laneNode.err = errors.New("context deadline exceeded")
	_, _, _ = lane.Send(ctx, pricedOn(146, 5, 0))
	node.laneNode.err = nil
	node.lookErr = errors.New("429 Too Many Requests")
	*clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(ctx, pricedOn(146, 5, 0)); err != nil {
		t.Fatal(err)
	}
	if got := node.sent(); len(got) != 2 || got[1] != 6 {
		t.Fatalf("nonces %v, want 5 then 6: no reuse, no re-broadcast", got)
	}
}

// Re-review item 6: the fee gate reads the balance before taking the lane's
// lock, on the caller's ctx — a hung balance read holds up only its own send.
func TestKeyLane_HungBalanceReadDoesNotBlockAnotherRole(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reg := NewSignerRegistry()
	hang := make(chan struct{})
	node := &gatedNode{laneNode: &laneNode{id: 56}, balance: big.NewInt(1e18), hang: hang}
	release := reg.Lane("pm_release", key, node)
	sponsor := reg.Lane("gas_sponsor", key, node)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, err := release.Send(ctx, pricedOn(56, 0, 0))
		done <- err
	}()
	<-hang // the release's balance read is now stuck
	start := time.Now()
	if _, _, err := sponsor.Send(context.Background(), pricedOn(56, 0, 0)); err != nil {
		t.Fatalf("the other role's send: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("the other role waited %v behind a hung balance read", time.Since(start))
	}
	if err := <-done; err == nil || !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("the hung send: err=%v, want refused (not broadcast) when its ctx ends", err)
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
	kl.AddReservation("pool_releases", func(context.Context) (uint64, error) {
		return owedSlices * poolReleaseCallGas, nil // priced at the send: 1 × 300k × 1 gwei × 2 = 6e14
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
	kl.AddReservation("pool_releases", func(context.Context) (uint64, error) { return 0, errors.New("db down") })
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
	// The owed delivery itself (an owed call site marks ReservedSend) spends
	// its reservation: a balance covering its own fee, but not fee +
	// reservation, still sends it — and the same transfer from a call site
	// that is NOT owed (a name-change refund) is refused by the reservation.
	// (+1e6 wei: the slice opens already broadcast and not yet mined.)
	sc.balances[strings.ToLower(lane.Signer.Address())] = new(big.Int).Add(feeNeed(treasuryERC20Gas, price, nil), big.NewInt(1_000_000))
	lane.Signer = &fakeRelaySigner{addr: lane.Signer.Address(), txHash: "0xdelivered"} // the rig's chain only signs escrow calls
	if _, _, err := tr.TransferToken(context.Background(), lane, "USDT", treasuryTestRecipient, big.NewInt(1)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("a non-owed transfer under the reservation = %v, want refused", err)
	}
	if _, _, err := tr.TransferToken(ReservedSend(context.Background()), lane, "USDT", treasuryTestRecipient, big.NewInt(1)); err != nil {
		t.Fatalf("the owed delivery must go: %v", err)
	}
}

// Review item 6: a quote reuses the release-gas reading for a few seconds and,
// when a read fails (a 429), falls back to a recent one rather than closing.
func TestPoolGates_ReleaseGasReadingIsReusedAndStaleOnError(t *testing.T) {
	c := healthyPoolChain()
	g := NewPoolChainGates(healthyGates(c))
	if !g.ReleaseGasOK(context.Background(), 1, false) {
		t.Fatal("setup")
	}
	c.balanceErr = errors.New("429 Too Many Requests")
	if !g.ReleaseGasOK(context.Background(), 1, false) {
		t.Fatal("a fresh reading must be reused")
	}
	g.gasMu.Lock()
	g.gasRead.at = g.gasRead.at.Add(-10 * time.Second) // past fresh, inside stale
	g.gasMu.Unlock()
	if !g.ReleaseGasOK(context.Background(), 1, false) {
		t.Fatal("a failed read must fall back to a recent reading")
	}
	g.gasMu.Lock()
	g.gasRead.at = g.gasRead.at.Add(-time.Minute)
	g.gasMu.Unlock()
	if g.ReleaseGasOK(context.Background(), 1, false) {
		t.Fatal("no recent reading and a failed read must close")
	}
}

// Re-review item 1: a lane funded for exactly N owed deliveries sends all N
// when each is an owed send (its call site marks ReservedSend for both the
// pre-check and the send); the reservation would refuse the first otherwise.
func TestTreasury_ExactlyFundedForNOwedDeliveriesSendsAllN(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	const n = 3
	lane.KeyLane.AddReservation("owed", func(context.Context) (uint64, error) { return n * treasuryERC20Gas, nil })
	price := big.NewInt(1_000_000_000)
	per := feeNeed(treasuryERC20Gas, price, nil)
	// Nothing mines in between: each send also counts the earlier ones'
	// unmined spend against the same balance.
	withTreasuryBalance(lane, new(big.Int).Mul(per, big.NewInt(n)))
	for i := 0; i < n; i++ {
		if _, _, err := tr.TransferToken(ReservedSend(context.Background()), lane, "USDC", treasuryTestRecipient, big.NewInt(1)); err != nil {
			t.Fatalf("owed delivery %d: %v", i+1, err)
		}
	}
	if signer.calls() != n {
		t.Fatalf("broadcasts = %d, want %d", signer.calls(), n)
	}
	withTreasuryBalance(lane, new(big.Int).Mul(per, big.NewInt(n)))
	if _, _, err := tr.TransferToken(context.Background(), lane, "USDC", treasuryTestRecipient, big.NewInt(1)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("a non-owed transfer must respect the reservation: %v", err)
	}
}
