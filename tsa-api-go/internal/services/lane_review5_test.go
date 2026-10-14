package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Regression tests for the fifth key-lane review.

// A: a give-up whose retry is a NEW key retires the old one for good — no
// attempt of it, begun before or after, ever commits.
func TestKeyLane_PermanentGiveUpRefusesEveryLaterAttempt(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(56)
	node.mined = 3
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	if !lane.GiveUp(ctx, lane.Address(), "gas_topup:row1", "", RetirePermanent) {
		t.Fatal("give-up refused")
	}
	if _, attempted, err := lane.Send(WithOperationRef(ctx, "gas_topup:row1"), callTo(56, callA, "aa", 3)); attempted || !errors.Is(err, ErrOperationRetired) {
		t.Fatalf("an attempt after a permanent give-up = %v %v, want refused", attempted, err)
	}
	if len(node.txs) != 0 {
		t.Fatal("broadcast after a permanent give-up")
	}
}

// B: every pre-broadcast hook updates exactly one row under its guard —
// a second write (the row already records a transaction) fails, so the
// allocation rolls back and nothing signs.
func TestHooks_UpdateExactlyOneGuardedRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.RampLeg{}, &models.GasSponsorship{}); err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE ramp_sell_intents (id text primary key, topped_up_at datetime, top_up_tx_hash text, top_up_wei text,
		dest_topped_up_at datetime, dest_top_up_tx_hash text, dest_top_up_wei text, updated_at datetime)`)
	ctx := context.Background()
	legs := &GormRampLegStore{DB: db}
	leg := &models.RampLeg{OrderID: uuid.New(), Kind: models.RampLegKindSale, Attempt: 1, Amount: "1", Status: models.RampLegStatusPending, CreatedAt: time.Now()}
	db.Create(leg)
	if err := legs.SetTxHash(ctx, leg.ID, "0x1", ""); err != nil {
		t.Fatal(err)
	}
	if err := legs.SetTxHash(ctx, leg.ID, "0x2", ""); err == nil {
		t.Fatal("a leg that records a transaction must refuse a second")
	}
	sp := &GormSponsorshipStore{DB: db}
	row := &models.GasSponsorship{ID: uuid.New(), UserID: uuid.New(), OrderID: uuid.New(), Kind: models.GasSponsorshipKindTopUp, ChainKey: "bsc", Token: "USDT", Wallet: "0x1"}
	db.Create(row)
	if err := sp.SetTxHash(ctx, row.ID, "0x1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := sp.SetTxHash(ctx, row.ID, "0x2", "1"); err == nil {
		t.Fatal("a sponsorship that records a transaction must refuse a second")
	}
	si := &SellIntentService{db: db}
	id := uuid.New()
	db.Exec(`INSERT INTO ramp_sell_intents (id) VALUES (?)`, id)
	if err := si.RecordTopUp(ctx, id, "0x1", "1"); err == nil {
		t.Fatal("an unclaimed intent must refuse a top-up record")
	}
	db.Exec(`UPDATE ramp_sell_intents SET topped_up_at = ? WHERE id = ?`, time.Now(), id)
	if err := si.RecordTopUp(ctx, id, "0x1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := si.RecordTopUp(ctx, id, "0x2", "1"); err == nil {
		t.Fatal("a recorded top-up must refuse a second")
	}
}

// Item 1: the IT give-up names the operation's key (it_op:<call hash>) and
// retires it for good.
func TestOperatorCallCancelled_RetiresTheOperationKey(t *testing.T) {
	node := newChainNode(146)
	op := "it_op:" + crypto.Keccak256Hash(common.FromHex("aa")).Hex()
	kl, _, h := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		h, _, _ := kl.Send(WithOperationRef(context.Background(), op), callTo(146, callA, "aa", 0))
		return h
	})
	svc := NewMCGPSwapService(nil, nil)
	svc.SetOperatorLane(kl)
	if c, err := svc.OperatorCallCancelled(context.Background(), h); err != nil || !c {
		t.Fatalf("cancelled = %v %v", c, err)
	}
	if _, attempted, err := kl.Send(WithOperationRef(context.Background(), op), callTo(146, callA, "aa", 0)); attempted || !errors.Is(err, ErrOperationRetired) {
		t.Fatalf("the cancelled op again = %v %v, want refused (retired)", attempted, err)
	}
}

// Item 3: a treasury refusal taken before the lane (the kill switch) for an
// operation that already holds an allocation answers in flight.
func TestTreasury_PreLaneRefusalOfACommittedOperationIsInFlight(t *testing.T) {
	tr, lane, _ := treasuryLaneOnNode(t)
	ctx := WithOperationRef(context.Background(), "ramp_leg:K3")
	h, _, err := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.SetKillSwitch(true)
	got, attempted, err := tr.TransferTokenRecorded(ctx, lane, "USDC", testRoutedEVM, big.NewInt(1), nil)
	if !attempted || !strings.EqualFold(got, h) || errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("killed, committed op = %q %v %v, want its allocation in flight", got, attempted, err)
	}
}

// Item 4: a relay in flight with no hash known is never a success with an
// empty hash: it is ErrRelayInProgress naming the operation.
func TestRelayer_InFlightWithoutAHashIsInProgress(t *testing.T) {
	r := newRig(t)
	r.signer.mu.Lock()
	r.signer.err = errors.New("read: i/o timeout") // uncertain, no hash
	r.signer.mu.Unlock()
	h, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if h != "" || !errors.Is(err, ErrRelayInProgress) || !strings.Contains(err.Error(), "relay:0x") {
		t.Fatalf("relay = %q %v, want in progress naming its op", h, err)
	}
}

// Item 5: a transient refusal (a held lane) answers not sent WITHOUT a
// tombstone — the caller's own retry is never refused.
func TestKeyLane_TransientRefusalLeavesNoTombstone(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := WithOperationRef(context.Background(), "p2p_release:T5")
	lane.hold(lane.Address(), 9, "transient")
	if _, attempted, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); attempted || !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("held = %v %v", attempted, err)
	}
	if seq, err := store.RetiredSeq(context.Background(), 146, lane.Address(), "p2p_release:T5"); err != nil || seq != 0 {
		t.Fatalf("tombstone seq = %d %v, want none", seq, err)
	}
	_ = lane.ClearHold(context.Background())
	if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); err != nil {
		t.Fatalf("the retry after a transient refusal: %v", err)
	}
}

// Item 6: a routine clear (not an admin's) never drops a hold whose persist
// failed — it is persisted again.
func TestKeyLane_RoutineClearKeepsAnUnpersistedHold(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	_ = store.GormKeyLaneStore.WithLock(context.Background(), 146, lane.Address(), func(q KeyLaneTxn) error {
		n := uint64(20)
		if err := q.SetHold(&n, "old", time.Now(), false); err != nil {
			return err
		}
		return q.SetHold(nil, "", time.Time{}, false) // a routine clear (e.g. the chain passed it)
	})
	store.failLock.Store(1)
	lane.hold(lane.Address(), 9, "genuine, persist failed")
	if err := lane.Watch(context.Background()); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("Watch = %v, want still held", err)
	}
	if st, _ := store.Hold(context.Background(), 146, lane.Address()); st == nil || st.HeldNonce == nil || *st.HeldNonce != 9 {
		t.Fatalf("shared hold = %+v, want re-persisted", st)
	}
}

// Item 8: a release that mined and REVERTED moved nothing: the give-up
// treats it as not executed and the trade rolls back as before.
func TestAutoRelease_RevertedReleaseRollsBack(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_r"); err != nil {
		t.Fatal(err)
	}
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	h, _, err := lane.Send(WithOperationRef(ctx, "p2p_release:"+trade.ID.String()), callTo(146, callA, "aa", 0))
	if err != nil {
		t.Fatal(err)
	}
	node.mu.Lock()
	node.mined = 1
	node.reverted = map[common.Hash]bool{common.HexToHash(h): true}
	node.mu.Unlock()
	node.mine(common.HexToHash(h), 10, 20)
	_ = lane.Watch(ctx)
	esc.verifyReleasedErr = errors.New("execution reverted")
	s := &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: lane.Address(), hash: h}, lane: lane}
	if _, err := svc.AutoRelease(ctx, trade.ID, s); err == nil {
		t.Fatal("want the verify error")
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusPaid {
		t.Fatalf("status = %s, want rolled back to paid after a reverted release", got.Status)
	}
}

// Item 9: a tombstone written before sequencing (seq 0) is permanent.
func TestKeyLane_LegacyTombstoneIsPermanent(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(56)
	node.mined = 3
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	if err := store.DB.Create(&models.KeyLaneRetiredOp{ChainID: 56, Address: strings.ToLower(lane.Address()), OpKey: "ramp_leg:old:route"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, attempted, err := lane.Send(WithOperationRef(context.Background(), "ramp_leg:old:route"), callTo(56, callA, "aa", 3)); attempted || !errors.Is(err, ErrOperationRetired) {
		t.Fatalf("legacy tombstone = %v %v, want refused for good", attempted, err)
	}
}

// Item 7: a cancelled intent top-up whose claim-clear matched nothing (the
// intent moved on meanwhile) is in progress — never a second top-up.
func TestTopUpForIntent_ClearMatchingNothingIsInProgress(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	node := newChainNode(8453)
	var h string
	kl, raw, _ := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		h, _, _ = kl.Send(WithOperationRef(context.Background(), "gas_topup_intent:x"), callTo(8453, callA, "aa", 0))
		return h
	})
	rig.lane.Signer, rig.lane.KeyLane = raw, kl
	intent := intentOn(8453, user)
	intent.TopUpTxHash = h
	store := &fakeIntentTopUps{recorded: map[uuid.UUID][2]string{intent.ID: {"0xsomething-else", "1"}}}
	if _, err := g.TopUpForIntent(context.Background(), user, intent, store); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("err = %v, want ErrSponsorshipInProgress", err)
	}
	if store.marks != 0 {
		t.Fatal("no new claim may be taken")
	}
}
