package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// Regression tests for the textbook outbox (final review of the key lane):
// the caller's pre-broadcast write commits with the signed call, a committed
// call is its nonce's transaction until mined, and nothing ever fills or
// replaces it.

// A sale whose broadcast the node refused is still in flight (committed, the
// lane keeps sending it): the leg keeps its hash and stays pending, so an
// operator's refund is refused and no second sale is sent — she can never be
// paid twice.
func TestSendSale_RefusedBroadcastStaysInFlightSoARefundCannotPayTwice(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	node := newChainNode(8453)
	node.refuse = func(*types.Transaction, int) error {
		return errors.New("insufficient funds for gas * price + value")
	}
	lane := r.s.companySale().wallet.Treasury().Lane("base")
	lane.Signer = NewLocalKeyReleaseSigner(newEcdsaKey().k, node)

	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	legs := r.legs(t)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusPending || legs[0].TxHash == "" || legs[0].Nonce == nil || *legs[0].Nonce != 7 {
		t.Fatalf("legs = %+v, want one pending leg carrying the committed signing's hash and nonce 7", legs)
	}
	if got := node.atNonce(7); len(got) != 1 || !strings.EqualFold(got[0].Hash().Hex(), legs[0].TxHash) {
		t.Fatalf("the leg's hash must be the refused signing's: %v vs %s", got, legs[0].TxHash)
	}
	if _, err := r.s.MarkStableBuyRefunded(ctx, uuid.New(), r.intent.ID, "customer asked"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("refund while the sale is in flight = %v, want ErrITBadState", err)
	}
	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if legs := r.legs(t); len(legs) != 1 {
		t.Fatalf("%d sale legs, want the one in flight", len(legs))
	}
	for _, tx := range node.txs {
		if tx.Nonce() != 7 {
			t.Fatalf("a second sale was signed at nonce %d", tx.Nonce())
		}
	}
}

// A slow pre-broadcast hook holds the allocation's transaction open: no other
// process can see, take or fill that nonce meanwhile. When it commits, the
// nonce carries its call; the other process's call goes above it.
func testSlowHookIsNeverReplacedByAFiller(t *testing.T, store KeyLaneStore) {
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock1, clock2 := time.Now(), time.Now().Add(time.Hour) // the other process is long past every grace
	p1 := durableLane(t, store, key, node, &clock1)
	p2 := durableLane(t, store, key, node, &clock2)
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var hookErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, hookErr = p1.SendRecorded(context.Background(), callTo(146, callA, "aa", 5), func(context.Context, uint64, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	wg.Add(2)
	go func() { defer wg.Done(); _ = p2.Watch(context.Background()) }()
	go func() {
		defer wg.Done()
		if _, _, err := p2.Send(context.Background(), callTo(146, callB, "bb", 5)); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	node.mu.Lock()
	early := len(node.txs)
	node.mu.Unlock()
	close(release)
	wg.Wait()
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if early != 0 {
		t.Fatalf("%d broadcasts while the hook's transaction was open", early)
	}
	if got := node.atNonce(5); len(got) != 1 || *got[0].To() != callA {
		t.Fatalf("nonce 5 = %v, want only the hooked call", got)
	}
	if got := node.atNonce(6); len(got) != 1 || *got[0].To() != callB {
		t.Fatalf("nonce 6 = %v, want the other process's call", got)
	}
	for _, tx := range node.txs {
		if strings.EqualFold(tx.To().Hex(), p1.Address()) {
			t.Fatalf("a filler was signed at nonce %d", tx.Nonce())
		}
	}
	if _, _, held := p2.Held(); held {
		t.Fatal("an uncommitted allocation is not a foreign hole")
	}
}

func TestKeyLane_SlowHookIsNeverReplacedByAFiller_SQLite(t *testing.T) {
	testSlowHookIsNeverReplacedByAFiller(t, sqliteLaneStore(t))
}

func TestKeyLane_SlowHookIsNeverReplacedByAFiller_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "key_lane_slow_hook_", &models.KeyLaneNonce{}, &models.KeyLaneTx{}, &models.KeyLaneState{})
	testSlowHookIsNeverReplacedByAFiller(t, &GormKeyLaneStore{DB: db})
}

// A retried operation is told the truth: in flight (ErrPendingBroadcast, with
// the hash) until a node accepted a signing of it — never "sent" before — and
// then the accepted hash, without error. Never a second nonce.
func TestKeyLane_RetrySeesHonestInFlightStatus(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(56)
	node.mined = 2
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	op := WithOperationRef(context.Background(), "p2p_release:T9")
	node.refuse = func(*types.Transaction, int) error { return errors.New("context deadline exceeded") } // never reached a node
	h, attempted, err := lane.Send(op, callTo(56, callA, "aa", 2))
	if !attempted || h == "" || !errors.Is(err, ErrPendingBroadcast) {
		t.Fatalf("first: %q %v %v, want in flight", h, attempted, err)
	}
	for i := 0; i < 2; i++ {
		got, attempted, err := lane.Send(op, callTo(56, callA, "aa", 2))
		if got != h || !attempted || !errors.Is(err, ErrPendingBroadcast) || errors.Is(err, ErrTxNotBroadcast) {
			t.Fatalf("retry %d: %q %v %v, want %s still pending", i, got, attempted, err, h)
		}
	}
	node.mu.Lock()
	node.refuse = nil
	node.mu.Unlock()
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := lane.Watch(context.Background()); err != nil { // re-broadcasts the same bytes
		t.Fatal(err)
	}
	got, _, err := lane.Send(op, callTo(56, callA, "aa", 2))
	if err != nil || got != h {
		t.Fatalf("after acceptance: %q %v, want %s and no error", got, err, h)
	}
	var allocs int64
	store.DB.Model(&models.KeyLaneNonce{}).Count(&allocs)
	if allocs != 1 {
		t.Fatalf("%d allocations for one operation", allocs)
	}
}

// Every allocation update names the version it read and never touches a
// mined row: a stale copy (an outcome recorded late, an admin retry) can
// never write a mined allocation back to live.
func testMinedStatusIsNeverResurrected(t *testing.T, store KeyLaneStore) {
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	h5, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.Get(ctx, 146, lane.Address(), 5)
	if err != nil || stale == nil {
		t.Fatalf("read: %v %v", stale, err)
	}
	// Two writers on one version: only the first wins.
	err = store.WithLock(ctx, 146, lane.Address(), func(q KeyLaneTxn) error {
		a, b := *stale, *stale
		a.ActedAt = clock
		if err := q.Save(&a); err != nil {
			return err
		}
		if err := q.Save(&b); !errors.Is(err, errStaleAllocation) {
			t.Errorf("a second write on the same version = %v, want errStaleAllocation", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	node.mu.Lock()
	node.mined = 6
	node.mu.Unlock()
	if err := lane.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, _ := store.Get(ctx, 146, lane.Address(), 5)
	err = store.WithLock(ctx, 146, lane.Address(), func(q KeyLaneTxn) error {
		back := *fresh
		back.Status, back.Bumps = models.KeyLaneNonceSent, 9
		return q.Save(&back)
	})
	if !errors.Is(err, errStaleAllocation) {
		t.Fatalf("writing a mined allocation back = %v, want errStaleAllocation", err)
	}
	txs, _ := store.Signings(ctx, stale.ID)
	lane.recordOutcome(ctx, lane.Address(), stale.ID, txs[0].ID, nil) // a late outcome
	if err := lane.RetrySpeedUp(ctx, 5); err == nil {
		t.Fatal("an admin retry on a mined nonce must be refused")
	}
	if _, err := lane.RaisePriceCap(ctx, 5, big.NewInt(5e9)); err == nil {
		t.Fatal("a price-cap raise on a mined nonce must be refused")
	}
	final, _ := store.Get(ctx, 146, lane.Address(), 5)
	if final.Status != models.KeyLaneNonceMined || final.Bumps != 0 || final.OpKey != "" {
		t.Fatalf("allocation after stale writes: status=%s bumps=%d, want mined, untouched", final.Status, final.Bumps)
	}
	if lane.FinalHash(ctx, h5) != strings.ToLower(h5) {
		t.Fatal("the mined call's hash moved")
	}
}

func TestKeyLane_MinedStatusIsNeverResurrected_SQLite(t *testing.T) {
	testMinedStatusIsNeverResurrected(t, sqliteLaneStore(t))
}

func TestKeyLane_MinedStatusIsNeverResurrected_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "key_lane_mined_", &models.KeyLaneNonce{}, &models.KeyLaneTx{}, &models.KeyLaneState{})
	testMinedStatusIsNeverResurrected(t, &GormKeyLaneStore{DB: db})
}

// A lane held at the speed-up fee cap is recoverable without touching the
// bump count alone: a super-admin raises THAT allocation's price cap, the
// hold clears, and the upkeep re-prices the SAME call above the old cap.
func TestKeyLane_CappedLaneRecoversViaAdminPriceCapRaise(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	h5, _, err := lane.Send(ctx, func() ([]byte, error) {
		return jsonTx(blockchain.UnsignedTx{Nonce: 5, GasPrice: "2900000000", GasLimit: 50_000, To: callA.Hex(), Value: "0", Data: "aa", ChainID: "146"})
	})
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
	if err := lane.Watch(ctx); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("Watch = %v, want held at the fee cap (3 × 1 gwei)", err)
	}
	if _, why, _ := lane.Held(); !strings.Contains(why, "fee cap") {
		t.Fatalf("hold reason %q", why)
	}
	if _, err := lane.RaisePriceCap(ctx, 5, big.NewInt(2_900_000_000)); err == nil {
		t.Fatal("a cap not above the existing signing must be refused")
	}
	if _, err := lane.RaisePriceCap(ctx, 5, new(big.Int).Mul(keyLaneFeeCeiling(146), big.NewInt(11))); err == nil {
		t.Fatal("a cap above 10× the chain's ceiling must be refused")
	}
	prev, err := lane.RaisePriceCap(ctx, 5, big.NewInt(5_000_000_000))
	if err != nil || prev != "" {
		t.Fatalf("raise: prev=%q err=%v", prev, err)
	}
	if _, _, held := lane.Held(); held {
		t.Fatal("the raise clears the hold")
	}
	if err := lane.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	at5 := node.atNonce(5)
	last := at5[len(at5)-1]
	if *last.To() != callA || common.Bytes2Hex(last.Data()) != "aa" {
		t.Fatal("the re-priced signing must carry the same call")
	}
	if last.GasPrice().Cmp(big.NewInt(3_000_000_000)) <= 0 || last.GasPrice().Cmp(big.NewInt(5_000_000_000)) > 0 {
		t.Fatalf("re-priced at %s, want above the old 3 gwei cap and within the raised 5 gwei", last.GasPrice())
	}
	if lane.FinalHash(ctx, h5) != last.Hash().Hex() {
		t.Fatal("the call's final hash must follow the re-priced signing")
	}
}

// The watch job's speed-up passes the same fee gate as a send, reservations
// included: it never spends what owed work needs. Underfunded is never a
// hold — once the key is topped up the next tick re-arms by itself.
func TestKeyLane_WatchSpeedUpRespectsTheReservation(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	lane.AddReservation("pool_releases", func(context.Context) (uint64, error) { return 300_000, nil })
	ctx := context.Background()
	h5, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5))
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
	// A speed-up alone (≈1.13e14 with the buffer) fits; with the 300k-unit
	// reservation at its price (≈3.4e14) it does not.
	node.balance = big.NewInt(200_000_000_000_000)
	node.mu.Unlock()
	for i := 0; i < 2; i++ {
		clock = clock.Add(keyLaneGapGrace + time.Second)
		if err := lane.Watch(ctx); err != nil {
			t.Fatalf("underfunded is never a hold: %v", err)
		}
	}
	for _, tx := range node.atNonce(5) {
		if tx.Hash() != common.HexToHash(h5) {
			t.Fatalf("a speed-up at %s was signed into the reservation", tx.GasPrice())
		}
	}
	node.mu.Lock()
	node.balance = big.NewInt(1_000_000_000_000_000)
	node.mu.Unlock()
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := lane.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	at5 := node.atNonce(5)
	if last := at5[len(at5)-1]; last.Hash() == common.HexToHash(h5) || *last.To() != callA {
		t.Fatal("once funded, the next tick must speed up the same call")
	}
}

// A sale the key lane sped up once mines under the REPLACEMENT's hash: the
// finalize verifies whichever signing of the same call mined, records that
// hash on the leg and settles — never a stall on the original's missing
// receipt.
func TestFinalizeTreasuryBuy_SpedUpSaleVerifiesTheReplacement(t *testing.T) {
	r := newDeliverRig(t)
	ctx := context.Background()
	node := newChainNode(8453)
	node.mined = 7
	raw := NewLocalKeyReleaseSigner(newEcdsaKey().k, node)
	tl := r.s.companySale().wallet.Treasury().Lane("base")
	tl.Signer = raw
	clock := time.Now()
	kl := soloLane(raw, nil)
	kl.pricer, kl.nonces, kl.lookup = node, node, node
	kl.now = func() time.Time { return clock }
	tl.KeyLane = kl

	if err := r.s.SendTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	original := r.legs(t)[0].TxHash
	node.forget(common.HexToHash(original)) // evicted, and refused when re-sent
	node.mu.Lock()
	node.refuse = func(tx *types.Transaction, _ int) error {
		if tx.Hash() == common.HexToHash(original) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	node.mu.Unlock()
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if err := kl.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	at7 := node.atNonce(7)
	replacement := at7[len(at7)-1].Hash()
	if replacement == common.HexToHash(original) {
		t.Fatal("setup: no speed-up")
	}

	r.probe.noReceipt = map[common.Hash]bool{common.HexToHash(original): true} // only the replacement mined
	r.verifier.credited, r.verifier.err = big.NewInt(7_278_150), nil
	if err := r.s.FinalizeTreasuryBuy(ctx, r.intent.ID); err != nil {
		t.Fatalf("finalize of a sped-up sale: %v, want settled", err)
	}
	if !strings.EqualFold(r.verifier.hash, replacement.Hex()) {
		t.Fatalf("verified %s, want the mined replacement %s", r.verifier.hash, replacement.Hex())
	}
	leg := r.legs(t)[0]
	if !strings.EqualFold(leg.TxHash, replacement.Hex()) || leg.Status != models.RampLegStatusConfirmed {
		t.Fatalf("leg tx=%s status=%s, want the replacement recorded and confirmed", leg.TxHash, leg.Status)
	}
	if got := reloadIntent(t, r.s, r.intent.ID); got.Status != models.ITStatusSettled || !strings.EqualFold(got.TxHash, replacement.Hex()) {
		t.Fatalf("intent status=%s tx=%s, want settled on the replacement", got.Status, got.TxHash)
	}
}
