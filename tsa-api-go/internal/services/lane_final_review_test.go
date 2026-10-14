package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// Regression tests for the final key-lane review (items 1–10).

// rawSaleLane wires the deliver rig's company-wallet lane to a raw signer on
// node, with a lane that can re-sign and look transactions up.
func rawSaleLane(t *testing.T, r *deliverRig, node *chainNode, clock *time.Time) (*LocalKeyReleaseSigner, *KeyLane) {
	t.Helper()
	raw := NewLocalKeyReleaseSigner(newEcdsaKey().k, node)
	tl := r.s.companySale().wallet.Treasury().Lane("base")
	tl.Signer = raw
	kl := soloLane(raw, nil)
	kl.pricer, kl.nonces, kl.lookup, kl.receipts = node, node, node, node
	kl.now = func() time.Time { return *clock }
	tl.KeyLane = kl
	return raw, kl
}

// Item 1: the first signing was REFUSED, its speed-up mined — the finalize
// follows the leg's recorded (refused) hash to the mined one and settles on it.
func TestFinalizeTreasuryBuy_RefusedFirstSigningSettlesOnTheMinedSpeedUp(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	node := newChainNode(8453)
	node.mined = 7
	clock := time.Now()
	_, kl := rawSaleLane(t, r, node, &clock)
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.GasPrice().Cmp(big.NewInt(1_000_000_000)) == 0 {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	h1 := r.legs(t)[0].TxHash
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	at7 := node.atNonce(7)
	h2 := at7[len(at7)-1].Hash()
	if h2 == common.HexToHash(h1) {
		t.Fatal("setup: no speed-up")
	}
	r.probe.noReceipt = map[common.Hash]bool{common.HexToHash(h1): true}
	r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	leg := r.legs(t)[0]
	if !strings.EqualFold(leg.TxHash, h2.Hex()) || leg.Status != models.RampLegStatusConfirmed {
		t.Fatalf("leg tx=%s status=%s, want settled on the mined speed-up %s", leg.TxHash, leg.Status, h2.Hex())
	}
}

// Item 7: following is a NON-waiting receipt read, and a read error defers —
// never "no receipt": the recorded hash is kept for the next tick.
func TestCallOutcome_ReceiptReadErrorDefers(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	h1, _, _ := lane.Send(ctx, callTo(146, callA, "aa", 5))
	node.forget(common.HexToHash(h1))
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Hash() == common.HexToHash(h1) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	_ = lane.Watch(ctx)
	got, _, err := lane.CallOutcome(ctx, h1, func(context.Context, string) (bool, error) { return false, errors.New("429") })
	if err == nil || got != h1 {
		t.Fatalf("read error: %s %v, want %s kept with the error", got, err, h1)
	}
	final := lane.FinalHash(ctx, h1)
	got, _, err = lane.CallOutcome(ctx, h1, func(_ context.Context, h string) (bool, error) { return strings.EqualFold(h, final), nil })
	if err != nil || !strings.EqualFold(got, final) || strings.EqualFold(got, h1) {
		t.Fatalf("mined replacement: %s %v, want %s", got, err, final)
	}
}

// Item 2 (a): an approve committed but not yet accepted keeps the bridge leg
// in flight WITH its approve hash — never a hashless, stranded leg.
// Item 2 (b): past the grace, with no allocation for the leg's ':route'
// operation, the attempt is freed and the next tick sends the route once.
func TestStartBridge_ApproveInFlightThenRouteRetried(t *testing.T) {
	r := newConversionRig(t)
	ctx := context.Background()
	route := *r.lifi.route
	route.ApprovalAddress = treasuryTestRouter
	r.lifi.route = &route
	approveHash := "0x" + strings.Repeat("a7", 32)
	r.src.signer.mu.Lock()
	r.src.signer.err = broadcastUncertainError{err: errors.New("read tcp: i/o timeout"), txHash: approveHash}
	r.src.signer.mu.Unlock()
	if err := r.svc.Advance(ctx, r.reload(t)); err != nil {
		t.Fatal(err)
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusPending || legs[0].TxHash != "" || !strings.EqualFold(legs[0].ApproveTxHash, approveHash) {
		t.Fatalf("legs = %+v, want one pending leg carrying the approve's hash", legs)
	}
	// Inside the grace: in flight, untouched.
	if err := r.svc.Advance(ctx, r.reload(t)); err != nil {
		t.Fatal(err)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); len(legs) != 1 || legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("inside the grace: %+v", legs)
	}
	r.src.signer.mu.Lock()
	r.src.signer.err = nil
	r.src.signer.mu.Unlock()
	r.clock = r.clock.Add(bridgeRouteGrace + time.Minute)
	for i := 0; i < 2; i++ { // free the approve-only attempt, then send the route
		if err := r.svc.Advance(ctx, r.reload(t)); err != nil {
			t.Fatal(err)
		}
	}
	legs = r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 2 || legs[0].Status != models.RampLegStatusFailed || !strings.HasPrefix(legs[0].Error, "approve only") {
		t.Fatalf("legs = %+v, want attempt 1 freed as approve-only", legs)
	}
	if legs[1].Status != models.RampLegStatusPending || legs[1].TxHash == "" || legs[1].LiFiTool != "across" {
		t.Fatalf("attempt 2 = %+v, want the route sent and recorded", legs[1])
	}
}

// Item 3: a call the node refuses for good (Polygon's RPC tx-fee cap) is
// cancelled at its nonce; once the cancel mines the sale leg fails cleanly
// (ErrCallCancelled), and the retry pays her exactly once.
func TestSendSale_FeeCapRefusalIsCancelledAndTheRetryPaysOnce(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	node := newChainNode(137)
	node.mined = 7
	clock := time.Now()
	raw, kl := rawSaleLane(t, r, node, &clock)
	feeCap := errors.New("tx fee (1.10 ether) exceeds the configured cap (1.00 ether)")
	node.refuse = func(tx *types.Transaction, _ int) error {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) {
			return feeCap
		}
		return nil
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	h1 := r.legs(t)[0].TxHash
	if err := kl.Watch(ctx); err != nil { // signs and sends the cancel at 7
		t.Fatal(err)
	}
	var cancelTx *types.Transaction
	for _, tx := range node.atNonce(7) {
		if strings.EqualFold(tx.To().Hex(), raw.Address()) {
			cancelTx = tx
		}
	}
	if cancelTx == nil || cancelTx.Value().Sign() != 0 || cancelTx.GasPrice().Cmp(big.NewInt(1_000_000_000)) <= 0 {
		t.Fatalf("want a 0-value self-transfer at 7 priced above the refused call, got %v", cancelTx)
	}
	if final := kl.FinalHash(ctx, h1); !strings.EqualFold(final, h1) {
		t.Fatalf("the cancel must never be followed as the call's receipt: %s", final)
	}
	node.mu.Lock()
	node.mined = 8 // the cancel mined, one block deep
	node.mu.Unlock()
	node.mine(cancelTx.Hash(), 100, 100)
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	// Shallower than the settle depth: still in flight — no verifier may
	// fail the leg the original could still land in after a reorg.
	if c, _ := kl.CallCancelled(ctx, h1); c {
		t.Fatal("a cancel 1 block deep must not be final")
	}
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if legs := r.legs(t); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("leg = %s, want still pending under a shallow cancel", legs[0].Status)
	}
	if _, _, err := kl.Send(WithOperationRef(ctx, "ramp_leg:"+r.legs(t)[0].ID.String()), func() ([]byte, error) { return nil, errors.New("unused") }); !errors.Is(err, ErrPendingBroadcast) || errors.Is(err, ErrCallCancelled) {
		t.Fatalf("the operation under a shallow cancel = %v, want in flight", err)
	}
	node.mine(cancelTx.Hash(), 100, 100+keyLaneCancelDepth-1) // now keyLaneCancelDepth deep
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	if c, err := kl.CallCancelled(ctx, h1); err != nil || !c {
		t.Fatalf("CallCancelled = %v %v, want true at depth", c, err)
	}
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("finalize of a cancelled sale: %v", err)
	}
	if legs := r.legs(t); legs[0].Status != models.RampLegStatusFailed || !strings.HasPrefix(legs[0].Error, "cancelled:") {
		t.Fatalf("leg = %+v, want failed as cancelled", legs[0])
	}
	node.mu.Lock()
	node.refuse = nil
	node.mu.Unlock()
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	legs := r.legs(t)
	if len(legs) != 2 || legs[1].Status != models.RampLegStatusPending || legs[1].Nonce == nil || *legs[1].Nonce != 8 {
		t.Fatalf("legs = %+v, want attempt 2 in flight at nonce 8", legs)
	}
	paid := 0
	node.mu.Lock()
	for _, tx := range node.txs {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) && node.known[tx.Hash()] {
			paid++
		}
	}
	node.mu.Unlock()
	if paid != 1 {
		t.Fatalf("%d accepted payments to her, want exactly 1", paid)
	}
}

// Item 4: an owed call's speed-up (a slice release at the reservation's
// edge) is gated as its send was — never against its own reservation.
func TestKeyLane_ReservedSpeedUpDoesNotCountItsOwnReservation(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	lane.AddReservation("pool_releases", func(context.Context) (uint64, error) { return 300_000, nil })
	ctx := context.Background()
	h5, _, err := lane.Send(ReservedSend(ctx), callTo(146, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	node.forget(common.HexToHash(h5))
	node.mu.Lock()
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	node.balance = big.NewInt(200_000_000_000_000) // its own fee fits; fee + the reservation would not
	node.mu.Unlock()
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := lane.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	at5 := node.atNonce(5)
	if last := at5[len(at5)-1]; last.Hash() == common.HexToHash(h5) || *last.To() != callA {
		t.Fatal("the owed release must be sped up within its own fee")
	}
}

// reusedReleaseSender answers like the key lane does for an operation that
// already holds an allocation: its hash, without running the hook again.
type reusedReleaseSender struct {
	fakeReleaseSigner
	lane *KeyLane
	err  error
}

func (s *reusedReleaseSender) Send(_ context.Context, _ int64, build func() ([]byte, error)) (string, error) {
	if _, err := build(); err != nil {
		return "", notBroadcast(err)
	}
	return s.hash, s.err
}

func (s *reusedReleaseSender) Lane(int64) *KeyLane { return s.lane }

// Item 6 (a): an ordinary trade's release the lane committed but no node
// accepted yet is IN FLIGHT: the trade stays releasing, never rolled back.
func TestAutoRelease_PendingBroadcastKeepsAnOrdinaryTradeReleasing(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_p"); err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("6a", 32)
	s := &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x1111111111111111111111111111111111111111", hash: hash},
		err: pendingBroadcast(hash, errors.New("transaction underpriced"))}
	if _, err := svc.AutoRelease(context.Background(), trade.ID, s); !errors.Is(err, ErrPendingBroadcast) {
		t.Fatalf("err = %v, want in flight", err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusReleasing {
		t.Fatalf("status = %s, want releasing (in flight, not rolled back)", got.Status)
	}
}

// Item 6 (b): the release was sped up during the verify — the replacement
// mined, not the original. The verify follows the call's signings and
// confirms on the replacement; nothing is rolled back.
func TestAutoRelease_VerifiesTheSpedUpRelease(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_s"); err != nil {
		t.Fatal(err)
	}
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	h1, _, _ := lane.Send(ctx, callTo(146, callA, "aa", 5))
	node.forget(common.HexToHash(h1))
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Hash() == common.HexToHash(h1) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	_ = lane.Watch(ctx)
	h2 := lane.FinalHash(ctx, h1)
	if strings.EqualFold(h2, h1) {
		t.Fatal("setup: no speed-up")
	}
	node.mine(common.HexToHash(h2), 50, 60) // the replacement mined (non-waiting receipt read)
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: new(big.Int).Sub(tokenAmt, big.NewInt(500)), FeeToSystem: big.NewInt(500)}
	esc.verifyReleasedFor = func(h string) error {
		if strings.EqualFold(h, h2) {
			return nil
		}
		return errors.New("receipt not found")
	}
	s := &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x1111111111111111111111111111111111111111", hash: h1}, lane: lane}
	if _, err := svc.AutoRelease(ctx, trade.ID, s); err != nil {
		t.Fatalf("AutoRelease: %v", err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusReleased {
		t.Fatalf("status = %s, want released on the replacement", got.Status)
	}
}

// Item 8: FillGap takes only a true hole — a nonce at or above the highest
// allocated one (a typo) is refused, as is one in the record.
func TestKeyLane_FillGapRefusesANonceThatIsNotAHole(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 6)); err != nil { // 6, 7: 5 is a hole
			t.Fatal(err)
		}
	}
	for _, n := range []uint64{7, 9, 70} {
		if _, err := lane.FillGap(ctx, n); err == nil {
			t.Fatalf("FillGap(%d) must be refused", n)
		}
	}
	if _, err := lane.FillGap(ctx, 5); err != nil {
		t.Fatalf("the true hole at 5: %v", err)
	}
}

// Item 9: a hold lives in the database: another process refuses at once (in
// its allocation transaction) and adopts it on its watch; an admin's clear on
// one process lets the other resume.
func TestKeyLane_HoldIsSharedAcrossProcesses(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	p1 := durableLane(t, store, key, node, &clock)
	p2 := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	p1.hold(p1.Address(), 9, "test hold")
	if _, _, err := p2.Send(ctx, callTo(146, callA, "aa", 5)); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("other process send: %v, want held", err)
	}
	if err := p2.Watch(ctx); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("other process watch: %v, want held", err)
	}
	if err := p1.ClearHold(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p2.Watch(ctx); err != nil {
		t.Fatalf("after the clear elsewhere: %v", err)
	}
	if _, _, err := p2.Send(ctx, callTo(146, callA, "aa", 5)); err != nil {
		t.Fatalf("the other process must resume: %v", err)
	}
}

// commitThenFailStore commits the next allocation and then reports an error
// (a connection lost on COMMIT).
type commitThenFailStore struct {
	*GormKeyLaneStore
	failNext atomic.Bool
}

func (s *commitThenFailStore) WithLock(ctx context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error {
	err := s.GormKeyLaneStore.WithLock(ctx, chainID, address, fn)
	if err == nil && s.failNext.CompareAndSwap(true, false) {
		return errors.New("driver: bad connection")
	}
	return err
}

// Item 10: an error that came AFTER the commit is not "not sent": the lane
// finds its allocation and answers in flight with the hash; the retry gets
// the same allocation, and the call goes out exactly once.
func TestKeyLane_CommitErrorAfterCommitIsInFlight(t *testing.T) {
	store := &commitThenFailStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(56)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := WithOperationRef(context.Background(), "ramp_leg:L10")
	store.failNext.Store(true)
	h, attempted, err := lane.Send(ctx, callTo(56, callA, "aa", 5))
	if !attempted || h == "" || !errors.Is(err, ErrPendingBroadcast) || errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("hash=%q attempted=%v err=%v, want in flight", h, attempted, err)
	}
	if h2, _, err := lane.Send(ctx, callTo(56, callA, "aa", 5)); h2 != h || !errors.Is(err, ErrPendingBroadcast) {
		t.Fatalf("retry: %q %v, want the same allocation in flight", h2, err)
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := lane.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := node.atNonce(5); len(got) != 1 || !strings.EqualFold(got[0].Hash().Hex(), h) {
		t.Fatalf("nonce 5 = %v, want the committed call sent exactly once", got)
	}
}

// A reorg replaces a still-shallow cancel with the ORIGINAL call: each tick
// re-reads the receipts, the allocation resolves as the call mined, and the
// caller sees the original's success — never a cancel, never a retry.
func TestSendSale_ReorgThatLandsTheOriginalOverAShallowCancelSettles(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	node := newChainNode(137)
	node.mined = 7
	clock := time.Now()
	raw, kl := rawSaleLane(t, r, node, &clock)
	node.refuse = func(tx *types.Transaction, _ int) error {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) {
			return errors.New("tx fee (1.10 ether) exceeds the configured cap (1.00 ether)")
		}
		return nil
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	h1 := r.legs(t)[0].TxHash
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	var cancelHash common.Hash
	for _, tx := range node.atNonce(7) {
		if strings.EqualFold(tx.To().Hex(), raw.Address()) {
			cancelHash = tx.Hash()
		}
	}
	node.mu.Lock()
	node.mined = 8
	node.mu.Unlock()
	node.mine(cancelHash, 100, 101) // the cancel mined, 2 deep
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	// The reorg: block 100 is replaced; nonce 7 is now the original call.
	node.mu.Lock()
	delete(node.receipts, cancelHash)
	node.mu.Unlock()
	node.mine(common.HexToHash(h1), 100, 110)
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := kl.CallCancelled(ctx, h1); c {
		t.Fatal("the original mined: never cancelled")
	}
	a, _, _ := kl.store.AllocationOf(ctx, kl.chainID, raw.Address(), h1)
	if a == nil || a.Status != models.KeyLaneNonceMined {
		t.Fatalf("allocation = %+v, want mined as the original call", a)
	}
	r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	legs := r.legs(t)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusConfirmed || !strings.EqualFold(legs[0].TxHash, h1) {
		t.Fatalf("legs = %+v, want the original confirmed and no retry", legs)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("intent = %s, want settled", got.Status)
	}
}

// A bridge leg's approve committed, its route not yet: the route's slow
// allocation (its hook still inside the allocation transaction) races the
// free. RetireOp under the lane lock decides it — exactly one route is ever
// broadcast, in either order.
func testRetiredRouteNeverRacesTheRetry(t *testing.T, store *GormKeyLaneStore, routeFirst bool) {
	if err := store.DB.AutoMigrate(&models.RampLeg{}); err != nil {
		t.Fatal(err)
	}
	legs := &GormRampLegStore{DB: store.DB}
	node := newChainNode(56)
	node.mined = 3
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	orderID := uuid.New()
	newLeg := func(attempt int) *models.RampLeg {
		l := &models.RampLeg{OrderID: orderID, Kind: models.RampLegKindBridge, Attempt: attempt, ChainKey: "bsc", Token: "USDT",
			Amount: "1", Status: models.RampLegStatusPending, ApproveTxHash: "0xapprove", CreatedAt: time.Now()}
		if err := store.DB.Create(l).Error; err != nil {
			t.Fatal(err)
		}
		return l
	}
	sendRoute := func(leg *models.RampLeg, hold chan struct{}, started chan struct{}) error {
		op := "ramp_leg:" + leg.ID.String() + ":route"
		_, _, err := lane.SendRecorded(WithOperationRef(ctx, op), callTo(56, callB, "bb", 3), func(hctx context.Context, _ uint64, hash string) error {
			if started != nil {
				close(started)
				<-hold
			}
			return legs.SetTxHash(hctx, leg.ID, hash, "across")
		})
		return err
	}
	// The resume step, as resumeBridgeAfterApprove takes it.
	free := func(leg *models.RampLeg) bool {
		held, err := lane.RetireOp(ctx, lane.Address(), "ramp_leg:"+leg.ID.String()+":route", "", RetirePermanent)
		if err != nil {
			t.Error(err)
			return false
		}
		if held != nil {
			return false
		}
		return legs.FailUnsent(ctx, leg.ID, "approve only: the route was never sent") == nil
	}
	leg := newLeg(1)
	freed := false
	if routeFirst {
		hold, started := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() { done <- sendRoute(leg, hold, started) }()
		<-started
		res := make(chan bool, 1)
		go func() { res <- free(leg) }() // blocks on the lane lock
		time.Sleep(100 * time.Millisecond)
		close(hold)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		freed = <-res
	} else {
		freed = free(leg)
		if err := sendRoute(leg, nil, nil); !errors.Is(err, ErrOperationRetired) || !errors.Is(err, ErrTxNotBroadcast) {
			t.Fatalf("the retired route = %v, want refused, not sent", err)
		}
	}
	if freed { // the retry: a new attempt sends its own route
		if err := sendRoute(newLeg(2), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	routes := 0
	for _, tx := range node.txs {
		if *tx.To() == callB {
			routes++
		}
	}
	if routes != 1 || freed == routeFirst {
		t.Fatalf("routes broadcast = %d (freed=%v, routeFirst=%v), want exactly one and the leg freed only when the route never committed", routes, freed, routeFirst)
	}
}

func TestBridge_RetiredRouteNeverRacesTheRetry_SQLite(t *testing.T) {
	for name, routeFirst := range map[string]bool{"route_first": true, "retire_first": false} {
		t.Run(name, func(t *testing.T) { testRetiredRouteNeverRacesTheRetry(t, sqliteLaneStore(t), routeFirst) })
	}
}

func TestBridge_RetiredRouteNeverRacesTheRetry_Postgres(t *testing.T) {
	for name, routeFirst := range map[string]bool{"route_first": true, "retire_first": false} {
		t.Run(name, func(t *testing.T) {
			db := testutil.TempPostgres(t, "key_lane_retire_", &models.KeyLaneNonce{}, &models.KeyLaneTx{}, &models.KeyLaneState{}, &models.KeyLaneRetiredOp{}, &models.RampLeg{})
			testRetiredRouteNeverRacesTheRetry(t, &GormKeyLaneStore{DB: db}, routeFirst)
		})
	}
}
