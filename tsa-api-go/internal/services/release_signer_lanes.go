package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// KeyLane is the ONE send lane of one hot key on one chain (signer
// consolidation 2026-10-13): the in-process lock, the one fee gate every send
// on the key passes, and the bridge to the DB-authoritative nonce record
// (KeyLaneStore). Every role signing with that key on that chain (PM release,
// expiry cancel, IT operator, gas sponsor, treasury) sends through the same
// KeyLane, and every process allocates its nonces from the same store under a
// per-(chain, address) advisory lock. Get one from a SignerRegistry; the lint
// test forbids building one anywhere else.
//
// Nonce rule (owner): a nonce only ever carries ONE call (to, data, value),
// enforced by the allocation's unique (chain, address, nonce) and its call
// fingerprint, across processes and restarts. A transaction of ours the node
// does not have past keyLaneGapGrace is re-broadcast byte for byte once, then
// re-signed with the SAME call at a higher fee (capped; at most
// keyLaneMaxBumps). A hole at a nonce no process allocated (a send from
// outside the server) holds the lane until the mined nonce passes it or a
// super-admin fills or clears it. Transient read failures never hold.
type KeyLane struct {
	sem     chan struct{} // the in-process lane lock, acquired ctx-aware
	signer  ReleaseSigner
	chainID int64 // what the lane's broadcaster reports; 0 = unchecked (a solo lane)

	held    atomic.Pointer[laneHold]
	gapMu   sync.Mutex
	gapSeen map[uint64]time.Time // when a hole no process allocated was first seen

	balances laneBalanceReader // nil: no fee gate (a solo test lane)
	state    laneStateReader   // balance and mined nonce at one block (consistent)
	lookup   laneTxLookup      // nil: an unseen tx is treated as unknown to the node
	nonces   laneNonceReader   // nil: holds never clear by themselves
	pricer   laneGasPricer     // nil: no speed-up (a stuck tx then holds the lane)
	store    KeyLaneStore      // never nil: the database at boot, memory otherwise
	roles    string

	reserveMu sync.Mutex
	reserves  map[string]LaneReservation

	now func() time.Time
}

type laneHold struct {
	nonce  uint64
	since  time.Time
	reason string
}

// laneBalanceReader is the lane broadcaster's native balance read
// (*blockchain.EVMClient), bounded by the caller's ctx.
type laneBalanceReader interface {
	NativeBalance(ctx context.Context, address string) (*big.Int, error)
}

// laneStateReader reads the balance and the mined nonce at the SAME block, so
// the fee gate knows exactly which of this lane's transactions the balance
// already paid for (*blockchain.EVMClient).
type laneStateReader interface {
	NativeStateAt(ctx context.Context, address string) (balance *big.Int, minedNonce uint64, err error)
}

// laneTxLookup asks the node whether it holds a transaction, pending or mined
// (blockchain.TxKnown).
type laneTxLookup interface {
	TransactionKnown(ctx context.Context, txHash string) (bool, error)
}

// laneNonceReader is the chain's latest MINED nonce for an address.
type laneNonceReader interface {
	LatestNonce(ctx context.Context, address string) (uint64, error)
}

// laneGasPricer is today's gas price, for a speed-up.
type laneGasPricer interface {
	SuggestGasPrice() (*big.Int, error)
}

// rawSigner is a signer that can sign without broadcasting, so the lane
// records the bytes before they leave (*LocalKeyReleaseSigner).
type rawSigner interface {
	signRaw(unsigned []byte) (raw []byte, hash string, err error)
	broadcastRaw(raw []byte, hash string) (string, error)
}

// ErrKeyLaneHeld: the lane found a hole in its nonce sequence it cannot fill
// with its own call, or a stuck call it may not bump further, and refuses every
// send until the mined nonce passes it or a super-admin acts.
var ErrKeyLaneHeld = errors.New("the signing key's lane is held at a nonce it cannot move")

const (
	// keyLaneGapGrace is how long a hole may persist before the lane acts on
	// it, and the spacing between actions on one stuck nonce.
	keyLaneGapGrace = 15 * time.Second
	// keyLaneReadTimeout bounds every read the lane makes.
	keyLaneReadTimeout = 10 * time.Second
	// keyLaneSpeedUpBumpPermille is a speed-up's minimum price bump: geth's
	// replacement rule is +10%.
	keyLaneSpeedUpBumpPermille = 1125
	// keyLaneMaxBumps: speed-ups per nonce before the lane holds (an admin
	// retry resets the count).
	keyLaneMaxBumps = 3
	// keyLaneLiveCapMultiple: a speed-up never prices above this × today's price.
	keyLaneLiveCapMultiple = 3
)

// keyLaneFeeCeilingGwei is each chain's absolute ceiling on a speed-up's gas
// price, in gwei (security review: a stuck tx must never be repriced into a
// drain); unlisted chains use keyLaneDefaultFeeCeilingGwei.
var keyLaneFeeCeilingGwei = map[int64]int64{
	56: 30, 97: 30, // BSC
	146: 2000, 14601: 2000, // Sonic
	8453: 5, 10: 5, 42161: 5, 59144: 5, // Base, Optimism, Arbitrum, Linea
	137:   3000, // Polygon
	1:     300,  // Ethereum
	43114: 300,  // Avalanche
}

const keyLaneDefaultFeeCeilingGwei = 500

func keyLaneFeeCeiling(chainID int64) *big.Int {
	g, ok := keyLaneFeeCeilingGwei[chainID]
	if !ok {
		g = keyLaneDefaultFeeCeilingGwei
	}
	return new(big.Int).Mul(big.NewInt(g), big.NewInt(1_000_000_000))
}

func newKeyLane(signer ReleaseSigner, chainID int64, store KeyLaneStore) *KeyLane {
	if store == nil {
		store = newMemKeyLaneStore()
	}
	return &KeyLane{sem: make(chan struct{}, 1), signer: signer, chainID: chainID, store: store,
		gapSeen: map[uint64]time.Time{}}
}

// soloLane is a lane outside any registry, for a signer handed in directly in
// a test (an in-memory store). balances nil = no fee gate. Only this file may
// build a lane (lint: TestKeyLanes_OnlyFromTheRegistry); production code takes
// registry lanes.
func soloLane(signer ReleaseSigner, balances laneBalanceReader) *KeyLane {
	l := newKeyLane(signer, 0, nil)
	l.balances = balances
	return l
}

// SetOperatorSigner wires a bare operator signer on a lane of its own. Tests
// only: the lint forbids any production call (boot uses SetOperatorLane).
// Defined here, with soloLane, so no other file builds a lane.
func (s *MCGPSwapService) SetOperatorSigner(signer ReleaseSigner) {
	s.operator, s.operatorLane = signer, nil
	if signer != nil {
		s.operatorLane = soloLane(signer, nil)
	}
	s.operatorVerified.Store(signer != nil) // a directly-wired signer is trusted (tests)
}

// Signer is the lane's signer: Address() for every role, and what Send signs with.
func (l *KeyLane) Signer() ReleaseSigner { return l.signer }

// Address is the lane's key's address.
func (l *KeyLane) Address() string { return l.signer.Address() }

// ChainID is the chain the lane sends on (0 for a solo lane).
func (l *KeyLane) ChainID() int64 { return l.chainID }

// lock acquires the lane, or gives up when ctx is (or becomes) done first.
func (l *KeyLane) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil { // never acquire on a dead ctx, even if the lane is free
		return notBroadcast(fmt.Errorf("key lane: waiting for the lane: %w", err))
	}
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return notBroadcast(fmt.Errorf("key lane: waiting for the lane: %w", ctx.Err()))
	}
}

func (l *KeyLane) unlock() { <-l.sem }

// LaneReservation is the gas (units) a lane must keep back for transactions it
// already owes; the gate prices it at each send's own gas price.
type LaneReservation func(ctx context.Context) (uint64, error)

// AddReservation registers (or replaces, by name) what the key's balance must
// keep back on this lane for transactions it already owes — the pool's live
// slices' releases on the release key, owed deliveries on a treasury lane.
// Every send but a ReservedSend must leave the sum of them plus its own fee; a
// reservation that cannot be read refuses the send.
func (l *KeyLane) AddReservation(name string, fn LaneReservation) {
	if l == nil || fn == nil {
		return
	}
	l.reserveMu.Lock()
	if l.reserves == nil {
		l.reserves = map[string]LaneReservation{}
	}
	l.reserves[name] = fn
	l.reserveMu.Unlock()
}

// reservedUnits sums the lane's reservations.
func (l *KeyLane) reservedUnits(ctx context.Context) (uint64, error) {
	l.reserveMu.Lock()
	fns := make(map[string]LaneReservation, len(l.reserves))
	for k, v := range l.reserves {
		fns[k] = v
	}
	l.reserveMu.Unlock()
	var sum uint64
	for name, fn := range fns {
		u, err := fn(ctx)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		sum += u
	}
	return sum, nil
}

type reservedSendKey struct{}

// ReservedSend marks ctx as carrying one of the transactions a lane's
// reservation is held for (a pool slice's release, cancel or resolve; an owed
// delivery or refund): its fee is checked, the reservation is not, since it is
// what the reservation pays. Only an owed call site marks it.
func ReservedSend(ctx context.Context) context.Context {
	return context.WithValue(ctx, reservedSendKey{}, true)
}

func isReservedSend(ctx context.Context) bool {
	v, _ := ctx.Value(reservedSendKey{}).(bool)
	return v
}

// callFingerprint identifies a call: keccak256(to|data|value). A nonce's
// allocation carries it; a speed-up re-signs only the same fingerprint.
func callFingerprint(u blockchain.UnsignedTx) string {
	value := u.Value
	if value == "" {
		value = "0"
	}
	return crypto.Keccak256Hash([]byte(strings.ToLower(u.To) + "|" + strings.ToLower(strings.TrimPrefix(u.Data, "0x")) + "|" + value)).Hex()
}

// Send builds (prepare + nonce read), allocates the nonce in the store,
// checks the key can pay, records, signs and broadcasts. attempted separates a
// build failure or a definite refusal (ErrTxNotBroadcast: nothing left this
// process) from a broadcast whose outcome is unknown, which callers must treat
// as possibly sent. A build error is returned as build returned it.
func (l *KeyLane) Send(ctx context.Context, build func() ([]byte, error)) (txHash string, attempted bool, err error) {
	return l.sendWith(ctx, l.signer, build, nil)
}

// SendRecorded is Send with a hook that receives the allocated nonce BEFORE
// the broadcast (review C1) — after the lane's own allocation and record, so a
// caller's persisted nonce always has a lane row. A hook error broadcasts
// nothing and frees the nonce.
func (l *KeyLane) SendRecorded(ctx context.Context, build func() ([]byte, error), onPrepared func(nonce uint64) error) (string, bool, error) {
	return l.sendWith(ctx, l.signer, build, onPrepared)
}

// feeInputs is what the fee gate read before taking any lock.
type feeInputs struct {
	balance       *big.Int
	mined         uint64 // the mined nonce the balance reflects
	minedKnown    bool
	reservedUnits uint64
}

// readFeeInputs reads the balance — with the mined nonce at the same block
// when the chain client can, else the mined nonce first and the balance after
// (an error there can only over-count) — and the reservation, with the
// caller's ctx (bounded), BEFORE any lock: a hung read holds up only its own
// send. nil on a lane without a fee gate.
func (l *KeyLane) readFeeInputs(ctx context.Context, address string) (*feeInputs, error) {
	if l.balances == nil && l.state == nil {
		return nil, nil
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	in := &feeInputs{}
	if !isReservedSend(ctx) {
		u, err := l.reservedUnits(rctx)
		if err != nil {
			return nil, notBroadcast(fmt.Errorf("key lane: read the lane's reservation: %w", err))
		}
		in.reservedUnits = u
	}
	if l.state != nil {
		bal, mined, err := l.state.NativeStateAt(rctx, address)
		if err != nil {
			return nil, notBroadcast(fmt.Errorf("key lane: read the balance: %w", err))
		}
		in.balance, in.mined, in.minedKnown = bal, mined, true
		return in, nil
	}
	if l.nonces != nil {
		if mined, err := l.nonces.LatestNonce(rctx, address); err == nil {
			in.mined, in.minedKnown = mined, true
		}
	}
	bal, err := l.balances.NativeBalance(rctx, address)
	if err != nil {
		return nil, notBroadcast(fmt.Errorf("key lane: read the balance: %w", err))
	}
	in.balance = bal
	return in, nil
}

// committedFrom is the nonce from which this lane's allocations are not yet
// paid for in the balance read (all of them when the mined nonce is unknown).
func (in *feeInputs) committedFrom() uint64 {
	if in == nil || !in.minedKnown {
		return 0
	}
	return in.mined
}

// heldErr is the refusal while the lane is held.
func (l *KeyLane) heldErr() error {
	h := l.held.Load()
	if h == nil {
		return nil
	}
	return notBroadcast(fmt.Errorf("%w (nonce %d since %s: %s)", ErrKeyLaneHeld, h.nonce, h.since.Format(time.RFC3339), h.reason))
}

// sendWith is Send signing with signer. Only a SponsorLane built without a
// registry lane passes a signer other than the lane's own (tests swap
// SponsorLane.Signer); every registry lane signs with its own.
func (l *KeyLane) sendWith(ctx context.Context, signer ReleaseSigner, build func() ([]byte, error), onPrepared func(uint64) error) (string, bool, error) {
	if err := l.heldErr(); err != nil { // refused before any read or build
		return "", false, err
	}
	rs, isRaw := signer.(rawSigner)
	if !isRaw && l.store.Durable() {
		// A signer that cannot hand over its bytes would broadcast a
		// transaction the store never saw.
		return "", false, notBroadcast(errors.New("key lane: this signer cannot sign without broadcasting; refusing to send unrecorded"))
	}
	address := signer.Address()
	fee, err := l.readFeeInputs(ctx, address)
	if err != nil {
		return "", false, err
	}
	if err := l.lock(ctx); err != nil {
		return "", false, err
	}
	defer l.unlock()
	if err := l.heldErr(); err != nil {
		return "", false, err
	}
	unsigned, err := build()
	if err != nil {
		return "", false, err
	}
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsigned, &u); err != nil {
		return "", false, notBroadcast(fmt.Errorf("key lane: read the prepared tx: %w", err))
	}
	if l.chainID != 0 {
		switch {
		case u.ChainID == "":
			return "", false, notBroadcast(fmt.Errorf("key lane: the transaction names no chain"))
		case u.ChainID != strconv.FormatInt(l.chainID, 10):
			return "", false, notBroadcast(fmt.Errorf("key lane: a tx built for chain %s was sent on chain %d", u.ChainID, l.chainID))
		}
	}
	fp := callFingerprint(u)
	var alloc *models.KeyLaneNonce
	var row *models.KeyLaneTx
	var raw []byte
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		l.reviewGapLocked(ctx, q, u.Nonce, fee)
		if err := l.heldErr(); err != nil {
			return err
		}
		a, err := q.Allocate(u.Nonce, fp, l.roles, l.clock())
		if err != nil {
			return err
		}
		alloc, u.Nonce = a, uint64(a.Nonce)
		if fee != nil {
			committed, err := q.Committed(fee.committedFrom())
			if err != nil {
				_ = q.Release(a.ID)
				return err
			}
			if err := l.requireFee(address, fee, committed, &u); err != nil {
				_ = q.Release(a.ID)
				return err
			}
		}
		if unsigned, err = json.Marshal(u); err != nil {
			_ = q.Release(a.ID)
			return err
		}
		if !isRaw {
			return nil // memory store only: recorded once the signer reports its hash
		}
		var hash string
		if raw, hash, err = rs.signRaw(unsigned); err != nil {
			_ = q.Release(a.ID)
			return err
		}
		row = laneTxRow(a, u, hash, raw, l.clock())
		return q.RecordTx(row)
	})
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) {
			return "", false, err
		}
		return "", false, notBroadcast(fmt.Errorf("key lane: allocate and record: %w", err))
	}
	if onPrepared != nil {
		if err := onPrepared(u.Nonce); err != nil {
			l.undo(ctx, address, alloc, row)
			return "", false, fmt.Errorf("key lane: record the nonce before broadcast: %w", err)
		}
	}
	var txHash string
	if isRaw {
		txHash, err = rs.broadcastRaw(raw, row.Hash)
	} else {
		txHash, err = signer.SignAndBroadcast(ctx, unsigned)
		h := txHash
		if err != nil {
			h = uncertainTxHash(err)
		}
		row = laneTxRow(alloc, u, h, nil, l.clock())
		if rerr := l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error { return q.RecordTx(row) }); rerr != nil {
			log.Printf("[signer] key lane: record a test signer's tx: %v", rerr)
		}
	}
	if errors.Is(err, ErrTxNotBroadcast) {
		l.undo(ctx, address, alloc, row)
		return "", false, err
	}
	if err == nil {
		if aerr := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error { return q.Accept(alloc, row) }); aerr != nil {
			log.Printf("[signer] key lane: mark %s accepted: %v", row.Hash, aerr)
		}
	}
	return txHash, true, err
}

// laneTxRow is a signing's row.
func laneTxRow(a *models.KeyLaneNonce, u blockchain.UnsignedTx, hash string, raw []byte, at time.Time) *models.KeyLaneTx {
	return &models.KeyLaneTx{AllocationID: a.ID, Nonce: a.Nonce, Hash: hash, RawTx: raw, To: u.To, Data: u.Data,
		Value: u.Value, GasLimit: int64(u.GasLimit), GasPrice: u.GasPrice, Status: models.KeyLaneTxPending, CreatedAt: at}
}

// undo frees a nonce whose transaction provably never left: its row is marked
// rejected and its allocation released, so the nonce is free again.
func (l *KeyLane) undo(ctx context.Context, address string, a *models.KeyLaneNonce, row *models.KeyLaneTx) {
	if a == nil {
		return
	}
	err := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error {
		if row != nil {
			if err := q.SetTxStatus(row.ID, models.KeyLaneTxRejected); err != nil {
				return err
			}
		}
		return q.Release(a.ID)
	})
	if err != nil {
		log.Printf("[signer] ALERT key_lane_release_failed_total=1 chain=%d address=%s nonce=%d err=%v — the nonce stays allocated; a later send skips it and the watch will hold the lane at the gap",
			l.chainID, address, a.Nonce, err)
	}
}

// requireFee is the lane's one balance gate (floors only alert, owner
// 2026-10-13): the balance, less what this lane's transactions the balance
// has not yet paid will still spend (committed), must cover this transaction's
// gas limit × gas price × gasFeeBuffer, plus its value, plus the lane's
// reservation priced at the same gas price (unless this is an owed send).
// Fails closed when the transaction cannot be priced. A lane without a fee
// gate (in == nil) is not gated.
func (l *KeyLane) requireFee(address string, in *feeInputs, committed *big.Int, u *blockchain.UnsignedTx) error {
	if in == nil {
		return nil
	}
	price, ok := new(big.Int).SetString(u.GasPrice, 10)
	if !ok || price.Sign() <= 0 || u.GasLimit == 0 {
		return notBroadcast(fmt.Errorf("key lane: cannot price the prepared tx (gasPrice %q, gasLimit %d)", u.GasPrice, u.GasLimit))
	}
	value := new(big.Int)
	if u.Value != "" {
		if _, ok := value.SetString(u.Value, 10); !ok {
			return notBroadcast(fmt.Errorf("key lane: cannot read the prepared tx's value %q", u.Value))
		}
	}
	need := feeNeed(u.GasLimit, price, value)
	reserved := feeNeed(in.reservedUnits, price, nil)
	need.Add(need, reserved)
	available := new(big.Int).Set(in.balance)
	if committed == nil {
		committed = new(big.Int)
	}
	available.Sub(available, committed)
	if available.Cmp(need) < 0 {
		log.Printf("[signer] ALERT key_lane_cannot_pay_total=1 chain=%d address=%s balance=%s in_flight=%s needed=%s reserved=%s",
			l.chainID, address, in.balance, committed, need, reserved)
		return notBroadcast(fmt.Errorf("%w: balance %s wei (%s committed to unmined txs), this transaction needs %s (%s of it reserved)",
			ErrRelayerUnderfunded, in.balance, committed, need, reserved))
	}
	return nil
}

// CanPay is the lane's fee gate asked before anything is built (a quote, a
// pool open): units of gas at price plus value, plus the lane's reservation
// unless ctx is a ReservedSend, against the balance less what this lane's
// unpaid transactions will still spend — the same computation the send makes.
func (l *KeyLane) CanPay(ctx context.Context, units uint64, price, value *big.Int) error {
	if l == nil || l.signer == nil {
		return nil
	}
	_, err := l.canPayAs(ctx, l.signer.Address(), units, price, value)
	return err
}

// canPayAs is CanPay for address (a solo lane signing with its caller's
// signer); it also returns the balance it read (nil on a lane without a
// balance reader) so a caller can raise its floor ALERT without a second read.
func (l *KeyLane) canPayAs(ctx context.Context, address string, units uint64, price, value *big.Int) (*big.Int, error) {
	if l == nil || (l.balances == nil && l.state == nil) {
		return nil, nil
	}
	if price == nil {
		return nil, notBroadcast(fmt.Errorf("key lane: no gas price"))
	}
	if value == nil {
		value = new(big.Int)
	}
	in, err := l.readFeeInputs(ctx, address)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	committed, err := l.store.Committed(rctx, l.chainID, address, in.committedFrom())
	if err != nil {
		return in.balance, notBroadcast(fmt.Errorf("key lane: read the lane's unmined spend: %w", err))
	}
	return in.balance, l.requireFee(address, in, committed, &blockchain.UnsignedTx{GasLimit: units, GasPrice: price.String(), Value: value.String()})
}

// reviewGapLocked looks at the chain's pending nonce p before allocating: a
// hole at a nonce of ours the node lost, a hole no process allocated, or
// nothing (a lagging view, or no hole). Decided from the store's allocations,
// never from a pending read alone. Never puts a different call at p.
// Transient read failures do nothing (the next send or watch tick retries).
// Caller holds the store's lane lock.
func (l *KeyLane) reviewGapLocked(ctx context.Context, q KeyLaneTxn, p uint64, fee *feeInputs) {
	now := l.clock()
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	a, err := q.AllocationAt(p)
	if err != nil {
		return
	}
	if a == nil {
		above, err := q.HasAllocationAbove(p)
		if err != nil || !above {
			l.forgetGap(p)
			return
		}
		// Our allocations wait above p, and no process ever allocated p.
		if l.nonces == nil {
			return
		}
		mined, err := l.nonces.LatestNonce(rctx, l.Address())
		if err != nil || mined > p {
			return // unknown, or p is mined: the pending view lags
		}
		if first := l.noteGap(p, now); now.Sub(first) >= keyLaneGapGrace {
			l.holdLocked(p, "no process allocated this nonce — something outside the server used the key")
		}
		return
	}
	if a.Status != models.KeyLaneNonceAllocated {
		return // mined (a lagging pending view) or released (the allocation below reuses it)
	}
	if now.Sub(a.ActedAt) < keyLaneGapGrace {
		return
	}
	cur, err := q.CurrentTx(a)
	if err != nil {
		return
	}
	if l.nonces != nil {
		if mined, err := l.nonces.LatestNonce(rctx, l.Address()); err == nil && mined > p {
			return
		}
	}
	if cur == nil {
		l.holdLocked(p, "this nonce is allocated but no signing of it is recorded")
		return
	}
	if l.lookup != nil {
		known, err := l.lookup.TransactionKnown(rctx, cur.Hash)
		if err != nil {
			log.Printf("[signer] key lane: nonce %d tx %s unverified (%s); not re-sent, retried later", p, cur.Hash, RedactAlertText(err.Error()))
			return
		}
		if known {
			return
		}
	}
	rs, isRaw := l.signer.(rawSigner)
	if cur.RawTx != nil && isRaw && a.Rebroadcasts == 0 {
		_ = q.Touch(a, a.Bumps, a.Rebroadcasts+1, now)
		_, err := rs.broadcastRaw(cur.RawTx, cur.Hash)
		log.Printf("[signer] ALERT key_lane_nonce_gap_rebroadcast_total=1 chain=%d address=%s nonce=%d tx=%s err=%v",
			l.chainID, l.Address(), p, cur.Hash, err)
		if err == nil {
			_ = q.Accept(a, cur)
			return
		}
		if !errors.Is(err, ErrTxNotBroadcast) {
			return // uncertain: look again after the grace
		}
		// Definitely refused (underpriced, below the base fee): speed it up now.
	}
	l.speedUpLocked(ctx, q, a, cur, fee)
}

func (l *KeyLane) noteGap(p uint64, now time.Time) time.Time {
	l.gapMu.Lock()
	defer l.gapMu.Unlock()
	first, ok := l.gapSeen[p]
	if !ok {
		l.gapSeen[p] = now
		return now
	}
	return first
}

func (l *KeyLane) forgetGap(p uint64) {
	l.gapMu.Lock()
	delete(l.gapSeen, p)
	l.gapMu.Unlock()
}

// speedUpLocked re-signs the SAME call at its nonce at a higher fee: the
// replacement bump over the last ACCEPTED price (or the original's), at least
// today's price, never above min(keyLaneLiveCapMultiple × today's price, the
// chain's ceiling), at most keyLaneMaxBumps times, and through the fee gate.
// Past a cap, or without a raw signer or price, it holds the lane.
func (l *KeyLane) speedUpLocked(ctx context.Context, q KeyLaneTxn, a *models.KeyLaneNonce, cur *models.KeyLaneTx, fee *feeInputs) {
	p := uint64(a.Nonce)
	rs, isRaw := l.signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		l.holdLocked(p, "the transaction at this nonce is gone and cannot be re-signed here")
		return
	}
	if a.Bumps >= keyLaneMaxBumps {
		l.holdLocked(p, fmt.Sprintf("speed-up limit reached (%d bumps)", a.Bumps))
		return
	}
	basePrice := a.AcceptedPrice
	if basePrice == "" {
		if first, err := q.FirstTx(a); err == nil && first != nil {
			basePrice = first.GasPrice
		}
	}
	base, ok := new(big.Int).SetString(basePrice, 10)
	if !ok {
		l.holdLocked(p, "the transaction at this nonce has no readable gas price")
		return
	}
	live, err := l.pricer.SuggestGasPrice()
	if err != nil || live == nil || live.Sign() <= 0 {
		return // transient
	}
	// One replacement step per signing since the last accepted one: a
	// signing the node refused is never the base, but re-sending its price
	// would be refused again, so the step compounds from the accepted price.
	bumped := new(big.Int).Set(base)
	for i := 0; i <= a.Bumps-a.AcceptedBumps; i++ {
		bumped.Mul(bumped, big.NewInt(keyLaneSpeedUpBumpPermille)).Div(bumped, big.NewInt(1000)).Add(bumped, big.NewInt(1))
	}
	if live.Cmp(bumped) > 0 {
		bumped = new(big.Int).Set(live)
	}
	ceiling := new(big.Int).Mul(live, big.NewInt(keyLaneLiveCapMultiple))
	if abs := keyLaneFeeCeiling(l.chainID); abs.Cmp(ceiling) < 0 {
		ceiling = abs
	}
	if bumped.Cmp(ceiling) > 0 {
		l.holdLocked(p, fmt.Sprintf("speed-up fee cap reached: next price %s wei, cap %s", bumped, ceiling))
		return
	}
	call := blockchain.UnsignedTx{Nonce: p, GasPrice: bumped.String(), GasLimit: uint64(cur.GasLimit), To: cur.To,
		Value: cur.Value, Data: cur.Data, ChainID: strconv.FormatInt(l.chainID, 10)}
	if callFingerprint(call) != a.Fingerprint {
		l.holdLocked(p, "the recorded call does not match this nonce's allocation")
		return
	}
	if fee != nil {
		committed, err := q.Committed(fee.committedFrom())
		if err != nil {
			return
		}
		replacing := new(big.Int)
		addSpend(replacing, cur)
		committed.Sub(committed, replacing) // this signing replaces the current one
		owed := *fee
		owed.reservedUnits = 0 // the speed-up IS owed work
		if err := l.requireFee(l.Address(), &owed, committed, &call); err != nil {
			return // alerted; retried later
		}
	}
	unsigned, err := json.Marshal(call)
	if err != nil {
		return
	}
	raw, hash, err := rs.signRaw(unsigned)
	if err != nil {
		return
	}
	row := laneTxRow(a, call, hash, raw, l.clock())
	if err := q.RecordTx(row); err != nil {
		log.Printf("[signer] key lane: record the speed-up at nonce %d: %v", p, err)
		return // never broadcast an unrecorded signing
	}
	_ = q.Touch(a, a.Bumps+1, a.Rebroadcasts, l.clock())
	_, berr := rs.broadcastRaw(raw, hash)
	switch {
	case berr == nil:
		_ = q.Accept(a, row)
	case errors.Is(berr, ErrTxNotBroadcast):
		_ = q.SetTxStatus(row.ID, models.KeyLaneTxRejected) // the next bump starts from the last accepted price
	}
	log.Printf("[signer] ALERT key_lane_speed_up_total=1 chain=%d address=%s nonce=%d old_tx=%s new_tx=%s gas_price=%s err=%v",
		l.chainID, l.Address(), p, cur.Hash, hash, bumped, berr)
}

// holdLocked holds the lane at nonce p.
func (l *KeyLane) holdLocked(p uint64, reason string) {
	if l.held.Load() != nil {
		return
	}
	l.held.Store(&laneHold{nonce: p, since: l.clock(), reason: reason})
	log.Printf("[signer] ALERT key_lane_nonce_gap_held_total=1 chain=%d address=%s nonce=%d reason=%q — every send on this key and chain is refused until the mined nonce passes it or a super-admin acts (POST /api/admin/signer/lanes/fill-gap, /retry, /clear-hold)",
		l.chainID, l.Address(), p, reason)
}

// Held is the lane's hold, if any: (nonce, reason, true).
func (l *KeyLane) Held() (uint64, string, bool) {
	h := l.held.Load()
	if h == nil {
		return 0, "", false
	}
	return h.nonce, h.reason, true
}

// ClearHold is a super-admin's exit from a held lane after manual inspection.
func (l *KeyLane) ClearHold(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	if h := l.held.Load(); h != nil {
		l.forgetGap(h.nonce)
		// The admin vouched for it: a fresh grace before it is judged again.
		_ = l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
			a, err := q.AllocationAt(h.nonce)
			if err != nil || a == nil {
				return err
			}
			return q.Touch(a, a.Bumps, a.Rebroadcasts, l.clock())
		})
	}
	l.held.Store(nil)
	return nil
}

// RetrySpeedUp is a super-admin's retry of a nonce held at the speed-up limit:
// its bump count is reset (the price cap still applies, priced at the next
// attempt's live price) and the hold cleared.
func (l *KeyLane) RetrySpeedUp(ctx context.Context, nonce uint64) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		a, err := q.AllocationAt(nonce)
		if err != nil {
			return err
		}
		if a == nil || a.Status != models.KeyLaneNonceAllocated {
			return fmt.Errorf("key lane: nonce %d has no unmined allocation", nonce)
		}
		return q.Touch(a, 0, a.Rebroadcasts, time.Time{})
	})
	if err != nil {
		return err
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	return nil
}

// FillGap is a super-admin's fix for a hole at a nonce NO process allocated
// (checked in the store, under the lane lock): a 0-value self-transfer at
// that nonce, allocated and recorded before its broadcast. Refused for any
// allocated nonce, or one the chain has mined.
func (l *KeyLane) FillGap(ctx context.Context, nonce uint64) (string, error) {
	if err := l.lock(ctx); err != nil {
		return "", err
	}
	defer l.unlock()
	rs, isRaw := l.signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		return "", errors.New("key lane: this lane cannot sign a gap fill")
	}
	var hash string
	err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		a, err := q.AllocationAt(nonce)
		if err != nil {
			return err
		}
		if a != nil {
			return fmt.Errorf("key lane: nonce %d is in the record (%s, role %q); only a hole no process allocated is filled", nonce, a.Status, a.Role)
		}
		rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
		defer cancel()
		if l.nonces != nil {
			mined, err := l.nonces.LatestNonce(rctx, l.Address())
			if err != nil {
				return fmt.Errorf("key lane: read the mined nonce: %w", err)
			}
			if mined > nonce {
				return fmt.Errorf("key lane: nonce %d is already mined", nonce)
			}
		}
		price, err := l.pricer.SuggestGasPrice()
		if err != nil {
			return fmt.Errorf("key lane: gas price: %w", err)
		}
		price = new(big.Int).Div(new(big.Int).Mul(price, big.NewInt(keyLaneSpeedUpBumpPermille)), big.NewInt(1000))
		call := blockchain.UnsignedTx{Nonce: nonce, GasPrice: price.String(), GasLimit: 21_000, To: l.Address(), Value: "0",
			ChainID: strconv.FormatInt(l.chainID, 10)}
		alloc, err := q.AllocateExact(nonce, callFingerprint(call), "gap_fill", l.clock())
		if err != nil {
			return err
		}
		unsigned, _ := json.Marshal(call)
		raw, h, err := rs.signRaw(unsigned)
		if err != nil {
			return err
		}
		row := laneTxRow(alloc, call, h, raw, l.clock())
		if err := q.RecordTx(row); err != nil {
			return err
		}
		if _, err := rs.broadcastRaw(raw, h); err != nil {
			if errors.Is(err, ErrTxNotBroadcast) {
				_ = q.SetTxStatus(row.ID, models.KeyLaneTxRejected)
				_ = q.Release(alloc.ID)
				return err
			}
		} else {
			_ = q.Accept(alloc, row)
		}
		hash = h
		return nil
	})
	if err != nil {
		return "", err
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	l.forgetGap(nonce)
	log.Printf("[signer] AUDIT key_lane_gap_filled=1 chain=%d address=%s nonce=%d tx=%s", l.chainID, l.Address(), nonce, hash)
	return hash, nil
}

// FinalHash follows a transaction this lane sent to the signing of its call
// the node accepted last (a speed-up), or returns hash unchanged.
func (l *KeyLane) FinalHash(ctx context.Context, hash string) string {
	if l == nil || l.signer == nil {
		return hash
	}
	h, err := l.store.FinalHash(ctx, l.chainID, l.Address(), hash)
	if err != nil || h == "" {
		return hash
	}
	return h
}

// waitFollowingReplacements waits up to timeout for hash's receipt, each
// round re-resolving hash to its call's accepted replacement (a speed-up), so
// a caller waiting on the original sees the replacement's receipt. wait is
// one bounded receipt wait. Returns the receipt and the hash it belongs to.
func waitFollowingReplacements(ctx context.Context, lane *KeyLane, hash string, timeout time.Duration,
	wait func(ctx context.Context, hash string, d time.Duration) (*types.Receipt, error)) (*types.Receipt, string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		h := hash
		if lane != nil {
			h = lane.FinalHash(ctx, hash)
		}
		round := time.Until(deadline)
		if round > 5*time.Second {
			round = 5 * time.Second
		}
		if round <= 0 {
			return nil, h, lastErr
		}
		started := time.Now()
		r, err := wait(ctx, h, round)
		if err == nil {
			return r, h, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, h, lastErr
		}
		// A wait that failed before its round was up is the wait's own answer
		// (not a timeout); keep going only if the call has a newer signing.
		if time.Since(started) < round-100*time.Millisecond && (lane == nil || lane.FinalHash(ctx, hash) == h) {
			return nil, h, lastErr
		}
	}
}

// Watch is the lane's periodic upkeep, outside the send path: read the mined
// nonce, mark the allocations it has passed mined (raw bytes dropped), and
// clear a hold the chain has passed. It returns an error while the lane is
// held or the mined nonce is unreadable, so the scheduler's failure-streak
// ALERT fires while either lasts.
func (l *KeyLane) Watch(ctx context.Context) error {
	if l.nonces == nil {
		return l.heldErr()
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	mined, err := l.nonces.LatestNonce(rctx, l.Address())
	cancel()
	if err != nil {
		return fmt.Errorf("key lane %d/%s: read the mined nonce: %w", l.chainID, l.Address(), err)
	}
	mctx, mcancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	if err := l.store.MarkMined(mctx, l.chainID, l.Address(), mined); err != nil {
		log.Printf("[signer] key lane: mark allocations below %d mined: %v", mined, err)
	}
	mcancel()
	l.gapMu.Lock()
	for n := range l.gapSeen {
		if n < mined {
			delete(l.gapSeen, n)
		}
	}
	l.gapMu.Unlock()
	if h := l.held.Load(); h != nil && mined > h.nonce {
		log.Printf("[signer] key lane hold cleared chain=%d address=%s nonce=%d (mined nonce %d)", l.chainID, l.Address(), h.nonce, mined)
		l.held.Store(nil)
	}
	return l.heldErr()
}

// logAddress is the lane's address ("" on a solo lane that signs with its
// caller's signer).
func (l *KeyLane) logAddress() string {
	if l.signer == nil {
		return ""
	}
	return l.signer.Address()
}

func (l *KeyLane) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// SignerRegistry hands out one KeyLane per (address, chain id), process-wide.
// Keyed by chain ID, never chain key, so BSC testnet 97 and BSC mainnet 56 stay
// apart and two roles on one key and chain always share one lock and floor,
// whatever their own client. The lane's broadcaster is the first one
// registered for that (address, chain id); a later role's client still builds
// its own transactions (its nonce read is covered by the floor).
type SignerRegistry struct {
	mu    sync.Mutex
	lanes map[keyLaneID]*KeyLane
	roles map[keyLaneID][]string
	store KeyLaneStore
}

// SetStore gives every lane, existing and future, the durable record store.
// Boot sets it before the first send.
func (r *SignerRegistry) SetStore(store KeyLaneStore) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	for _, l := range r.lanes {
		l.store = store
	}
}

// LaneFor is the registry's lane for (chainID, address), nil if none.
func (r *SignerRegistry) LaneFor(chainID int64, address string) *KeyLane {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lanes[keyLaneID{address: strings.ToLower(address), chainID: chainID}]
}

// Watch runs every lane's upkeep (KeyLane.Watch) and joins their errors.
func (r *SignerRegistry) Watch(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	lanes := make([]*KeyLane, 0, len(r.lanes))
	for _, l := range r.lanes {
		lanes = append(lanes, l)
	}
	r.mu.Unlock()
	var errs []error
	for _, l := range lanes {
		if err := l.Watch(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// KeyLaneStatus is one lane as the admin route shows it.
type KeyLaneStatus struct {
	ChainID    int64  `json:"chainId"`
	Address    string `json:"address"`
	Roles      string `json:"roles"`
	Held       bool   `json:"held"`
	HeldNonce  uint64 `json:"heldNonce,omitempty"`
	HeldReason string `json:"heldReason,omitempty"`
}

// Status is every lane, for the super-admin view.
func (r *SignerRegistry) Status(ctx context.Context) []KeyLaneStatus {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	lanes := make([]*KeyLane, 0, len(r.lanes))
	for _, l := range r.lanes {
		lanes = append(lanes, l)
	}
	r.mu.Unlock()
	out := make([]KeyLaneStatus, 0, len(lanes))
	for _, l := range lanes {
		st := KeyLaneStatus{ChainID: l.chainID, Address: l.Address(), Roles: l.roles}
		if n, why, held := l.Held(); held {
			st.Held, st.HeldNonce, st.HeldReason = true, n, why
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChainID != out[j].ChainID {
			return out[i].ChainID < out[j].ChainID
		}
		return out[i].Address < out[j].Address
	})
	return out
}

type keyLaneID struct {
	address string // lower-case hex
	chainID int64
}

// NewSignerRegistry is a registry on an in-memory store; boot gives it the
// database (SetStore) before the first send.
func NewSignerRegistry() *SignerRegistry {
	return &SignerRegistry{lanes: map[keyLaneID]*KeyLane{}, roles: map[keyLaneID][]string{}, store: newMemKeyLaneStore()}
}

// Lane is key's lane on bc's chain, created on first use; role names who asked
// (for the boot summary). A nil registry hands out a private lane, which is
// what a test or a caller without a registry gets. Nil when bc reports no chain.
func (r *SignerRegistry) Lane(role string, key *ecdsa.PrivateKey, bc ReleaseBroadcaster) *KeyLane {
	if bc == nil || bc.ChainID() == nil {
		return nil
	}
	chainID := bc.ChainID().Int64()
	if r == nil {
		return newBroadcasterLane(key, bc, chainID, nil)
	}
	id := keyLaneID{address: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), chainID: chainID}
	r.mu.Lock()
	defer r.mu.Unlock()
	lane := r.lanes[id]
	if lane == nil {
		lane = newBroadcasterLane(key, bc, chainID, r.store)
		r.lanes[id] = lane
	}
	if role != "" && !containsString(r.roles[id], role) {
		r.roles[id] = append(r.roles[id], role)
		lane.roles = strings.Join(r.roles[id], ",")
	}
	return lane
}

// newBroadcasterLane is key's lane over bc, with bc's balance read and
// transaction lookup when it has them.
func newBroadcasterLane(key *ecdsa.PrivateKey, bc ReleaseBroadcaster, chainID int64, store KeyLaneStore) *KeyLane {
	l := newKeyLane(NewLocalKeyReleaseSigner(key, bc), chainID, store)
	l.balances, _ = bc.(laneBalanceReader)
	l.state, _ = bc.(laneStateReader)
	l.lookup, _ = bc.(laneTxLookup)
	l.nonces, _ = bc.(laneNonceReader)
	l.pricer, _ = bc.(laneGasPricer)
	return l
}

// KeyLaneSummary is one address's lanes, for the boot line.
type KeyLaneSummary struct {
	Address string
	Roles   []string
	Chains  []int64
}

// Summary is every address the registry has lanes for, sorted, with the roles
// that asked for one and the chain ids it sends on.
func (r *SignerRegistry) Summary() []KeyLaneSummary {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byAddr := map[string]*KeyLaneSummary{}
	for id, lane := range r.lanes {
		s := byAddr[id.address]
		if s == nil {
			s = &KeyLaneSummary{Address: lane.Address()}
			byAddr[id.address] = s
		}
		s.Chains = append(s.Chains, id.chainID)
		for _, role := range r.roles[id] {
			if !containsString(s.Roles, role) {
				s.Roles = append(s.Roles, role)
			}
		}
	}
	out := make([]KeyLaneSummary, 0, len(byAddr))
	for _, s := range byAddr {
		sort.Slice(s.Chains, func(i, j int) bool { return s.Chains[i] < s.Chains[j] })
		sort.Strings(s.Roles)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
