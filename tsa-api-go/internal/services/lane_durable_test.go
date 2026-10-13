package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// chainNode is a full lane broadcaster: it records every signed tx it is
// handed, can refuse one (refuse), knows what it accepted (unless forgotten),
// and reports a mined nonce and a gas price.
type chainNode struct {
	mu       sync.Mutex
	id       int64
	txs      []*types.Transaction
	known    map[common.Hash]bool
	refuse   func(tx *types.Transaction, attempt int) error
	attempts map[common.Hash]int
	mined    uint64
	minedErr error
	lookErr  error
	price    *big.Int
	balance  *big.Int
	// inLock, when set, reports whether a store lock is held right now: any
	// node call while it is counts as network I/O inside the lock.
	inLock     func() bool
	ioInLock   atomic.Int32
	pendingSeq []uint64 // PendingNonce answers, in order (last repeats)
	pendingN   int
}

func (n *chainNode) io() {
	if n.inLock != nil && n.inLock() {
		n.ioInLock.Add(1)
	}
}

func (n *chainNode) PendingNonce(context.Context, string) (uint64, error) {
	n.io()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.pendingSeq) == 0 {
		return n.mined, nil
	}
	i := n.pendingN
	if i >= len(n.pendingSeq) {
		i = len(n.pendingSeq) - 1
	}
	n.pendingN++
	return n.pendingSeq[i], nil
}

func newChainNode(id int64) *chainNode {
	return &chainNode{id: id, known: map[common.Hash]bool{}, attempts: map[common.Hash]int{}, price: big.NewInt(1e9), balance: big.NewInt(1e18)}
}

func (n *chainNode) ChainID() *big.Int { return big.NewInt(n.id) }
func (n *chainNode) SendRawTransaction(raw []byte) (string, error) {
	n.io()
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(raw, tx); err != nil {
		return "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.txs = append(n.txs, tx)
	n.attempts[tx.Hash()]++
	if n.refuse != nil {
		if err := n.refuse(tx, n.attempts[tx.Hash()]); err != nil {
			return "", err
		}
	}
	n.known[tx.Hash()] = true
	return tx.Hash().Hex(), nil
}
func (n *chainNode) TransactionKnown(_ context.Context, h string) (bool, error) {
	n.io()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lookErr != nil {
		return false, n.lookErr
	}
	return n.known[common.HexToHash(h)], nil
}
func (n *chainNode) LatestNonce(context.Context, string) (uint64, error) {
	n.io()
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mined, n.minedErr
}
func (n *chainNode) SuggestGasPrice() (*big.Int, error) { n.io(); return n.price, nil }
func (n *chainNode) NativeBalance(context.Context, string) (*big.Int, error) {
	n.io()
	n.mu.Lock()
	defer n.mu.Unlock()
	return new(big.Int).Set(n.balance), nil
}
func (n *chainNode) forget(h common.Hash) { n.mu.Lock(); delete(n.known, h); n.mu.Unlock() }
func (n *chainNode) atNonce(nonce uint64) []*types.Transaction {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*types.Transaction
	for _, tx := range n.txs {
		if tx.Nonce() == nonce {
			out = append(out, tx)
		}
	}
	return out
}

var (
	callA = common.HexToAddress("0x00000000000000000000000000000000000000aa")
	callB = common.HexToAddress("0x00000000000000000000000000000000000000bb")
)

// callTo is a priced call to `to` with data, built at pending nonce.
func callTo(chain int64, to common.Address, data string, nonce uint64) func() ([]byte, error) {
	return func() ([]byte, error) {
		return jsonTx(blockchain.UnsignedTx{Nonce: nonce, GasPrice: "1000000000", GasLimit: 50_000, To: to.Hex(), Value: "0",
			Data: data, ChainID: big.NewInt(chain).String()})
	}
}

func durableLane(t *testing.T, store KeyLaneStore, key *ecdsaKey, node *chainNode, clock *time.Time) *KeyLane {
	t.Helper()
	reg := NewSignerRegistry()
	reg.SetStore(store)
	lane := reg.Lane("pm_release", key.k, node)
	lane.now = func() time.Time { return *clock }
	return lane
}

type ecdsaKey struct{ k *ecdsa.PrivateKey }

func newEcdsaKey() *ecdsaKey { k, _ := crypto.GenerateKey(); return &ecdsaKey{k: k} }

func jsonTx(u blockchain.UnsignedTx) ([]byte, error) { return json.Marshal(u) }

// sqliteLaneStore is the durable GORM store on a private SQLite database
// (the lane lock emulated by a process mutex), shared by every "process"
// (registry) a test builds on it.
func sqliteLaneStore(t *testing.T) *GormKeyLaneStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.KeyLaneNonce{}, &models.KeyLaneTx{}); err != nil {
		t.Fatal(err)
	}
	return &GormKeyLaneStore{DB: db}
}

// statefulNode adds a consistent (balance, mined nonce) read.
type statefulNode struct {
	*chainNode
	stateMined uint64
}

func (n *statefulNode) NativeStateAt(context.Context, string) (*big.Int, uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return new(big.Int).Set(n.balance), n.stateMined, nil
}

// Owner's rule across a restart: process 1 signs call A at nonce 5 and its
// broadcast times out without reaching the node. Process 2 (same database)
// finds the allocation and, once the grace has passed, re-broadcasts A at 5 —
// the next call (B) goes to 6. No other call is ever signed at 5.
func TestKeyLane_RestartResendsTheSameCallNeverADifferentOne(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	node.refuse = func(tx *types.Transaction, attempt int) error {
		if tx.Nonce() == 5 && attempt == 1 {
			return errors.New("context deadline exceeded") // never reached the node
		}
		return nil
	}
	first := durableLane(t, store, key, node, &clock)
	if _, attempted, _ := first.Send(context.Background(), callTo(146, callA, "aa", 5)); !attempted {
		t.Fatal("a timeout is an uncertain broadcast")
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	second := durableLane(t, store, key, node, &clock) // a new process
	if _, _, err := second.Send(context.Background(), callTo(146, callB, "bb", 5)); err != nil {
		t.Fatal(err)
	}
	for _, tx := range node.atNonce(5) {
		if *tx.To() != callA || common.Bytes2Hex(tx.Data()) != "aa" {
			t.Fatalf("nonce 5 carried a different call: to=%s data=%x", tx.To(), tx.Data())
		}
	}
	if got := node.atNonce(5); len(got) != 2 {
		t.Fatalf("nonce 5 sent %d times, want the original and its re-broadcast", len(got))
	}
	if got := node.atNonce(6); len(got) != 1 || *got[0].To() != callB {
		t.Fatalf("call B must go to 6: %v", got)
	}
}

// Review 1 (multi-process): two lanes on one key in two "processes" sharing
// the database, building on an RPC whose pending view never moves, send
// concurrently: every nonce is allocated once, and carries one call.
func testTwoProcessesNeverShareANonce(t *testing.T, store KeyLaneStore) {
	key := newEcdsaKey()
	node := newChainNode(56)
	clock := time.Now()
	p1 := durableLane(t, store, key, node, &clock)
	p2 := durableLane(t, store, key, node, &clock)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lane := p1
			if i%2 == 1 {
				lane = p2
			}
			to := common.BigToAddress(big.NewInt(int64(1000 + i)))
			if _, _, err := lane.Send(context.Background(), callTo(56, to, "ab", 0)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	byNonce := map[uint64]common.Address{}
	for _, tx := range node.txs {
		if prev, ok := byNonce[tx.Nonce()]; ok && prev != *tx.To() {
			t.Fatalf("nonce %d carried two calls (%s and %s)", tx.Nonce(), prev, tx.To())
		}
		byNonce[tx.Nonce()] = *tx.To()
	}
	if len(byNonce) != 8 {
		t.Fatalf("%d distinct nonces for 8 sends, want 8", len(byNonce))
	}
}

func TestKeyLane_TwoProcessesNeverShareANonce_SQLite(t *testing.T) {
	testTwoProcessesNeverShareANonce(t, sqliteLaneStore(t))
}

func TestKeyLane_TwoProcessesNeverShareANonce_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "key_lane_", &models.KeyLaneNonce{}, &models.KeyLaneTx{})
	testTwoProcessesNeverShareANonce(t, &GormKeyLaneStore{DB: db})
}

// An evicted, underpriced tx: its re-broadcast is refused as underpriced, so
// the lane re-signs the SAME call at nonce 5 with a higher fee; once mined,
// later sends carry on above it.
func TestKeyLane_EvictedUnderpricedTxIsSpedUpWithTheSameCall(t *testing.T) {
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
	node.forget(common.HexToHash(h5)) // evicted from the mempool
	node.refuse = func(tx *types.Transaction, attempt int) error {
		if tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5)); err != nil {
		t.Fatal(err)
	}
	at5 := node.atNonce(5)
	last := at5[len(at5)-1]
	if *last.To() != callA || common.Bytes2Hex(last.Data()) != "aa" || last.Value().Sign() != 0 {
		t.Fatal("the speed-up must carry the same call")
	}
	if want := big.NewInt(1_125_000_001); last.GasPrice().Cmp(want) < 0 {
		t.Fatalf("speed-up price %s, want at least %s", last.GasPrice(), want)
	}
	if got := node.atNonce(6); len(got) != 1 || *got[0].To() != callB {
		t.Fatal("call B must go to 6")
	}
	// Review 3: the original's hash now resolves to the accepted speed-up.
	if final := lane.FinalHash(context.Background(), h5); final != last.Hash().Hex() {
		t.Fatalf("FinalHash(%s) = %s, want the speed-up %s", h5, final, last.Hash().Hex())
	}
	node.mu.Lock()
	node.mined = 7
	node.mu.Unlock()
	if err := lane.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "cc", 7)); err != nil {
		t.Fatal(err)
	}
	if got := node.atNonce(7); len(got) != 1 {
		t.Fatalf("after mining: nonce 7 sends=%d", len(got))
	}
	var raws int64
	store.DB.Model(&models.KeyLaneTx{}).Where("nonce < 7 AND raw_tx IS NOT NULL").Count(&raws)
	if raws != 0 {
		t.Fatalf("%d mined rows kept their raw bytes", raws)
	}
}

// Review 2 (security): speed-ups are capped — at most keyLaneMaxBumps, each
// based on the last ACCEPTED price, never above 3× today's price — and past
// the cap the lane holds; the admin retry re-arms it.
func TestKeyLane_SpeedUpsAreCappedAndBasedOnTheLastAcceptedPrice(t *testing.T) {
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
	// Nothing at nonce 5 is ever accepted again: every signing is evicted, and
	// the second speed-up is refused outright (never accepted).
	node.refuse = func(tx *types.Transaction, attempt int) error {
		if tx.Nonce() == 5 && tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		if tx.Nonce() == 5 && tx.GasPrice().Cmp(big.NewInt(1_200_000_000)) > 0 && tx.GasPrice().Cmp(big.NewInt(1_300_000_000)) < 0 {
			return errors.New("transaction underpriced") // the 2nd bump: rejected, never accepted
		}
		return nil
	}
	var prices []*big.Int
	for i := 0; i < 5; i++ {
		node.mu.Lock()
		for h := range node.known {
			delete(node.known, h)
		}
		node.mu.Unlock()
		clock = clock.Add(keyLaneGapGrace + time.Second)
		_, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5))
		if errors.Is(err, ErrKeyLaneHeld) {
			break
		}
	}
	seen := map[common.Hash]bool{common.HexToHash(h5): true}
	for _, tx := range node.atNonce(5) { // each signing once (a re-broadcast repeats its bytes)
		if !seen[tx.Hash()] {
			seen[tx.Hash()] = true
			prices = append(prices, tx.GasPrice())
		}
	}
	if len(prices) != keyLaneMaxBumps {
		t.Fatalf("speed-ups %v, want exactly %d before the hold", prices, keyLaneMaxBumps)
	}
	// Bump 2 was refused: recorded rejected, never the accepted base.
	var rejected int64
	store.DB.Model(&models.KeyLaneTx{}).Where("nonce = 5 AND status = ? AND hash <> ?", models.KeyLaneTxRefused, strings.ToLower(h5)).Count(&rejected)
	var alloc models.KeyLaneNonce
	store.DB.Where("nonce = 5").First(&alloc)
	if rejected != 1 || alloc.AcceptedPrice != prices[2].String() {
		t.Fatalf("rejected rows %d, accepted price %s; want only bump 2 rejected and bump 3 (%s) accepted", rejected, alloc.AcceptedPrice, prices[2])
	}
	if prices[2].Cmp(prices[1]) <= 0 {
		t.Fatalf("bump 3 %s must step past the refused bump 2 %s", prices[2], prices[1])
	}
	for _, p := range prices {
		if p.Cmp(big.NewInt(3_000_000_000)) > 0 {
			t.Fatalf("a speed-up priced %s, above 3× today's 1 gwei", p)
		}
	}
	if _, why, held := lane.Held(); !held || !strings.Contains(why, "speed-up limit") {
		t.Fatalf("held=%v reason=%q, want held at the speed-up limit", held, why)
	}
	if err := lane.RetrySpeedUp(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if _, _, held := lane.Held(); held {
		t.Fatal("the admin retry clears the hold")
	}
}

// The fee cap itself: a stuck call already priced near 3× today's price is
// not bumped above it — the lane holds.
func TestKeyLane_SpeedUpNeverExceedsTheFeeCap(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	h5, _, err := lane.Send(context.Background(), func() ([]byte, error) {
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
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5)); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("err = %v, want held at the fee cap", err)
	}
	for _, tx := range node.atNonce(5) {
		if tx.GasPrice().Cmp(big.NewInt(3_000_000_000)) > 0 {
			t.Fatalf("a signing at %s wei exceeded the cap", tx.GasPrice())
		}
	}
}

// A hole at a nonce NO process allocated (a send from outside the server):
// after the grace the lane holds, Watch fails (the streak ALERT), and a
// super-admin's FillGap — a 0-value self-transfer — releases it. FillGap is
// refused for any nonce in the record.
func TestKeyLane_ForeignGapHoldsUntilAnAdminFillsIt(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // the RPC reports 6: someone else's tx sat at 5
		if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 6)); err != nil {
			t.Fatal(err)
		}
	}
	// The foreign tx at 5 is dropped: pending reads 5; no process allocated 5.
	if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); err != nil {
		t.Fatalf("first sight of the hole must not hold yet: %v", err)
	}
	if got := node.atNonce(5); len(got) != 0 {
		t.Fatal("a hole outside the record is never taken by our own call")
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("err = %v, want the lane held", err)
	}
	if err := lane.Watch(ctx); !errors.Is(err, ErrKeyLaneHeld) {
		t.Fatalf("Watch = %v, want the held error (failure-streak ALERT)", err)
	}
	built := false
	if _, _, err := lane.Send(ctx, func() ([]byte, error) { built = true; return nil, nil }); !errors.Is(err, ErrKeyLaneHeld) || built {
		t.Fatalf("a held lane refuses before building: err=%v built=%v", err, built)
	}
	if _, err := lane.FillGap(ctx, 6); err == nil {
		t.Fatal("a nonce in the record is never filled")
	}
	hash, err := lane.FillGap(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	fill := node.atNonce(5)
	if len(fill) != 1 || fill[0].Hash().Hex() != hash || fill[0].Value().Sign() != 0 || !strings.EqualFold(fill[0].To().Hex(), lane.Address()) {
		t.Fatalf("the fill must be one 0-value self-transfer at 5: %v", fill)
	}
	if _, _, held := lane.Held(); held {
		t.Fatal("the fill clears the hold")
	}
	// Review 5/6: a second process (same database) cannot fill it again.
	other := durableLane(t, store, key, node, &clock)
	if _, err := other.FillGap(ctx, 5); err == nil {
		t.Fatal("a filled nonce is in the record: never filled twice")
	}
}

// Review 9: a nonce we allocated that is now mined is never "foreign", even
// while a lagging RPC still reports it as pending.
func TestKeyLane_MinedAllocationIsNeverForeign(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); err != nil {
			t.Fatal(err)
		}
	}
	node.mu.Lock()
	node.mined = 6
	node.mu.Unlock()
	_ = lane.Watch(ctx) // 5 mined
	clock = clock.Add(time.Minute)
	for i := 0; i < 2; i++ { // the lagging view still says 5
		if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		clock = clock.Add(keyLaneGapGrace + time.Second)
	}
	if _, _, held := lane.Held(); held {
		t.Fatal("a mined allocation of ours is not a foreign hole")
	}
}

// A 429 on the lookup (or the mined-nonce read) is transient: never a hold,
// never a reuse.
func TestKeyLane_TransientLookupErrorsNeverHold(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(146)
	node.mined = 5
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	h5, _, _ := lane.Send(context.Background(), callTo(146, callA, "aa", 5))
	node.forget(common.HexToHash(h5))
	node.lookErr = errors.New("429 Too Many Requests")
	node.minedErr = errors.New("429 Too Many Requests")
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5)); err != nil {
		t.Fatalf("a transient error must not refuse or hold: %v", err)
	}
	if _, _, held := lane.Held(); held {
		t.Fatal("a 429 must never hold the lane")
	}
	if got := node.atNonce(5); len(got) != 1 {
		t.Fatalf("nonce 5 sent %d times; an unverified gap is neither re-sent nor reused", len(got))
	}
}

// Two concurrent top-ups each fit the balance on their own, but not together:
// the second sees the first's unmined spend and is refused, so the
// reservation is never dipped into.
func TestKeyLane_ConcurrentTopUpsCannotDipIntoTheReservation(t *testing.T) {
	key := newEcdsaKey()
	node := newChainNode(56)
	reg := NewSignerRegistry()
	reg.SetStore(sqliteLaneStore(t))
	lane := reg.Lane("gas_sponsor", key.k, node)
	lane.AddReservation("pool_releases", func(context.Context) (uint64, error) { return 300_000, nil }) // 6e14 at 1 gwei
	node.balance = big.NewInt(2_000_000_000_000_000)                                                    // one 1e15 top-up + fee + reserve fits
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = lane.Send(context.Background(), pricedOn(56, 0, 1_000_000_000_000_000))
		}(i)
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrRelayerUnderfunded):
			refused++
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("errs = %v, want exactly one top-up through and one refused", errs)
	}
	// Review 8: the quote-time check counts the same unmined spend.
	if err := lane.CanPay(context.Background(), 21_000, big.NewInt(1e9), big.NewInt(1_000_000_000_000_000)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("CanPay = %v, want refused like the send", err)
	}
}

// Review 4: the balance and the mined nonce are read at one block; a
// transaction already mined at that block is paid for in the balance (not
// subtracted again), one not yet mined is.
func TestKeyLane_FeeGateCountsOnlyUnpaidSpend(t *testing.T) {
	key := newEcdsaKey()
	cn := newChainNode(56)
	node := &statefulNode{chainNode: cn}
	reg := NewSignerRegistry()
	reg.SetStore(sqliteLaneStore(t))
	lane := reg.Lane("gas_sponsor", key.k, node)
	ctx := context.Background()
	cn.balance = big.NewInt(2_000_000_000_000_000)
	if _, _, err := lane.Send(ctx, pricedOn(56, 0, 1_000_000_000_000_000)); err != nil {
		t.Fatal(err)
	}
	// Not yet mined at the read: its 1e15 (+ fee) is still to be spent, so a
	// 0.96e15 send (needing 1.002e15) does not fit 2e15 - 1.000021e15.
	if _, _, err := lane.Send(ctx, pricedOn(56, 0, 960_000_000_000_000)); !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("undercount: err = %v, want the unmined top-up counted", err)
	}
	// Mined at the read (the balance already paid it): a 0.9e15 send (needing
	// 0.942e15) fits 0.999979e15 — the mined top-up is not subtracted twice.
	node.mu.Lock()
	node.stateMined = 1
	cn.balance = big.NewInt(999_979_000_000_000)
	node.mu.Unlock()
	if _, _, err := lane.Send(ctx, pricedOn(56, 0, 900_000_000_000_000)); err != nil {
		t.Fatalf("double count: err = %v, want the mined top-up not subtracted again", err)
	}
}

// The pre-broadcast hook runs INSIDE the allocation transaction: it sees the
// allocation and its signing, and its error rolls all of it back — nothing
// signed survives and the nonce is not taken.
func TestKeyLane_HookRunsInsideTheAllocationTransaction(t *testing.T) {
	store := sqliteLaneStore(t)
	key := newEcdsaKey()
	node := newChainNode(56)
	clock := time.Now()
	lane := durableLane(t, store, key, node, &clock)
	_, _, err := lane.SendRecorded(context.Background(), callTo(56, callA, "aa", 3), func(hctx context.Context, n uint64, hash string) error {
		var alloc, txs int64
		LaneDB(hctx, store.DB).Model(&models.KeyLaneNonce{}).Where("nonce = ?", n).Count(&alloc)
		LaneDB(hctx, store.DB).Model(&models.KeyLaneTx{}).Where("nonce = ? AND hash = ? AND raw_tx IS NOT NULL", n, strings.ToLower(hash)).Count(&txs)
		if alloc != 1 || txs != 1 || len(node.txs) != 0 {
			t.Errorf("at the hook: %d allocations, %d signings for nonce %d, %d broadcasts; want 1, 1, 0", alloc, txs, n, len(node.txs))
		}
		return errors.New("the caller's row moved")
	})
	if err == nil || !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want the hook's refusal as not broadcast", err)
	}
	var allocs, signings int64
	store.DB.Model(&models.KeyLaneNonce{}).Count(&allocs)
	store.DB.Model(&models.KeyLaneTx{}).Count(&signings)
	if allocs != 0 || signings != 0 || len(node.txs) != 0 {
		t.Fatalf("after a hook error: %d allocations, %d signings, %d broadcasts; want none", allocs, signings, len(node.txs))
	}
	if _, _, err := lane.Send(context.Background(), callTo(56, callB, "bb", 3)); err != nil {
		t.Fatal(err)
	}
	if got := node.atNonce(3); len(got) != 1 || *got[0].To() != callB {
		t.Fatalf("nonce 3 must be free for the next call: %v", got)
	}
}

// Review 10: a signer that cannot sign without broadcasting (a future KMS) is
// refused on a durable store — nothing is broadcast unrecorded.
func TestKeyLane_NonRawSignerIsRefusedOnADurableStore(t *testing.T) {
	lane := soloLane(&fakeReleaseSigner{addr: sponsorTestSigner, hash: "0x1"}, nil)
	lane.store = sqliteLaneStore(t)
	built := false
	if _, attempted, err := lane.Send(context.Background(), func() ([]byte, error) { built = true; return unsignedOn(0, 1) }); attempted || !errors.Is(err, ErrTxNotBroadcast) || built {
		t.Fatalf("attempted=%v err=%v built=%v, want refused before anything", attempted, err, built)
	}
}

// Review 3: an IT operator debit the lane sped up reports the replacement's
// receipt for the original hash.
func TestOperatorReceipt_FollowsTheSpeedUp(t *testing.T) {
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
		if tx.Hash() == common.HexToHash(h5) {
			return errors.New("transaction underpriced")
		}
		return nil
	}
	clock = clock.Add(keyLaneGapGrace + time.Second)
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "bb", 5)); err != nil {
		t.Fatal(err)
	}
	replacement := lane.FinalHash(context.Background(), h5)
	if replacement == h5 {
		t.Fatal("setup: no speed-up recorded")
	}
	svc := NewMCGPSwapService(nil, nil)
	svc.SetOperatorLane(lane)
	svc.receiptWait = func(h string, d time.Duration) (*types.Receipt, error) {
		if strings.EqualFold(h, replacement) {
			return &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: common.HexToHash(h)}, nil
		}
		time.Sleep(d)
		return nil, errors.New("not found")
	}
	r, err := svc.receipt(h5)
	if err != nil || r.TxHash != common.HexToHash(replacement) {
		t.Fatalf("receipt(%s) = %v, %v; want the replacement %s's receipt", h5, r, err, replacement)
	}
}
