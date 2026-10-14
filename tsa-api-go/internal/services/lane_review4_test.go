package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Regression tests for the fourth key-lane review: the structural give-up
// rule (RetireOp) and items 1–10.

// The structural rule at the lane boundary: a refusal before any commit
// (here a build error) for an operation another process already committed is
// answered IN FLIGHT with that allocation — never "not sent".
func TestKeyLane_RefusalOfACommittedOperationIsInFlight(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	p1 := durableLane(t, store, key, node, &clock)
	p2 := durableLane(t, store, key, node, &clock)
	ctx := WithOperationRef(context.Background(), "ramp_leg:S1")
	h, _, err := p1.Send(ctx, callTo(146, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	store.missByOp.Store(1) // p2's early read misses (a replica lag): the build then fails
	got, attempted, err := p2.Send(ctx, func() ([]byte, error) { return nil, errors.New("rpc: estimate gas failed") })
	if !attempted || got != h || errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("refusal of a committed op = %q %v %v, want its allocation in flight", got, attempted, err)
	}
}

// A give-up (RetireOp) while an attempt of the same operation is still on
// its way: that late attempt is refused at its commit; a retry begun after the
// give-up proceeds. SQLite and Postgres.
func testLateAttemptRefusedAfterGiveUp(t *testing.T, store KeyLaneStore) {
	key := newEcdsaKey()
	node := newChainNode(56)
	node.mined = 3
	clock := time.Now()
	slow := durableLane(t, store, key, node, &clock)
	other := durableLane(t, store, key, node, &clock)
	ctx := WithOperationRef(context.Background(), "ramp_leg:L4")
	building, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var lateErr error
	var lateAttempted bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, lateAttempted, lateErr = slow.Send(ctx, func() ([]byte, error) {
			close(building)
			<-release
			return callTo(56, callA, "aa", 3)()
		})
	}()
	<-building
	if held, err := other.RetireOp(context.Background(), other.Address(), "ramp_leg:L4", "", false); err != nil || held != nil {
		t.Fatalf("give-up: %v %v", held, err)
	}
	close(release)
	wg.Wait()
	if lateAttempted || !errors.Is(lateErr, ErrOperationRetired) {
		t.Fatalf("the late attempt = %v %v, want refused (retired)", lateAttempted, lateErr)
	}
	if len(node.txs) != 0 {
		t.Fatal("the late attempt was broadcast")
	}
	if _, _, err := other.Send(ctx, callTo(56, callA, "aa", 3)); err != nil {
		t.Fatalf("a retry begun after the give-up: %v", err)
	}
	if len(node.txs) != 1 {
		t.Fatalf("broadcasts = %d, want the retry only", len(node.txs))
	}
}

func TestKeyLane_LateAttemptRefusedAfterGiveUp_SQLite(t *testing.T) {
	testLateAttemptRefusedAfterGiveUp(t, sqliteLaneStore(t))
}

func TestKeyLane_LateAttemptRefusedAfterGiveUp_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "key_lane_late_", &models.KeyLaneNonce{}, &models.KeyLaneTx{}, &models.KeyLaneState{}, &models.KeyLaneRetiredOp{})
	testLateAttemptRefusedAfterGiveUp(t, &GormKeyLaneStore{DB: db})
}

// Item 1: an intent's top-up claim is given back only while it still records
// the cancelled top-up — a newer hash is never cleared.
func TestSellIntent_ClearCancelledTopUpIsConditional(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE ramp_sell_intents (id text primary key, topped_up_at datetime, top_up_tx_hash text, top_up_wei text,
		dest_topped_up_at datetime, dest_top_up_tx_hash text, dest_top_up_wei text, updated_at datetime)`).Error; err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	db.Exec(`INSERT INTO ramp_sell_intents (id, topped_up_at, top_up_tx_hash, dest_topped_up_at, dest_top_up_tx_hash) VALUES (?, ?, '0xnew', ?, '0xdnew')`, id, time.Now(), time.Now())
	svc := &SellIntentService{db: db}
	if ok, err := svc.ClearCancelledTopUp(context.Background(), id, "0xold"); err != nil || ok {
		t.Fatalf("clear on a stale hash = %v %v, want refused", ok, err)
	}
	if ok, err := svc.ClearCancelledDestTopUp(context.Background(), id, "0xdold"); err != nil || ok {
		t.Fatalf("dest clear on a stale hash = %v %v, want refused", ok, err)
	}
	if ok, err := svc.ClearCancelledTopUp(context.Background(), id, "0xnew"); err != nil || !ok {
		t.Fatalf("clear on the recorded hash = %v %v, want cleared", ok, err)
	}
}

// Item 3: an ordinary trade's release whose outcome is unknown stays
// releasing (in flight) — never rolled back.
func TestAutoRelease_UnknownOutcomeKeepsTheTradeReleasing(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_u"); err != nil {
		t.Fatal(err)
	}
	s := &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x1111111111111111111111111111111111111111"},
		err: outcomeUnknown("", errors.New("driver: bad connection"))}
	if _, err := svc.AutoRelease(context.Background(), trade.ID, s); !errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("err = %v", err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusReleasing {
		t.Fatalf("status = %s, want releasing", got.Status)
	}
}

// Item 4: a relay committed but not yet accepted returns its hash to the
// client (in flight, budget and slot kept), and the same request retried
// reuses the allocation — never a second relay.
func TestRelayer_InFlightRelayReturnsItsHashAndRetriesReuseIt(t *testing.T) {
	r := newRig(t)
	r.signer.mu.Lock()
	r.signer.err = broadcastUncertainError{err: errors.New("read: i/o timeout"), txHash: "0xinflight"}
	r.signer.mu.Unlock()
	h, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if err != nil || h != "0xinflight" {
		t.Fatalf("in-flight relay = %q %v, want its hash and no error", h, err)
	}
	r.signer.mu.Lock()
	r.signer.err = nil
	r.signer.mu.Unlock()
	h2, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if err != nil || h2 != "0xinflight" || r.signer.calls() != 1 {
		t.Fatalf("retry = %q %v broadcasts=%d, want the same relay, sent once", h2, err, r.signer.calls())
	}
}

// Item 6: hold adoption, persistence and watches from several goroutines
// (processes) race on nothing (run under -race).
func TestKeyLane_HoldStateIsRaceFree(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	p1 := durableLane(t, store, key, node, &clock)
	p2 := durableLane(t, store, key, node, &clock)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); p1.hold(p1.Address(), 9, "race") }()
		go func() { defer wg.Done(); _ = p2.Watch(context.Background()) }()
		go func() { defer wg.Done(); _ = p1.ClearHold(context.Background()); _, _, _ = p2.Held() }()
	}
	wg.Wait()
}

// Item 7: a hold the chain has passed is dropped before any re-persist, and
// a hold an admin cleared elsewhere is never re-persisted.
func TestKeyLane_WatchNeverRepersistsAPassedOrClearedHold(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	store.failLock.Store(1)
	lane.hold(lane.Address(), 3, "passed already") // persist fails; the chain is past 3
	if err := lane.Watch(context.Background()); err != nil {
		t.Fatalf("Watch = %v, want the passed hold dropped", err)
	}
	if st, _ := store.Hold(context.Background(), 146, lane.Address()); st != nil && st.HeldNonce != nil {
		t.Fatalf("a passed hold was re-persisted: %+v", st)
	}
	other := durableLane(t, store, key, node, &clock)
	store.failLock.Store(1)
	lane.hold(lane.Address(), 9, "unpersisted") // persist fails again
	other.hold(other.Address(), 9, "x")
	if err := other.ClearHold(context.Background()); err != nil { // an admin's clear elsewhere
		t.Fatal(err)
	}
	_ = lane.Watch(context.Background())
	if st, _ := store.Hold(context.Background(), 146, lane.Address()); st != nil && st.HeldNonce != nil {
		t.Fatalf("a hold cleared elsewhere was re-persisted: %+v", st)
	}
	if _, _, held := lane.Held(); held {
		t.Fatal("the cleared hold must be dropped locally")
	}
}

// Item 8: a recorded cancel the chain no longer shows (a reorg landed the
// original) falls through to the call's signings: the mined original answers.
func TestCallOutcome_UnprovenCancelFollowsTheMinedCall(t *testing.T) {
	node := newChainNode(137)
	kl, _, h := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		h, _, _ := kl.Send(context.Background(), callTo(137, callA, "aa", 0))
		return h
	})
	node.mu.Lock()
	for k := range node.receipts {
		delete(node.receipts, k)
	}
	node.mu.Unlock()
	node.mine(common.HexToHash(h), 100, 120)
	mined, cancelled, err := kl.CallOutcome(context.Background(), h, receiptExists(node))
	if err != nil || cancelled || !strings.EqualFold(mined, h) {
		t.Fatalf("outcome = %s %v %v, want the original, not cancelled", mined, cancelled, err)
	}
}

// Item 9: a COMMIT error re-check finds the allocation by its nonce and id
// even when its operation key is already gone (mined at once).
func TestKeyLane_RecheckFindsTheAllocationByNonce(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(56)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	store.afterCommit = func() {
		_ = store.GormKeyLaneStore.WithLock(context.Background(), 56, lane.Address(), func(q KeyLaneTxn) error { return q.MarkMined(6) })
	}
	store.failCommit.Store(true)
	h, attempted, err := lane.Send(WithOperationRef(context.Background(), "it_op:R9"), callTo(56, callA, "aa", 5))
	if !attempted || h == "" || errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("commit error, op key gone: %q %v %v, want the allocation found by nonce (in flight / done)", h, attempted, err)
	}
}
