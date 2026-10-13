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
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// lockWatchStore marks while a store lock is held, so a node call made then
// is caught (outbox rule A: no network I/O inside the lock).
type lockWatchStore struct {
	*GormKeyLaneStore
	held atomic.Bool
}

func (s *lockWatchStore) WithLock(ctx context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error {
	return s.GormKeyLaneStore.WithLock(ctx, chainID, address, func(q KeyLaneTxn) error {
		s.held.Store(true)
		defer s.held.Store(false)
		return fn(q)
	})
}

// Review 7 / outbox A–B: sends, re-broadcasts, speed-ups and fills never
// touch the network while a lane lock is held.
func TestKeyLane_NoNetworkInsideTheLock(t *testing.T) {
	store := &lockWatchStore{GormKeyLaneStore: sqliteLaneStore(t)}
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	node.inLock = store.held.Load
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	h5, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	node.forget(common.HexToHash(h5))
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(ctx, callTo(146, callB, "bb", 5)); err != nil { // re-broadcast + speed-up + a send
		t.Fatal(err)
	}
	if n := node.ioInLock.Load(); n != 0 {
		t.Fatalf("%d node calls made while a lane lock was held", n)
	}
	if got := len(node.atNonce(5)); got < 3 {
		t.Fatalf("setup: nonce 5 saw %d signings, want the original, its re-broadcast and a speed-up", got)
	}
}

// Review 1 / outbox D: a speed-up is committed before its broadcast — a
// refused broadcast leaves its row (refused) and the bump counted.
func TestKeyLane_SpeedUpIsCommittedBeforeItsBroadcast(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	h5, _, err := lane.Send(context.Background(), callTo(146, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	node.forget(common.HexToHash(h5))
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Nonce() == 5 {
			return errors.New("transaction underpriced") // the re-broadcast and the speed-up
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	_, _, _ = lane.Send(context.Background(), callTo(146, callB, "bb", 5))
	var alloc models.KeyLaneNonce
	store.DB.Where("nonce = 5").First(&alloc)
	var speedUps int64
	store.DB.Model(&models.KeyLaneTx{}).Where("nonce = 5 AND hash <> ? AND status = ?", strings.ToLower(h5), models.KeyLaneTxRefused).Count(&speedUps)
	if alloc.Bumps != 1 || speedUps != 1 {
		t.Fatalf("bumps=%d refused speed-up rows=%d, want 1 and 1: committed before the broadcast", alloc.Bumps, speedUps)
	}
}

// Review 2: a statement that fails inside the lock (a unique violation) rolls
// the transaction back and the lane keeps working — on SQLite and Postgres.
func testUniqueViolationDoesNotWedge(t *testing.T, store *GormKeyLaneStore) {
	key := newEcdsaKey()
	node := newChainNode(56)
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	if _, _, err := lane.Send(ctx, callTo(56, callA, "aa", 0)); err != nil {
		t.Fatal(err)
	}
	err := store.WithLock(ctx, 56, lane.Address(), func(q KeyLaneTxn) error {
		return q.Create(&models.KeyLaneNonce{Nonce: 0, Fingerprint: "x", Status: models.KeyLaneNonceSigned, BumpLimit: 3})
	})
	if err == nil {
		t.Fatal("setup: a duplicate nonce must violate the unique index")
	}
	if _, _, err := lane.Send(ctx, callTo(56, callB, "bb", 0)); err != nil {
		t.Fatalf("after a failed statement the lane must still send: %v", err)
	}
	if got := node.atNonce(1); len(got) != 1 {
		t.Fatalf("the next call must go to 1: %v", got)
	}
}

func TestKeyLane_UniqueViolationDoesNotWedge_SQLite(t *testing.T) {
	testUniqueViolationDoesNotWedge(t, sqliteLaneStore(t))
}

func TestKeyLane_UniqueViolationDoesNotWedge_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "key_lane_wedge_", &models.KeyLaneNonce{}, &models.KeyLaneTx{})
	testUniqueViolationDoesNotWedge(t, &GormKeyLaneStore{DB: db})
}

// Outbox C + E: a definitely refused signing is never released to another
// call. The same operation retried before the upkeep fills it re-signs the
// SAME call at the SAME nonce; once filled (a 0-value self-transfer), the
// operation is refused for good.
func TestKeyLane_RefusedCallIsRetriedOrFilledNeverReused(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(56)
	node.mined = 3
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	opA := WithOperationRef(context.Background(), "leg:A")
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Nonce() == 3 && *tx.To() == callA {
			return errors.New("insufficient funds for gas * price + value")
		}
		return nil
	}
	if _, attempted, err := lane.Send(opA, callTo(56, callA, "aa", 3)); attempted || !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("attempted=%v err=%v, want a definite refusal", attempted, err)
	}
	// Another call never takes 3.
	if _, _, err := lane.Send(context.Background(), callTo(56, callB, "bb", 3)); err != nil {
		t.Fatal(err)
	}
	if got := node.atNonce(4); len(got) != 1 || *got[0].To() != callB {
		t.Fatalf("call B must go to 4: %v", got)
	}
	// The same operation retried: the same call at 3.
	node.refuse = nil
	if _, _, err := lane.Send(opA, callTo(56, callA, "aa", 3)); err != nil {
		t.Fatal(err)
	}
	for _, tx := range node.atNonce(3) {
		if *tx.To() != callA {
			t.Fatalf("nonce 3 carried another call: %s", tx.To())
		}
	}
	// A second operation refused and never retried: the upkeep fills it.
	opC := WithOperationRef(context.Background(), "leg:C")
	node.refuse = func(tx *types.Transaction, _ int) error {
		if *tx.To() == common.HexToAddress("0x00000000000000000000000000000000000000cc") {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	cc := common.HexToAddress("0x00000000000000000000000000000000000000cc")
	_, _, _ = lane.Send(opC, callTo(56, cc, "cc", 3))
	var c models.KeyLaneNonce
	store.DB.Where("op_key = ?", "leg:C").First(&c)
	node.mu.Lock()
	node.mined = uint64(c.Nonce)
	node.mu.Unlock()
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := lane.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}
	fill := node.atNonce(uint64(c.Nonce))
	last := fill[len(fill)-1]
	if !strings.EqualFold(last.To().Hex(), lane.Address()) || last.Value().Sign() != 0 {
		t.Fatalf("the abandoned nonce must be filled with a 0-value self-transfer, got to=%s", last.To())
	}
	if _, _, err := lane.Send(opC, callTo(56, cc, "cc", 3)); !errors.Is(err, ErrOperationFilled) {
		t.Fatalf("a filled operation retried: %v, want ErrOperationFilled", err)
	}
}

// Review 6 / E: a caller retrying the same operation never gets a new nonce
// — whether the first try's hook failed or it was sent.
func TestKeyLane_SameOperationNeverGetsASecondNonce(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(56)
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	op := WithOperationRef(context.Background(), "release:T1")
	if _, _, err := lane.SendRecorded(op, callTo(56, callA, "aa", 0), func(uint64) error { return errors.New("db down") }); err == nil {
		t.Fatal("the hook's failure must be returned")
	}
	h1, _, err := lane.Send(op, callTo(56, callA, "aa", 0)) // the caller retries
	if err != nil {
		t.Fatal(err)
	}
	h2, attempted, err := lane.Send(op, callTo(56, callA, "aa", 0)) // and again
	if err != nil || !attempted || h2 != h1 {
		t.Fatalf("a repeat returned %s (attempted=%v err=%v), want the first signing %s", h2, attempted, err, h1)
	}
	var allocs int64
	store.DB.Model(&models.KeyLaneNonce{}).Count(&allocs)
	if allocs != 1 || len(node.txs) != 1 {
		t.Fatalf("allocations=%d broadcasts=%d, want 1 and 1", allocs, len(node.txs))
	}
	if _, _, err := lane.Send(op, callTo(56, callB, "bb", 0)); err == nil {
		t.Fatal("the same operation with a different call must be refused")
	}
}

// Review 3: an admin retry after the speed-up limit re-arms a step past every
// earlier signing — never an identical transaction.
func TestKeyLane_RetrySpeedUpStepsPastEverySigning(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	h5, _, _ := lane.Send(context.Background(), callTo(146, callA, "aa", 5))
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Nonce() == 5 && tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	for i := 0; i < 6; i++ {
		node.mu.Lock()
		node.known = map[common.Hash]bool{}
		node.mu.Unlock()
		clock = clock.Add(keyLaneGapGrace + time.Second)
		if _, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5)); errors.Is(err, ErrKeyLaneHeld) {
			break
		}
	}
	before := node.atNonce(5)
	if err := lane.RetrySpeedUp(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	node.mu.Lock()
	node.known = map[common.Hash]bool{}
	node.mu.Unlock()
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5)); err != nil {
		t.Fatal(err)
	}
	after := node.atNonce(5)
	if len(after) <= len(before) {
		t.Fatal("the retry must sign a new speed-up")
	}
	next := after[len(after)-1]
	for _, tx := range before {
		if tx.Hash() == next.Hash() || tx.GasPrice().Cmp(next.GasPrice()) >= 0 {
			t.Fatalf("retry signing %s at %s does not step past %s at %s", next.Hash(), next.GasPrice(), tx.Hash(), tx.GasPrice())
		}
	}
}

// Review 4: a receipt wait polls every signing of the call: the ORIGINAL
// mining after a speed-up was sent still answers.
func TestKeyLane_ReceiptWaitFindsTheOriginalAfterASpeedUp(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	h5, _, _ := lane.Send(context.Background(), callTo(146, callA, "aa", 5))
	node.forget(common.HexToHash(h5))
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	_, _, _ = lane.Send(context.Background(), callTo(146, callB, "bb", 5))
	if lane.FinalHash(context.Background(), h5) == strings.ToLower(h5) {
		t.Fatal("setup: no speed-up")
	}
	r, h, err := waitFollowingReplacements(context.Background(), lane, h5, 3*time.Second,
		func(_ context.Context, hash string, d time.Duration) (*types.Receipt, error) {
			if strings.EqualFold(hash, h5) { // the original mined after all
				return &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: common.HexToHash(hash)}, nil
			}
			time.Sleep(d)
			return nil, errors.New("not found")
		})
	if err != nil || !strings.EqualFold(h, h5) || r.TxHash != common.HexToHash(h5) {
		t.Fatalf("receipt %v for %s (%v), want the original's", r, h, err)
	}
}

// Review 9: the first allocation on an empty table takes the pending nonce
// read twice (an old process may still be sending) and the mined nonce.
func TestKeyLane_FirstUseFloorReadsPendingTwice(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(56)
	node.mined = 4
	node.pendingSeq = []uint64{5, 7} // the old process sent 5 and 6 between the reads
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	if _, _, err := lane.Send(context.Background(), callTo(56, callA, "aa", 5)); err != nil {
		t.Fatal(err)
	}
	if got := node.atNonce(7); len(got) != 1 {
		t.Fatalf("first nonce: %v, want 7 (the second pending read)", node.txs)
	}
	_ = big.NewInt
}
