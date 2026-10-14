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
)

// Regression tests for the third key-lane review (items 1–10).

const feeCapRefusal = "tx fee (1.10 ether) exceeds the configured cap (1.00 ether)"

// cancelledLane is a solo lane on node whose stuck call (refused for good)
// was cancelled and is final at depth; it returns the lane, the signer and
// the cancelled call's hash.
func cancelledLane(t *testing.T, node *chainNode, send func(kl *KeyLane, raw *LocalKeyReleaseSigner) string) (*KeyLane, *LocalKeyReleaseSigner, string) {
	t.Helper()
	ctx := context.Background()
	raw := NewLocalKeyReleaseSigner(newEcdsaKey().k, node)
	kl := soloLane(raw, nil)
	kl.pricer, kl.nonces, kl.lookup, kl.receipts = node, node, node, node
	node.mu.Lock()
	start := node.mined
	node.refuse = func(tx *types.Transaction, _ int) error {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) {
			return errors.New(feeCapRefusal)
		}
		return nil
	}
	node.mu.Unlock()
	h := send(kl, raw)
	driveCancelFinal(t, kl, node, raw.Address(), start)
	node.mu.Lock()
	node.refuse = nil
	node.mu.Unlock()
	if c, err := kl.CallCancelled(ctx, h); err != nil || !c {
		t.Fatalf("setup: %s not cancelled (%v)", h, err)
	}
	return kl, raw, h
}

// driveCancelFinal signs the cancel at nonce, mines it and buries it at the
// lane's cancel depth.
func driveCancelFinal(t *testing.T, kl *KeyLane, node *chainNode, self string, nonce uint64) {
	t.Helper()
	ctx := context.Background()
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	var cancel common.Hash
	for _, tx := range node.atNonce(nonce) {
		if strings.EqualFold(tx.To().Hex(), self) {
			cancel = tx.Hash()
		}
	}
	if cancel == (common.Hash{}) {
		t.Fatal("setup: no cancel signed")
	}
	node.mu.Lock()
	node.mined = nonce + 1
	node.mu.Unlock()
	node.mine(cancel, 100, 100+kl.cancelDepth()-1)
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
}

// Item 1: finality is max(keyLaneCancelDepth, the configured depth).
func TestKeyLane_CancelFinalOnlyAtTheConfiguredDepth(t *testing.T) {
	node := newChainNode(137)
	node.mined = 4
	raw := NewLocalKeyReleaseSigner(newEcdsaKey().k, node)
	kl := soloLane(raw, nil)
	kl.pricer, kl.nonces, kl.lookup, kl.receipts = node, node, node, node
	kl.finality = 8
	ctx := context.Background()
	node.refuse = func(tx *types.Transaction, _ int) error {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) {
			return errors.New(feeCapRefusal)
		}
		return nil
	}
	h, _, _ := kl.Send(ctx, callTo(137, callA, "aa", 4))
	_ = kl.Watch(ctx)
	var cancel common.Hash
	for _, tx := range node.atNonce(4) {
		if strings.EqualFold(tx.To().Hex(), raw.Address()) {
			cancel = tx.Hash()
		}
	}
	node.mu.Lock()
	node.mined = 5
	node.mu.Unlock()
	node.mine(cancel, 100, 100+keyLaneCancelDepth-1) // 5 deep: enough for the floor, not the configured 8
	_ = kl.Watch(ctx)
	if c, _ := kl.CallCancelled(ctx, h); c {
		t.Fatal("5 deep under a configured depth of 8 must not be final")
	}
	node.mine(cancel, 100, 107)
	_ = kl.Watch(ctx)
	if c, err := kl.CallCancelled(ctx, h); err != nil || !c {
		t.Fatalf("8 deep: %v %v, want cancelled", c, err)
	}
}

// Item 1: before paying again for a leg recorded as cancelled, the sale gate
// re-proves it on chain — a chain that disagrees (the original mined) means
// no second payment.
func TestSaleGate_CancelledLegIsReprovenOnChainBeforeARetry(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	node := newChainNode(137)
	node.mined = 7
	clock := time.Now()
	raw, kl := rawSaleLane(t, r, node, &clock)
	kl.receipts = node
	node.refuse = func(tx *types.Transaction, _ int) error {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) {
			return errors.New(feeCapRefusal)
		}
		return nil
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	h1 := r.legs(t)[0].TxHash
	driveCancelFinal(t, kl, node, raw.Address(), 7)
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatal(err)
	}
	if leg := r.legs(t)[0]; leg.Status != models.RampLegStatusFailed {
		t.Fatalf("setup: leg = %s", leg.Status)
	}
	// The chain now disagrees: the original is what mined at 7.
	node.mu.Lock()
	for h := range node.receipts {
		delete(node.receipts, h)
	}
	node.refuse = nil
	node.mu.Unlock()
	node.mine(common.HexToHash(h1), 100, 120)
	before := len(node.txs)
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("retry on a cancel the chain does not show = %v, want refused", err)
	}
	if len(r.legs(t)) != 1 || len(node.txs) != before {
		t.Fatal("no second payment may be signed")
	}
}

// raceStore lets a test fail the store in the ways a real database does.
type raceStore struct {
	*GormKeyLaneStore
	missByOp     atomic.Int32 // outside-lock ByOp reads that miss (a race)
	byOpErr      atomic.Bool  // the outside-lock ByOp read fails
	failCommit   atomic.Bool  // the next WithLock commits, then reports an error
	failLock     atomic.Int32 // WithLock calls that fail before running
	locks        atomic.Int32 // WithLock calls made
	thenFailLock atomic.Bool  // after the commit error, the next WithLock fails
	afterCommit  func()       // runs after a commit that then reports an error
	allocOf      atomic.Int32 // AllocationOf calls
	others       atomic.Int32 // other signing reads
}

func (s *raceStore) ByOp(ctx context.Context, chainID int64, address, op string) (*models.KeyLaneNonce, error) {
	if s.byOpErr.Load() {
		return nil, errors.New("read: connection reset")
	}
	if s.missByOp.Load() > 0 {
		s.missByOp.Add(-1)
		return nil, nil
	}
	return s.GormKeyLaneStore.ByOp(ctx, chainID, address, op)
}

func (s *raceStore) WithLock(ctx context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error {
	s.locks.Add(1)
	if s.failLock.Load() > 0 {
		s.failLock.Add(-1)
		return errors.New("lock: connection reset")
	}
	err := s.GormKeyLaneStore.WithLock(ctx, chainID, address, fn)
	if err == nil && s.failCommit.CompareAndSwap(true, false) {
		if s.thenFailLock.CompareAndSwap(true, false) {
			s.failLock.Store(1)
		}
		if s.afterCommit != nil {
			s.afterCommit()
		}
		return errors.New("driver: bad connection")
	}
	return err
}

func (s *raceStore) AllocationOf(ctx context.Context, chainID int64, address, hash string) (*models.KeyLaneNonce, []models.KeyLaneTx, error) {
	s.allocOf.Add(1)
	return s.GormKeyLaneStore.AllocationOf(ctx, chainID, address, hash)
}

func (s *raceStore) SigningsOf(ctx context.Context, chainID int64, address, hash string) ([]string, error) {
	s.others.Add(1)
	return s.GormKeyLaneStore.SigningsOf(ctx, chainID, address, hash)
}

// Item 2: on the REUSED path (the operation's allocation found only under the
// lock), a COMMIT error is re-checked under the re-taken lock: in flight.
func TestKeyLane_ReusedPathCommitErrorIsInFlight(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(56)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := WithOperationRef(context.Background(), "p2p_release:R2")
	h, _, err := lane.Send(ctx, callTo(56, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	store.missByOp.Store(1)
	store.failCommit.Store(true)
	h2, attempted, err := lane.Send(ctx, callTo(56, callA, "aa", 5))
	if h2 != h || !attempted || errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("reused + commit error: %q %v %v, want the allocation (in flight), never not-sent", h2, attempted, err)
	}
	if got := node.atNonce(5); len(got) != 1 {
		t.Fatalf("nonce 5 sent %d times", len(got))
	}
}

// Item 2: when the re-check itself fails, the answer is "outcome unknown"
// (attempted, never not-sent); a failed read of the operation likewise.
func TestKeyLane_UnknownCommitOutcomeIsNeverNotSent(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(56)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := WithOperationRef(context.Background(), "ramp_leg:U")
	store.failCommit.Store(true)
	store.thenFailLock.Store(true) // the re-check's lock fails too
	h, attempted, err := lane.Send(ctx, callTo(56, callA, "aa", 5))
	if !attempted || h == "" || errors.Is(err, ErrTxNotBroadcast) || !errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("commit error + failed re-check: %q %v %v, want outcome unknown", h, attempted, err)
	}
	store.byOpErr.Store(true)
	if _, attempted, err := lane.Send(ctx, callTo(56, callA, "aa", 5)); !attempted || !errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("unreadable operation: %v %v, want outcome unknown", attempted, err)
	}
}

// Item 3: a fee-cap refusal of a SPEED-UP while an earlier, cheaper signing
// is accepted stops the bumping (hold) — it never cancels the accepted call.
func TestKeyLane_FeeCapOnASpeedUpHoldsNeverCancelsTheAcceptedCall(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(137)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	h1, _, err := lane.Send(ctx, callTo(137, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	node.forget(common.HexToHash(h1))
	node.refuse = func(tx *types.Transaction, _ int) error {
		switch {
		case tx.Hash() == common.HexToHash(h1):
			return errors.New("transaction underpriced")
		case tx.GasPrice().Cmp(big.NewInt(1_000_000_000)) > 0:
			return errors.New(feeCapRefusal)
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	_ = lane.Watch(ctx)
	a, _ := store.Get(ctx, 137, lane.Address(), 5)
	if a.Cancel {
		t.Fatal("an accepted call must never be cancelled for a speed-up's fee-cap refusal")
	}
	if _, why, held := lane.Held(); !held || !strings.Contains(why, "earlier signing is accepted") {
		t.Fatalf("held=%v reason=%q, want held: stop bumping", held, why)
	}
	for _, tx := range node.txs {
		if strings.EqualFold(tx.To().Hex(), lane.Address()) {
			t.Fatal("no cancel may be signed")
		}
	}
}

// Item 4: a committed operation is answered (reused) even on a lane another
// process has held — never "held, not sent".
func TestKeyLane_CommittedOperationAnsweredOnAHeldLane(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	p1 := durableLane(t, store, key, node, &clock)
	p2 := durableLane(t, store, key, node, &clock)
	ctx := WithOperationRef(context.Background(), "p2p_release:H4")
	h, _, err := p2.Send(ctx, callTo(146, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	p1.hold(p1.Address(), 9, "held by another process")
	store.missByOp.Store(1) // the early read misses: the lock decides
	got, attempted, err := p2.Send(ctx, callTo(146, callA, "aa", 5))
	if got != h || !attempted || errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("retry on a held lane: %q %v %v, want the committed allocation", got, attempted, err)
	}
}

// Item 5: a nonce flagged for cancellation is never sped up as the call.
func TestKeyLane_SpeedUpRefusesACancelFlaggedNonce(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	h1, _, _ := lane.Send(ctx, callTo(146, callA, "aa", 5))
	if err := lane.RequestCancel(ctx, 5); err != nil {
		t.Fatal(err)
	}
	a, _ := store.Get(ctx, 146, lane.Address(), 5)
	txs, _ := store.Signings(ctx, a.ID)
	lane.speedUp(ctx, lane.signer, a, &txs[0])
	for _, tx := range node.atNonce(5) {
		if *tx.To() == callA && tx.Hash() != common.HexToHash(h1) {
			t.Fatal("a cancel-flagged call was sped up")
		}
	}
}

// Item 7: the release verify reads the mined signing without waiting, then
// verifies ONCE on it.
func TestAutoRelease_VerifiesOnceOnTheMinedSigning(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_7"); err != nil {
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
	node.mine(common.HexToHash(h2), 50, 60)
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID), Buyer: common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: new(big.Int).Sub(tokenAmt, big.NewInt(500)), FeeToSystem: big.NewInt(500)}
	var calls []string
	esc.verifyReleasedFor = func(h string) error {
		calls = append(calls, h)
		if strings.EqualFold(h, h2) {
			return nil
		}
		return errors.New("receipt not found")
	}
	s := &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x1111111111111111111111111111111111111111", hash: h1}, lane: lane}
	if _, err := svc.AutoRelease(ctx, trade.ID, s); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.EqualFold(calls[0], h2) {
		t.Fatalf("verify calls = %v, want exactly one, on the mined %s", calls, h2)
	}
}

// Item 8: a gas top-up the lane cancelled never arrived — the next request
// sends a new one instead of reporting the dead one.
func TestGasTopUp_CancelledTopUpIsReplaced(t *testing.T) {
	r, orderID := newSponsorRig("bsc", "USDT")
	r.chain.balances[strings.ToLower(sponsorTestUser)] = big.NewInt(0)
	svc := NewGasTopUp(r.orders, r.store, r.lanes(), 3, time.Second, func() time.Time { return sponsorTestNow })
	node := newChainNode(56)
	kl, raw, h1 := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		r.lane.Signer, r.lane.KeyLane = raw, kl
		_, _ = svc.TopUp(context.Background(), topUpUser(r), orderID)
		return r.store.onlyRow().TxHash
	})
	_ = kl
	got, err := svc.TopUp(context.Background(), topUpUser(r), orderID)
	if err != nil && !errors.Is(err, ErrTopUpUnconfirmed) {
		t.Fatalf("TopUp after the cancel: %v", err)
	}
	if got.TxHash == "" || strings.EqualFold(got.TxHash, h1) || !strings.EqualFold(r.store.onlyRow().TxHash, got.TxHash) {
		t.Fatalf("result %+v row %+v, want a new top-up replacing the cancelled %s", got, r.store.onlyRow(), h1)
	}
	paid := 0
	for _, tx := range node.txs {
		if !strings.EqualFold(tx.To().Hex(), raw.Address()) && node.known[tx.Hash()] {
			paid++
		}
	}
	if paid != 1 {
		t.Fatalf("%d top-ups reached her, want 1", paid)
	}
}

// Item 8: a P2P drip the lane cancelled is not an "unused drip".
func TestP2PGas_CancelledDripAllowsANewOne(t *testing.T) {
	node := newChainNode(146)
	var h string
	kl, raw, _ := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		h, _, _ = kl.Send(WithOperationRef(context.Background(), "p2p_drip:d1"), callTo(146, callA, "aa", 0))
		return h
	})
	user := uuid.New()
	p := &P2PGasSponsor{now: time.Now, lastDrip: map[uuid.UUID]p2pDrip{user: {at: time.Now(), postBalance: big.NewInt(1), txHash: h, op: "p2p_drip:d1"}}}
	lane := &SponsorLane{KeyLane: kl, Signer: raw}
	if err := p.refuseUnusedDrip(context.Background(), lane, user, sponsorTestUser, big.NewInt(10)); err != nil {
		t.Fatalf("a cancelled drip: %v, want a new one allowed", err)
	}
	if _, ok := p.lastDrip[user]; ok {
		t.Fatal("the cancelled drip must be forgotten")
	}
}

// cancellingSellChain is the IT sell chain whose operator lane cancelled tx.
type cancellingSellChain struct {
	*fakeSellChain
	cancelled string
}

func (c *cancellingSellChain) OperatorCallCancelled(_ context.Context, h string) (bool, error) {
	return strings.EqualFold(h, c.cancelled), nil
}

// Item 8: an IT sell whose debit the operator lane cancelled is failed at
// once (nothing was debited) — not left debiting for the abandon window.
func TestReconcileSell_CancelledDebitFailsAtOnce(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(i *models.ITIntent) { i.Status = models.ITStatusDebiting; i.TxHash = "0xdebit" })
	f.s.sellChain = &cancellingSellChain{fakeSellChain: f.chain, cancelled: "0xdebit"}
	if !f.s.reconcileInflightSell(context.Background(), in, time.Now()) {
		t.Fatal("want the cancelled debit decided now")
	}
	var got models.ITIntent
	f.s.db.First(&got, "id = ?", in.ID)
	if got.Status != models.ITStatusFailed || !strings.Contains(got.FailureReason, "cancelled") {
		t.Fatalf("sell = %s %q, want failed as cancelled", got.Status, got.FailureReason)
	}
}

// Item 8: a pool slice release the release lane cancelled moved nothing:
// as final as a deep revert, so the slice is released again.
func TestSliceHashReverted_CancelledReleaseCounts(t *testing.T) {
	r, _, sl, _, _ := openOne(t)
	node := newChainNode(sl.ChainID)
	kl, _, h := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		h, _, _ := kl.Send(context.Background(), callTo(sl.ChainID, callA, "aa", 0))
		return h
	})
	r.it.poolDesk.c.Sender = &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x00000000000000000000000000000000000000a1"}, lane: kl}
	sl.ReleaseTxHash = h
	if gone, err := r.it.SliceHashReverted(context.Background(), &sl); err != nil || !gone {
		t.Fatalf("SliceHashReverted = %v %v, want true for a cancelled release", gone, err)
	}
}

// cancellingRail is the name-change fee rail whose lane cancelled tx.
type cancellingRail struct {
	*fakeStableRail
	cancelled string
}

func (c *cancellingRail) GiveUpCancelled(_ context.Context, _, _, h string) (bool, error) {
	return strings.EqualFold(h, c.cancelled), nil
}

// Item 8: a name-change refund the treasury lane cancelled never left: it
// goes back to refund_failed and the sweep sends it again (a new operation).
func TestNameChangeSweep_CancelledRefundIsRetried(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.svc.SetStableRail(&cancellingRail{fakeStableRail: r.rail, cancelled: "0xrefund"})
	ch := &models.UserNameChange{ID: uuid.New(), UserID: r.user, Status: models.NameChangeStatusRejected,
		FeeStatus: models.NameChangeFeeRefundUncertain, FeeTxHash: "0xrefund", PayChain: "base"}
	if err := r.db.Create(ch).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.sweepAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got models.UserNameChange
	r.db.First(&got, "id = ?", ch.ID)
	if got.FeeStatus != models.NameChangeFeeRefundFailed {
		t.Fatalf("fee status = %s, want refund_failed (retried as a new send)", got.FeeStatus)
	}
}

// Item 9: a hold whose persist failed is persisted again by the watch —
// never cleared for want of a row.
func TestKeyLane_UnpersistedHoldIsRepersistedNotCleared(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	store.failLock.Store(1)
	lane.hold(lane.Address(), 9, "persist fails once")
	if err := lane.Watch(context.Background()); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("Watch = %v, want still held", err)
	}
	st, err := store.Hold(context.Background(), 146, lane.Address())
	if err != nil || st == nil || st.HeldNonce == nil || *st.HeldNonce != 9 {
		t.Fatalf("shared hold = %+v %v, want re-persisted at 9", st, err)
	}
}

// Item 9: an admin's clear on one process resets the gap timers of every
// process (the shared clear version).
func TestKeyLane_ClearResetsGapTimersInEveryProcess(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	p1 := durableLane(t, store, key, node, &clock)
	p2 := durableLane(t, store, key, node, &clock)
	p2.noteGap(9, clock.Add(-time.Hour))
	p1.hold(p1.Address(), 9, "x")
	if err := p1.ClearHold(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = p2.Watch(context.Background())
	p2.gapMu.Lock()
	_, kept := p2.gapSeen[9]
	p2.gapMu.Unlock()
	if kept {
		t.Fatal("the other process kept its gap timer across an admin clear")
	}
}

// Item 10: following a recorded hash is ONE store read (allocation and its
// signings together).
func TestFollowCall_IsOneStoreRead(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
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
	store.allocOf.Store(0)
	store.others.Store(0)
	if _, _, err := lane.CallOutcome(ctx, h1, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if store.allocOf.Load() != 1 || store.others.Load() != 0 {
		t.Fatalf("store reads: allocation+signings=%d others=%d, want 1 and 0", store.allocOf.Load(), store.others.Load())
	}
}

// Item 8: an authorization relay the lane cancelled never executed — the
// next request relays it again instead of reporting the dead hash.
func TestAuthorizationRelay_CancelledRelayIsRelayedAgain(t *testing.T) {
	r, orderID := newSponsorRig("base", "USDC")
	withBaseUSDCDomain(t, r)
	relayer := newTestAuthRelayer(r)
	node := newChainNode(8453)
	_, _, h1 := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		r.lane.Signer, r.lane.KeyLane = raw, kl
		_, _ = relayer.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
		return r.store.onlyRow().TxHash
	})
	got, err := relayer.Relay(context.Background(), r.userID, sponsorTestUser, orderID, validAuth())
	if err != nil {
		t.Fatalf("relay after the cancel: %v", err)
	}
	if got.TxHash == "" || strings.EqualFold(got.TxHash, h1) {
		t.Fatalf("result %+v, want a new relay replacing the cancelled %s", got, h1)
	}
}
