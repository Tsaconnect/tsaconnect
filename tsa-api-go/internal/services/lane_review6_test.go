package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Regression tests for the sixth key-lane review.

// Item 1: a system release that mined and REVERTED is never rolled back to
// paid (the recovery sweep would release it again, forever): shallow it is
// left alone; at settle depth it is parked releasing, flagged, alerted, and
// AutoRelease refuses it from then on.
func TestAutoRelease_RevertedReleaseIsParkedNotRolledBack(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_rv"); err != nil {
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
	node.mine(common.HexToHash(h), 10, 11) // 2 deep: not final
	_ = lane.Watch(ctx)
	esc.verifyReleasedErr = errors.New("execution reverted")
	s := &reusedReleaseSender{fakeReleaseSigner: fakeReleaseSigner{addr: lane.Address(), hash: h}, lane: lane}
	_, _ = svc.AutoRelease(ctx, trade.ID, s)
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusReleasing || got.ReleaseRevertedAt != nil {
		t.Fatalf("shallow revert: %s reverted_at=%v, want releasing, not yet parked", got.Status, got.ReleaseRevertedAt)
	}
	node.mine(common.HexToHash(h), 10, 10+keyLaneCancelDepth) // settle depth
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("status", models.P2PTradeStatusPaid)
	_, _ = svc.AutoRelease(ctx, trade.ID, s)
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusReleasing || got.ReleaseRevertedAt == nil || !strings.EqualFold(got.ReleaseTxHash, h) {
		t.Fatalf("deep revert: %s reverted_at=%v hash=%q, want parked releasing with the hash", got.Status, got.ReleaseRevertedAt, got.ReleaseTxHash)
	}
	if _, err := svc.AutoRelease(ctx, trade.ID, s); err == nil {
		t.Fatal("a parked reverted release must never be released again by the system")
	}
}

// Item 2: a pre-lane refusal whose related call (the approve) is committed
// is in flight — ErrPendingBroadcast with NO hash, never success with the
// approve's hash.
func TestExecuteRoute_RefusalWithACommittedApproveIsInFlightWithoutItsHash(t *testing.T) {
	tr, lane, _ := treasuryLaneOnNode(t)
	op := "ramp_leg:R2"
	if _, _, err := lane.send(WithOperationRef(context.Background(), op+":approve"), func() ([]byte, error) {
		return signableTransfers{}.PrepareERC20Transfer(treasuryTestToken, "", "", big.NewInt(1))
	}); err != nil {
		t.Fatal(err)
	}
	tr.SetKillSwitch(true)
	h, attempted, err := tr.ExecuteRoute(WithOperationRef(context.Background(), op), lane, "USDC", big.NewInt(1), validRoute())
	if !attempted || h != "" || !errors.Is(err, ErrPendingBroadcast) {
		t.Fatalf("= %q %v %v, want in flight without the approve's hash", h, attempted, err)
	}
}

// Item 3: a bridge leg left pending with no hash (an unknown outcome) is
// recovered after the window: nothing committed → freed for a new attempt;
// a committed route → its hash adopted onto the leg.
func TestHashlessLeg_IsFreedOrAdopted(t *testing.T) {
	for _, committed := range []bool{false, true} {
		r := newConversionRig(t)
		leg := r.seedLeg(t, &models.RampLeg{OrderID: r.order.ID, Kind: models.RampLegKindBridge, Attempt: 1,
			ChainKey: "base", Token: "USDC", Amount: "100000000", Status: models.RampLegStatusPending})
		if committed {
			if _, _, err := r.src.lane.send(WithOperationRef(context.Background(), "ramp_leg:"+leg.ID.String()+":route"), func() ([]byte, error) {
				return r.src.builder.BuildUnsignedCall(r.src.lane.Signer.Address(), treasuryTestRouter, []byte{1}, big.NewInt(0), 100_000)
			}); err != nil {
				t.Fatal(err)
			}
		}
		r.clock = r.clock.Add(hashlessRecoverAfter + time.Minute)
		_ = r.svc.Advance(context.Background(), r.reload(t))
		got := r.legsOfKind(t, models.RampLegKindBridge)[0]
		switch {
		case committed && (got.Status != models.RampLegStatusPending || got.TxHash != "0xsponsored"):
			t.Fatalf("committed route: leg %s tx=%q, want the route's hash adopted", got.Status, got.TxHash)
		case !committed && got.Status != models.RampLegStatusFailed:
			t.Fatalf("nothing committed: leg %s, want freed", got.Status)
		}
	}
}

// Item 4: an IT op answered "not sent" leaves a (sequenced) tombstone: an
// attempt begun before that answer can never commit after it.
func TestOperatorNotSent_LeavesATombstone(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	svc := NewMCGPSwapService(nil, nil)
	svc.SetOperatorLane(lane)
	svc.operatorVerified.Store(true)
	ref := [32]byte{4}
	data, err := svc.pack("settleDebit", ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.OperatorSettleDebit(context.Background(), ref); !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("setup: %v, want not sent (no chain client)", err)
	}
	op := "it_op:" + crypto.Keccak256Hash(data).Hex()
	if seq, err := store.RetiredSeq(context.Background(), 146, lane.Address(), op); err != nil || seq != 1 {
		t.Fatalf("tombstone seq = %d %v, want 1", seq, err)
	}
}

// Item 5: an admin's retry clears the hold with an ADMIN clear (the version
// every process adopts).
func TestKeyLane_AdminActionsBumpTheClearVersion(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	if _, _, err := lane.Send(context.Background(), callTo(146, callA, "aa", 5)); err != nil {
		t.Fatal(err)
	}
	lane.hold(lane.Address(), 5, "cap")
	if err := lane.RetrySpeedUp(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Hold(context.Background(), 146, lane.Address())
	if st == nil || st.ClearVersion != 1 || st.HeldNonce != nil {
		t.Fatalf("state = %+v, want cleared by an admin (clear version 1)", st)
	}
}

// Item 6: permanence comes only from the permanent column, and a later
// sequenced give-up never downgrades it.
func TestKeyLane_PermanentTombstoneIsNeverDowngraded(t *testing.T) {
	store := sqliteLaneStore(t)
	node := newChainNode(56)
	node.mined = 3
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	ctx := context.Background()
	lane.GiveUp(ctx, lane.Address(), "op:x", "", RetirePermanent)
	lane.GiveUp(ctx, lane.Address(), "op:x", "", RetireSequenced)
	if _, attempted, err := lane.Send(WithOperationRef(ctx, "op:x"), callTo(56, callA, "aa", 3)); attempted || !errors.Is(err, ErrOperationRetired) {
		t.Fatalf("= %v %v, want still refused for good", attempted, err)
	}
}

// Item 7: the sale hook writes only a leg with no hash yet — a leg that
// already records one makes the allocation roll back: nothing is signed.
func TestSendSale_HookRefusesALegThatAlreadyRecordsAHash(t *testing.T) {
	r := newDeliverRig(t)
	node := newChainNode(8453)
	node.mined = 7
	clock := time.Now()
	rawSaleLane(t, r, node, &clock)
	// Whatever wrote a hash between the reserve and the send (here a trigger).
	if err := r.s.db.Exec(`CREATE TRIGGER pre_hash AFTER INSERT ON ramp_legs BEGIN UPDATE ramp_legs SET tx_hash = '0xpre' WHERE id = NEW.id; END`).Error; err != nil {
		t.Fatal(err)
	}
	_ = r.s.SendTreasuryBuy(context.Background(), r.intent.ID)
	if len(node.txs) != 0 {
		t.Fatalf("%d broadcasts: a leg recording a hash must never get a second send", len(node.txs))
	}
}

// Item 8: a request that loses the conditional clear writes no tombstone.
func TestTopUpForIntent_LosingTheClearWritesNoTombstone(t *testing.T) {
	g, user, rig := newIntentTopUpRigWith(t)
	node := newChainNode(8453)
	var h string
	kl, raw, _ := cancelledLane(t, node, func(kl *KeyLane, raw *LocalKeyReleaseSigner) string {
		h, _, _ = kl.Send(context.Background(), callTo(8453, callA, "aa", 0))
		return h
	})
	rig.lane.Signer, rig.lane.KeyLane = raw, kl
	intent := intentOn(8453, user)
	intent.TopUpTxHash = h
	store := &fakeIntentTopUps{recorded: map[uuid.UUID][2]string{intent.ID: {"0xother", "1"}}}
	if _, err := g.TopUpForIntent(context.Background(), user, intent, store); !errors.Is(err, ErrSponsorshipInProgress) {
		t.Fatalf("err = %v", err)
	}
	if seq, _ := kl.store.RetiredSeq(context.Background(), kl.chainID, raw.Address(), "gas_topup_intent:"+intent.ID.String()); seq != 0 {
		t.Fatalf("tombstone seq = %d, want none from the request that lost the clear", seq)
	}
}

// Item 9: a retry of a committed relay is answered from its allocation
// before the rate limit, budget or simulation — never a 429 for it.
func TestRelayer_RetryOfACommittedRelayBypassesTheRateLimit(t *testing.T) {
	r := newRig(t, func(l *RelayerLimits) { l.PerUserPerWindow = 1 })
	req := r.req()
	h, err := r.svc.Relay(context.Background(), userAddr, req)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := r.svc.Relay(context.Background(), userAddr, req)
	if err != nil || h2 != h || r.signer.calls() != 1 {
		t.Fatalf("retry = %q %v (broadcasts %d), want the committed relay, no 429", h2, err, r.signer.calls())
	}
}

// Item 10: every key of a refusal is probed under ONE lane lock.
func TestGiveUpRefused_ProbesAllKeysUnderOneLock(t *testing.T) {
	store := &raceStore{GormKeyLaneStore: sqliteLaneStore(t)}
	node := newChainNode(146)
	clock := time.Now()
	lane := durableLane(t, store, newEcdsaKey(), node, &clock)
	store.locks.Store(0)
	_, attempted, err := lane.giveUpRefused(context.Background(), lane.Address(), errors.New("refused"), []string{"a", "a:route"}, []string{"a:approve"})
	if attempted || err == nil || store.locks.Load() != 1 {
		t.Fatalf("= %v %v locks=%d, want not sent after one lock", attempted, err, store.locks.Load())
	}
}

var _ = types.ReceiptStatusFailed
