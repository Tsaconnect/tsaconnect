package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// memLaneStore is a KeyLaneStore in memory, shared between "processes".
type memLaneStore struct {
	mu   sync.Mutex
	rows []models.KeyLaneTx
}

func (m *memLaneStore) Record(_ context.Context, r *models.KeyLaneTx) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.CreatedAt = time.Now()
	m.rows = append(m.rows, *r)
	return nil
}
func (m *memLaneStore) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rows {
		if r.ID == id {
			m.rows = append(m.rows[:i], m.rows[i+1:]...)
			return nil
		}
	}
	return nil
}
func (m *memLaneStore) SetStatus(_ context.Context, id uuid.UUID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].ID == id {
			m.rows[i].Status = status
		}
	}
	return nil
}
func (m *memLaneStore) Pending(_ context.Context, chainID int64, address string) ([]models.KeyLaneTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.KeyLaneTx
	for _, r := range m.rows {
		if r.ChainID == chainID && strings.EqualFold(r.Address, address) && r.Status == models.KeyLaneTxPending {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memLaneStore) MarkMined(_ context.Context, chainID int64, address string, mined uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].ChainID == chainID && strings.EqualFold(m.rows[i].Address, address) && uint64(m.rows[i].Nonce) < mined {
			m.rows[i].Status, m.rows[i].RawTx = models.KeyLaneTxMined, nil
		}
	}
	return nil
}

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
}

func newChainNode(id int64) *chainNode {
	return &chainNode{id: id, known: map[common.Hash]bool{}, attempts: map[common.Hash]int{}, price: big.NewInt(1e9), balance: big.NewInt(1e18)}
}

func (n *chainNode) ChainID() *big.Int { return big.NewInt(n.id) }
func (n *chainNode) SendRawTransaction(raw []byte) (string, error) {
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
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lookErr != nil {
		return false, n.lookErr
	}
	return n.known[common.HexToHash(h)], nil
}
func (n *chainNode) LatestNonce(context.Context, string) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mined, n.minedErr
}
func (n *chainNode) SuggestGasPrice() (*big.Int, error) { return n.price, nil }
func (n *chainNode) NativeBalance(context.Context, string) (*big.Int, error) {
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

// Owner's rule across a restart: process 1 signs call A at nonce 5 and its
// broadcast times out without reaching the node. Process 2 (same store) seeds
// from key_lane_txs and, once the grace has passed, re-broadcasts A at 5 —
// the next call (B) goes to 6. No other call is ever signed at 5.
func TestKeyLane_RestartResendsTheSameCallNeverADifferentOne(t *testing.T) {
	store := &memLaneStore{}
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

// An evicted, underpriced tx: its re-broadcast is refused as underpriced, so
// the lane re-signs the SAME call at nonce 5 with a higher fee; once mined,
// later sends carry on above it.
func TestKeyLane_EvictedUnderpricedTxIsSpedUpWithTheSameCall(t *testing.T) {
	store := &memLaneStore{}
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
	node.mu.Lock()
	node.mined = 7
	node.mu.Unlock()
	if err := lane.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lane.Send(context.Background(), callTo(146, callB, "cc", 7)); err != nil {
		t.Fatal(err)
	}
	if got := node.atNonce(7); len(got) != 1 || len(lane.sent) != 1 {
		t.Fatalf("after mining: nonce 7 sends=%d records=%d", len(got), len(lane.sent))
	}
	for _, r := range store.rows {
		if r.Nonce < 7 && (r.Status != models.KeyLaneTxMined || r.RawTx != nil) {
			t.Fatalf("row %d status %s raw kept=%v, want mined with raw dropped", r.Nonce, r.Status, r.RawTx != nil)
		}
	}
}

// A hole at a nonce this lane never signed (a send from outside the
// process): after the grace the lane holds, Watch fails (the streak ALERT),
// and a super-admin's FillGap — a 0-value self-transfer at that nonce —
// releases it.
func TestKeyLane_ForeignGapHoldsUntilAnAdminFillsIt(t *testing.T) {
	store := &memLaneStore{}
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
	// The foreign tx at 5 is dropped: pending now reads 5, nothing of ours there.
	if _, _, err := lane.Send(ctx, callTo(146, callA, "aa", 5)); err != nil {
		t.Fatalf("first sight of the hole must not hold yet: %v", err)
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
	if _, err := lane.FillGap(ctx, 6); err == nil {
		t.Fatal("a nonce carrying our own call is never filled")
	}
	found := false
	for _, r := range store.rows {
		found = found || (r.Nonce == 5 && r.Status == models.KeyLaneTxFilled)
	}
	if !found {
		t.Fatal("the fill is recorded before its broadcast")
	}
}

// A 429 on the lookup (or the mined-nonce read) is transient: never a hold,
// never a reuse.
func TestKeyLane_TransientLookupErrorsNeverHold(t *testing.T) {
	store := &memLaneStore{}
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

// Review: two concurrent top-ups each fit the balance on their own, but not
// together: the second sees the first's unmined spend under the lane lock and
// is refused, so the reservation is never dipped into.
func TestKeyLane_ConcurrentTopUpsCannotDipIntoTheReservation(t *testing.T) {
	key := newEcdsaKey()
	node := newChainNode(56)
	reg := NewSignerRegistry()
	lane := reg.Lane("gas_sponsor", key.k, node)
	lane.AddReservation("pool_releases", func(context.Context) (uint64, error) { return 300_000, nil }) // 6e14 at 1 gwei
	// One top-up: 1e15 value + 21k gas fee (4.2e13 buffered) + 6e14 reserved = 1.642e15.
	node.balance = big.NewInt(2_000_000_000_000_000)
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
}

// The lane lock is ctx-aware: a caller whose ctx is done never acquires it.
func TestKeyLane_LockIsContextAware(t *testing.T) {
	lane := soloLane(&fakeReleaseSigner{addr: sponsorTestSigner, hash: "0x1"}, nil)
	if err := lane.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer lane.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	built := false
	if _, _, err := lane.Send(ctx, func() ([]byte, error) { built = true; u, _ := unsignedOn(0, 1); return u, nil }); err == nil || built {
		t.Fatalf("err=%v built=%v, want refused without building", err, built)
	}
}

// The GORM store round trip on SQLite: rows written before broadcast, read
// back as pending, and marked mined with their raw bytes dropped.
func TestGormKeyLaneStore_RoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.KeyLaneTx{}); err != nil {
		t.Fatal(err)
	}
	s := &GormKeyLaneStore{DB: db}
	ctx := context.Background()
	for n := int64(4); n <= 6; n++ {
		if err := s.Record(ctx, &models.KeyLaneTx{ChainID: 56, Address: "0xABC", Nonce: n, Hash: "0xh" + big.NewInt(n).String(),
			RawTx: []byte{1, 2}, Status: models.KeyLaneTxPending}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkMined(ctx, 56, "0xabc", 6); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Pending(ctx, 56, "0xAbC")
	if err != nil || len(rows) != 1 || rows[0].Nonce != 6 || rows[0].RawTx == nil {
		t.Fatalf("pending = %+v, %v; want only nonce 6 with its bytes", rows, err)
	}
	var mined models.KeyLaneTx
	db.First(&mined, "nonce = ?", 4)
	if mined.Status != models.KeyLaneTxMined || mined.RawTx != nil {
		t.Fatalf("row 4 = %+v, want mined with raw dropped", mined)
	}
}

type ecdsaKey struct{ k *ecdsa.PrivateKey }

func newEcdsaKey() *ecdsaKey { k, _ := crypto.GenerateKey(); return &ecdsaKey{k: k} }

func jsonTx(u blockchain.UnsignedTx) ([]byte, error) { return json.Marshal(u) }
