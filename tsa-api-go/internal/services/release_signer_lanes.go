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
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// KeyLane is the ONE send lane of one hot key on one chain (signer
// consolidation 2026-10-13): the in-process lock, the one fee gate every send
// on the key passes, and the bridge to the DB-authoritative OUTBOX of its
// nonces (KeyLaneStore). Every role signing with that key on that chain (PM
// release, expiry cancel, IT operator, gas sponsor, treasury) sends through
// the same KeyLane, and every process allocates from the same store under a
// per-(chain, address) advisory lock. Get one from a SignerRegistry; the lint
// test forbids building one anywhere else.
//
// Outbox (owner rule: a nonce only ever carries ONE call, across processes and
// restarts):
//   - A. Under the advisory lock, in one DB transaction, with no network I/O:
//     choose the nonce, sign, write the allocation and its signing ('signed'),
//     commit. Chain reads (pending, mined, balance, price) happen before.
//   - B. After the commit, outside every lock: hand the nonce to the caller
//     (onPrepared), broadcast, then record the outcome in a short transaction.
//   - C. A nonce is never given to another call. A refused or lost signing is
//     re-sent with the SAME call (its stored bytes, or a capped speed-up); a
//     call its caller abandoned is replaced by a 0-value self-transfer
//     ('filled'), never by another call.
//   - D. Speed-ups are decided, signed and committed under the lock (bumps
//     counted in the committed row), broadcast after the commit.
//   - E. The caller's operation key (WithOperationRef) is the allocation's
//     idempotency key: retrying the same operation gets the same allocation,
//     never a new nonce.
type KeyLane struct {
	sem     chan struct{} // the in-process lane lock, acquired ctx-aware
	signer  ReleaseSigner
	chainID int64 // what the lane's broadcaster reports; 0 = unchecked (a solo lane)

	held    atomic.Pointer[laneHold]
	gapMu   sync.Mutex
	gapSeen map[uint64]time.Time // when a hole no process allocated was first seen

	bootMu    sync.Mutex
	bootFloor uint64 // the first-use floor (empty table): pending read twice, and mined
	booted    bool

	balances laneBalanceReader // nil: no fee gate (a solo test lane)
	state    laneStateReader   // balance and mined nonce at one block (consistent)
	lookup   laneTxLookup      // nil: an unseen tx is treated as unknown to the node
	nonces   laneNonceReader   // nil: holds never clear by themselves
	pending  lanePendingReader // nil: no first-use floor read
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

// lanePendingReader is the chain's pending nonce (the first-use floor).
type lanePendingReader interface {
	PendingNonce(ctx context.Context, address string) (uint64, error)
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

// keyLaneBootReread is the gap before the first-use floor's second pending
// read (a var so tests can shorten it).
var keyLaneBootReread = time.Second

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

type operationRefKey struct{}

// WithOperationRef names the caller's operation (a trade's release, a sale
// leg, an IT debit): the lane's idempotency key for it. Retrying the same
// operation — same call, same ref — gets the existing allocation back, never a
// new nonce, so a caller retry can never pay twice.
func WithOperationRef(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, operationRefKey{}, ref)
}

func operationRef(ctx context.Context) string {
	v, _ := ctx.Value(operationRefKey{}).(string)
	return v
}

// ErrOperationFilled: the operation's call was abandoned and its nonce filled
// with a self-transfer; the same operation is never sent again.
var ErrOperationFilled = errors.New("this operation's signing was abandoned and its nonce filled; it will not be sent")

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
		// The outbox commits the exact bytes before they leave; a signer that
		// cannot hand them over (a future KMS without raw signing) is refused.
		return "", false, notBroadcast(errors.New("key lane: this signer cannot sign without broadcasting; refusing to send unrecorded"))
	}
	address := signer.Address()
	fee, err := l.readFeeInputs(ctx, address)
	if err != nil {
		return "", false, err
	}
	floor, err := l.firstUseFloor(ctx, address)
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
	pending := u.Nonce
	// Upkeep at the chain's pending nonce first (network outside any lock).
	l.reviewGap(ctx, signer, pending, fee)
	if err := l.heldErr(); err != nil {
		return "", false, err
	}
	fp, op := callFingerprint(u), operationRef(ctx)
	if pending < floor {
		pending = floor
	}
	if fee != nil && fee.minedKnown && fee.mined > pending {
		pending = fee.mined
	}
	var alloc *models.KeyLaneNonce
	var row *models.KeyLaneTx
	reused := false
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		alloc, row, reused = nil, nil, false
		existing, err := q.ByOp(op)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Fingerprint != fp {
				return notBroadcast(fmt.Errorf("key lane: operation %q already holds nonce %d for a different call", op, existing.Nonce))
			}
			switch existing.Status {
			case models.KeyLaneNonceFilled:
				return notBroadcast(ErrOperationFilled)
			case models.KeyLaneNonceAbandoned:
				// The same call again at its own nonce (a fresh signing).
				u.Nonce = uint64(existing.Nonce)
				if err := l.gateLocked(q, address, fee, &u); err != nil {
					return err
				}
				// Never an identical transaction: price it above every
				// earlier signing of this call.
				prior, err := q.Signings(existing.ID)
				if err != nil {
					return err
				}
				if price, ok := new(big.Int).SetString(u.GasPrice, 10); ok {
					for _, t := range prior {
						if prev, ok := new(big.Int).SetString(t.GasPrice, 10); ok && prev.Cmp(price) >= 0 {
							price.Add(prev, big.NewInt(1))
						}
					}
					u.GasPrice = price.String()
				}
				alloc = existing
				if isRaw {
					if _, row, err = l.signLocked(q, rs, existing, u); err != nil {
						return err
					}
				}
				alloc.Status, alloc.Handed, alloc.ActedAt = models.KeyLaneNonceSigned, onPrepared == nil, l.clock()
				return q.Save(alloc)
			default:
				alloc, reused = existing, true
				return nil
			}
		}
		n := pending
		if max, ok, err := q.MaxNonce(); err != nil {
			return err
		} else if ok && uint64(max)+1 > n {
			n = uint64(max) + 1
		}
		u.Nonce = n
		if err := l.gateLocked(q, address, fee, &u); err != nil {
			return err
		}
		a := &models.KeyLaneNonce{Nonce: int64(n), OpKey: op, Fingerprint: fp, Status: models.KeyLaneNonceSigned,
			Role: l.roles, Handed: onPrepared == nil, BumpLimit: keyLaneMaxBumps, ActedAt: l.clock()}
		if err := q.Create(a); err != nil {
			return err
		}
		alloc = a
		if isRaw {
			_, row, err = l.signLocked(q, rs, a, u)
		}
		return err
	})
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) {
			return "", false, err
		}
		return "", false, notBroadcast(fmt.Errorf("key lane: allocate and sign: %w", err))
	}
	if reused {
		// The same operation again: never a new nonce. The lane's upkeep
		// drives its signing; the caller gets its current hash.
		return l.FinalHash(ctx, l.currentHash(ctx, alloc)), true, nil
	}
	if onPrepared != nil {
		if err := onPrepared(uint64(alloc.Nonce)); err != nil {
			l.abandon(ctx, address, alloc.ID)
			return "", false, fmt.Errorf("key lane: record the nonce before broadcast: %w", err)
		}
		if err := l.markHanded(ctx, address, alloc.ID); err != nil {
			l.abandon(ctx, address, alloc.ID)
			return "", false, notBroadcast(fmt.Errorf("key lane: mark the nonce handed: %w", err))
		}
	}
	if !isRaw {
		return l.sendUnrecorded(ctx, signer, address, alloc, u)
	}
	txHash, berr := rs.broadcastRaw(row.RawTx, row.Hash)
	l.recordOutcome(ctx, address, alloc.ID, row.ID, berr)
	if errors.Is(berr, ErrTxNotBroadcast) {
		return "", false, berr
	}
	return txHash, true, berr
}

// sendUnrecorded is the in-memory store's path for a signer that cannot sign
// without broadcasting (test fakes; never on the database store): the
// allocation is committed first as always, the signing recorded once the
// signer reports its hash.
func (l *KeyLane) sendUnrecorded(ctx context.Context, signer ReleaseSigner, address string, alloc *models.KeyLaneNonce, u blockchain.UnsignedTx) (string, bool, error) {
	unsigned, err := json.Marshal(u)
	if err != nil {
		l.abandon(ctx, address, alloc.ID)
		return "", false, notBroadcast(err)
	}
	txHash, berr := signer.SignAndBroadcast(ctx, unsigned)
	h := txHash
	if berr != nil {
		h = uncertainTxHash(berr)
	}
	if h == "" {
		h = "unrecorded-" + alloc.ID.String()
	}
	row := laneTxRow(alloc, u, h, nil, l.clock())
	if err := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error { return q.RecordTx(row) }); err != nil {
		log.Printf("[signer] key lane: record a test signer's tx: %v", err)
	}
	l.recordOutcome(ctx, address, alloc.ID, row.ID, berr)
	if errors.Is(berr, ErrTxNotBroadcast) {
		return "", false, berr
	}
	return txHash, true, berr
}

// gateLocked is the fee gate inside the lock: the committed spend of the
// lane's live allocations the balance read has not paid, then requireFee.
func (l *KeyLane) gateLocked(q KeyLaneTxn, address string, fee *feeInputs, u *blockchain.UnsignedTx) error {
	if fee == nil {
		return nil
	}
	committed, err := q.Committed(fee.committedFrom())
	if err != nil {
		return err
	}
	return l.requireFee(address, fee, committed, u)
}

// signLocked signs u for allocation a and records the signing ('signed').
// Local CPU and the database only.
func (l *KeyLane) signLocked(q KeyLaneTxn, rs rawSigner, a *models.KeyLaneNonce, u blockchain.UnsignedTx) (*models.KeyLaneNonce, *models.KeyLaneTx, error) {
	unsigned, err := json.Marshal(u)
	if err != nil {
		return nil, nil, err
	}
	raw, hash, err := rs.signRaw(unsigned)
	if err != nil {
		return nil, nil, err
	}
	row := laneTxRow(a, u, hash, raw, l.clock())
	if err := q.RecordTx(row); err != nil {
		return nil, nil, err
	}
	return a, row, nil
}

// laneTxRow is a signing's row.
func laneTxRow(a *models.KeyLaneNonce, u blockchain.UnsignedTx, hash string, raw []byte, at time.Time) *models.KeyLaneTx {
	return &models.KeyLaneTx{AllocationID: a.ID, Nonce: a.Nonce, Hash: hash, RawTx: raw, To: u.To, Data: u.Data,
		Value: u.Value, GasLimit: int64(u.GasLimit), GasPrice: u.GasPrice, Status: models.KeyLaneTxSigned, CreatedAt: at}
}

// currentHash is the allocation's current signing's hash ("" if none).
func (l *KeyLane) currentHash(ctx context.Context, a *models.KeyLaneNonce) string {
	txs, err := l.store.Signings(ctx, a.ID)
	if err != nil {
		return ""
	}
	if cur := currentSigning(a, txs); cur != nil {
		return cur.Hash
	}
	return ""
}

// firstUseFloor is the lane's floor before its first allocation (an empty
// table, e.g. the first deploy): the pending nonce read twice a moment apart
// (a process on the old binary may still be sending) and the mined nonce.
// Computed once; a failed read refuses the send (retried next time).
func (l *KeyLane) firstUseFloor(ctx context.Context, address string) (uint64, error) {
	l.bootMu.Lock()
	defer l.bootMu.Unlock()
	if l.booted {
		return l.bootFloor, nil
	}
	if _, ok, err := l.store.MaxNonce(ctx, l.chainID, address); err != nil {
		return 0, notBroadcast(fmt.Errorf("key lane: read the lane's allocations: %w", err))
	} else if ok || l.pending == nil {
		l.booted = true
		return l.bootFloor, nil
	}
	var floor uint64
	for i := 0; i < 2; i++ {
		if i > 0 {
			select {
			case <-time.After(keyLaneBootReread):
			case <-ctx.Done():
				return 0, notBroadcast(ctx.Err())
			}
		}
		rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
		p, err := l.pending.PendingNonce(rctx, address)
		cancel()
		if err != nil {
			return 0, notBroadcast(fmt.Errorf("key lane: first-use floor: %w", err))
		}
		if p > floor {
			floor = p
		}
	}
	if l.nonces != nil {
		rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
		if m, err := l.nonces.LatestNonce(rctx, address); err == nil && m > floor {
			floor = m
		}
		cancel()
	}
	l.bootFloor, l.booted = floor, true
	log.Printf("[signer] key lane first use chain=%d address=%s floor=%d (pending read twice, mined)", l.chainID, address, floor)
	return floor, nil
}

// abandon marks a signed allocation its caller never took: it is never
// broadcast, and the upkeep fills its nonce unless the same operation retries.
func (l *KeyLane) abandon(ctx context.Context, address string, allocID uuid.UUID) {
	err := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error {
		return l.updateAlloc(q, allocID, func(a *models.KeyLaneNonce) bool {
			if a.Status != models.KeyLaneNonceSigned {
				return false
			}
			a.Status = models.KeyLaneNonceAbandoned
			return true
		})
	})
	if err != nil {
		log.Printf("[signer] ALERT key_lane_abandon_failed_total=1 chain=%d address=%s alloc=%s err=%v — the upkeep abandons it once its grace passes unhanded",
			l.chainID, address, allocID, err)
	}
}

func (l *KeyLane) markHanded(ctx context.Context, address string, allocID uuid.UUID) error {
	return l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error {
		return l.updateAlloc(q, allocID, func(a *models.KeyLaneNonce) bool { a.Handed = true; return true })
	})
}

// updateAlloc reloads an allocation by id inside the lock and saves it when
// change reports a change.
func (l *KeyLane) updateAlloc(q KeyLaneTxn, allocID uuid.UUID, change func(a *models.KeyLaneNonce) bool) error {
	a, err := l.allocByID(q, allocID)
	if err != nil || a == nil {
		return err
	}
	if !change(a) {
		return nil
	}
	return q.Save(a)
}

// allocByID reloads an allocation by id inside the lock.
func (l *KeyLane) allocByID(q KeyLaneTxn, allocID uuid.UUID) (*models.KeyLaneNonce, error) {
	return q.ByID(allocID)
}

// recordOutcome records one broadcast's outcome (a short transaction after
// it, never around it): accepted — the allocation's current signing, its
// others replaced; refused — those bytes never left (an allocation with no
// other live signing is abandoned, to be filled); otherwise uncertain.
func (l *KeyLane) recordOutcome(ctx context.Context, address string, allocID, txID uuid.UUID, berr error) {
	err := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error {
		a, err := l.allocByID(q, allocID)
		if err != nil || a == nil {
			return err
		}
		txs, err := q.Signings(a.ID)
		if err != nil {
			return err
		}
		var row *models.KeyLaneTx
		for i := range txs {
			if txs[i].ID == txID {
				row = &txs[i]
			}
		}
		if row == nil {
			return nil
		}
		switch {
		case berr == nil:
			for _, t := range txs {
				if t.ID != txID && containsString(liveTxStatuses, t.Status) {
					if err := q.SetTxStatus(t.ID, models.KeyLaneTxReplaced); err != nil {
						return err
					}
				}
			}
			if err := q.SetTxStatus(txID, models.KeyLaneTxAccepted); err != nil {
				return err
			}
			a.AcceptedHash, a.AcceptedPrice, a.AcceptedBumps = row.Hash, row.GasPrice, a.Bumps
			if a.Status == models.KeyLaneNonceSigned {
				a.Status = models.KeyLaneNonceSent
			}
			return q.Save(a)
		case errors.Is(berr, ErrTxNotBroadcast):
			if row.Status != models.KeyLaneTxSigned {
				// A re-broadcast of bytes that already left once: they may
				// still mine, so the signing stays live.
				return nil
			}
			if err := q.SetTxStatus(txID, models.KeyLaneTxRefused); err != nil {
				return err
			}
			live := false
			for _, t := range txs {
				if t.ID != txID && containsString(liveTxStatuses, t.Status) {
					live = true
				}
			}
			if !live && a.Status == models.KeyLaneNonceSigned {
				a.Status = models.KeyLaneNonceAbandoned // its call never left: filled unless retried
				return q.Save(a)
			}
			return nil
		default:
			if err := q.SetTxStatus(txID, models.KeyLaneTxUncertain); err != nil {
				return err
			}
			if a.Status == models.KeyLaneNonceSigned {
				a.Status = models.KeyLaneNonceSent
				return q.Save(a)
			}
			return nil
		}
	})
	if err != nil {
		log.Printf("[signer] key lane: record the outcome of %s: %v (the upkeep re-reads it)", txID, err)
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

// reviewGap is the lane's upkeep at the chain's pending nonce p: an
// allocation of ours whose signing the node lacks (re-broadcast its bytes,
// then a capped same-call speed-up), a call its caller abandoned (fill), or a
// hole no process allocated (hold). Every chain read happens here, outside any
// lock; each mutation is a short locked transaction that re-checks the state
// it decided on, signing there and broadcasting after its commit. Transient
// read failures do nothing (the next send or watch tick retries).
func (l *KeyLane) reviewGap(ctx context.Context, signer ReleaseSigner, p uint64, fee *feeInputs) {
	now := l.clock()
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	address := signer.Address()
	a, err := l.store.Get(rctx, l.chainID, address, p)
	if err != nil {
		return
	}
	if a == nil {
		above, err := l.store.HasUnminedAbove(rctx, l.chainID, address, p)
		if err != nil || !above {
			l.forgetGap(p)
			return
		}
		// Our allocations wait above p, and no process ever allocated p.
		if l.nonces == nil {
			return
		}
		mined, err := l.nonces.LatestNonce(rctx, address)
		if err != nil || mined > p {
			return // unknown, or p is mined: the pending view lags
		}
		if first := l.noteGap(p, now); now.Sub(first) >= keyLaneGapGrace {
			l.holdLocked(p, "no process allocated this nonce — something outside the server used the key")
		}
		return
	}
	if a.Status == models.KeyLaneNonceMined || now.Sub(a.ActedAt) < keyLaneGapGrace {
		return
	}
	if l.nonces != nil {
		if mined, err := l.nonces.LatestNonce(rctx, address); err == nil && mined > p {
			return
		}
	}
	if a.Status == models.KeyLaneNonceSigned && !a.Handed {
		l.abandon(ctx, address, a.ID) // its caller never took it: filled next
		return
	}
	if a.Status == models.KeyLaneNonceAbandoned {
		_, _ = l.fill(ctx, signer, a, false)
		return
	}
	txs, err := l.store.Signings(rctx, a.ID)
	if err != nil {
		return
	}
	cur := currentSigning(a, txs)
	if cur == nil {
		l.holdLocked(p, "this nonce is allocated but no live signing of it is recorded")
		return
	}
	if l.lookup != nil {
		for _, t := range txs {
			if t.Status == models.KeyLaneTxRefused {
				continue
			}
			known, err := l.lookup.TransactionKnown(rctx, t.Hash)
			if err != nil {
				log.Printf("[signer] key lane: nonce %d tx %s unverified (%s); not re-sent, retried later", p, t.Hash, RedactAlertText(err.Error()))
				return
			}
			if known {
				return
			}
		}
	}
	rs, isRaw := signer.(rawSigner)
	if isRaw && cur.RawTx != nil && a.Rebroadcasts == 0 {
		claimed := false
		_ = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
			return l.updateAlloc(q, a.ID, func(x *models.KeyLaneNonce) bool {
				if x.Rebroadcasts != 0 || x.Status == models.KeyLaneNonceMined {
					return false
				}
				x.Rebroadcasts, x.ActedAt, claimed = 1, l.clock(), true
				return true
			})
		})
		if !claimed {
			return
		}
		_, berr := rs.broadcastRaw(cur.RawTx, cur.Hash)
		log.Printf("[signer] ALERT key_lane_nonce_gap_rebroadcast_total=1 chain=%d address=%s nonce=%d tx=%s err=%v",
			l.chainID, address, p, cur.Hash, berr)
		l.recordOutcome(ctx, address, a.ID, cur.ID, berr)
		if !errors.Is(berr, ErrTxNotBroadcast) {
			return // accepted, or uncertain: look again after the grace
		}
		// Definitely refused (underpriced, below the base fee): speed it up now.
	}
	l.speedUp(ctx, signer, a, cur, fee)
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

// speedUp re-signs the SAME call at its nonce at a higher fee — decided,
// signed and committed under the lock (the bump counted in the committed row),
// broadcast after the commit. Price: one +12.5% step per signing since the
// last ACCEPTED one (a refused signing is never the base), above every
// earlier signing of the call, at least today's price, never above
// min(keyLaneLiveCapMultiple × today's price, the chain's ceiling), at most
// BumpLimit bumps, through the fee gate. Past a cap, or without a raw signer
// or price, the lane holds.
func (l *KeyLane) speedUp(ctx context.Context, signer ReleaseSigner, a *models.KeyLaneNonce, cur *models.KeyLaneTx, fee *feeInputs) {
	p := uint64(a.Nonce)
	rs, isRaw := signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		l.holdLocked(p, "the transaction at this nonce is gone and cannot be re-signed here")
		return
	}
	live, err := l.pricer.SuggestGasPrice() // network: before the lock
	if err != nil || live == nil || live.Sign() <= 0 {
		return // transient
	}
	address := signer.Address()
	var row *models.KeyLaneTx
	var holdReason string
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		x, err := l.allocByID(q, a.ID)
		if err != nil || x == nil {
			return err
		}
		if x.Status == models.KeyLaneNonceMined || x.Bumps != a.Bumps {
			return nil // decided elsewhere meanwhile
		}
		if x.Bumps >= x.BumpLimit {
			holdReason = fmt.Sprintf("speed-up limit reached (%d bumps)", x.Bumps)
			return nil
		}
		txs, err := q.Signings(x.ID)
		if err != nil {
			return err
		}
		price, why := speedUpPrice(x, txs, live, l.chainID)
		if price == nil {
			holdReason = why
			return nil
		}
		base := currentSigning(x, txs)
		if base == nil {
			base = cur
		}
		call := blockchain.UnsignedTx{Nonce: p, GasPrice: price.String(), GasLimit: uint64(base.GasLimit), To: base.To,
			Value: base.Value, Data: base.Data, ChainID: strconv.FormatInt(l.chainID, 10)}
		if fee != nil {
			committed, err := q.Committed(fee.committedFrom())
			if err != nil {
				return err
			}
			replacing := new(big.Int)
			addSpend(replacing, base)
			committed.Sub(committed, replacing)
			owed := *fee
			owed.reservedUnits = 0 // the speed-up IS owed work
			if err := l.requireFee(address, &owed, committed, &call); err != nil {
				return nil // alerted; retried later
			}
		}
		if _, row, err = l.signLocked(q, rs, x, call); err != nil {
			return err
		}
		x.Bumps, x.ActedAt = x.Bumps+1, l.clock()
		return q.Save(x)
	})
	if holdReason != "" {
		l.holdLocked(p, holdReason)
		return
	}
	if err != nil || row == nil {
		return
	}
	_, berr := rs.broadcastRaw(row.RawTx, row.Hash)
	l.recordOutcome(ctx, address, a.ID, row.ID, berr)
	log.Printf("[signer] ALERT key_lane_speed_up_total=1 chain=%d address=%s nonce=%d old_tx=%s new_tx=%s gas_price=%s err=%v",
		l.chainID, address, p, cur.Hash, row.Hash, row.GasPrice, berr)
}

// speedUpPrice is a speed-up's price for allocation a with signings txs, or
// nil and why it may not bump: one +12.5% step per bump since the last
// accepted signing, from the last ACCEPTED price (the first signing's when
// none was accepted), raised to today's price, strictly above every earlier
// signing (never an identical transaction), capped at
// min(keyLaneLiveCapMultiple × live, the chain's ceiling).
func speedUpPrice(a *models.KeyLaneNonce, txs []models.KeyLaneTx, live *big.Int, chainID int64) (*big.Int, string) {
	basePrice := a.AcceptedPrice
	if basePrice == "" && len(txs) > 0 {
		basePrice = txs[0].GasPrice
	}
	base, ok := new(big.Int).SetString(basePrice, 10)
	if !ok {
		return nil, "the transaction at this nonce has no readable gas price"
	}
	price := new(big.Int).Set(base)
	steps := a.Bumps - a.AcceptedBumps + 1
	if steps < 1 {
		steps = 1
	}
	for i := 0; i < steps; i++ {
		price.Mul(price, big.NewInt(keyLaneSpeedUpBumpPermille)).Div(price, big.NewInt(1000)).Add(price, big.NewInt(1))
	}
	if live.Cmp(price) > 0 {
		price.Set(live)
	}
	for _, t := range txs {
		if prev, ok := new(big.Int).SetString(t.GasPrice, 10); ok && prev.Cmp(price) >= 0 {
			price.Add(prev, big.NewInt(1))
		}
	}
	ceiling := new(big.Int).Mul(live, big.NewInt(keyLaneLiveCapMultiple))
	if abs := keyLaneFeeCeiling(chainID); abs.Cmp(ceiling) < 0 {
		ceiling = abs
	}
	if price.Cmp(ceiling) > 0 {
		return nil, fmt.Sprintf("speed-up fee cap reached: next price %s wei, cap %s", price, ceiling)
	}
	return price, ""
}

// fill replaces an abandoned call (or, for FillGap, a hole no process
// allocated) with a 0-value self-transfer at its nonce, signed and committed
// under the lock (status 'filled'), broadcast after. exact: a new allocation
// at a hole (admin); else the abandoned allocation a is filled.
func (l *KeyLane) fill(ctx context.Context, signer ReleaseSigner, a *models.KeyLaneNonce, exact bool) (string, error) {
	rs, isRaw := signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		return "", errors.New("key lane: this lane cannot sign a fill")
	}
	price, err := l.pricer.SuggestGasPrice() // network: before the lock
	if err != nil {
		return "", fmt.Errorf("key lane: gas price: %w", err)
	}
	price = new(big.Int).Div(new(big.Int).Mul(price, big.NewInt(keyLaneSpeedUpBumpPermille)), big.NewInt(1000))
	address := signer.Address()
	call := blockchain.UnsignedTx{Nonce: uint64(a.Nonce), GasPrice: price.String(), GasLimit: 21_000, To: address, Value: "0",
		ChainID: strconv.FormatInt(l.chainID, 10)}
	var alloc *models.KeyLaneNonce
	var row *models.KeyLaneTx
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		x, err := q.Get(uint64(a.Nonce))
		if err != nil {
			return err
		}
		switch {
		case exact && x != nil:
			return fmt.Errorf("key lane: nonce %d is in the record (%s, role %q); only a hole no process allocated is filled", a.Nonce, x.Status, x.Role)
		case exact:
			x = &models.KeyLaneNonce{Nonce: a.Nonce, Fingerprint: callFingerprint(call), Status: models.KeyLaneNonceFilled,
				Role: "gap_fill", Handed: true, BumpLimit: keyLaneMaxBumps, ActedAt: l.clock()}
			if err := q.Create(x); err != nil {
				return err
			}
		case x == nil || x.ID != a.ID || x.Status != models.KeyLaneNonceAbandoned:
			return nil // decided elsewhere meanwhile
		default:
			x.Status, x.Handed, x.ActedAt = models.KeyLaneNonceFilled, true, l.clock()
			x.AcceptedHash, x.AcceptedPrice, x.AcceptedBumps, x.Bumps, x.Rebroadcasts = "", "", 0, 0, 0
			if err := q.Save(x); err != nil {
				return err
			}
		}
		alloc = x
		_, row, err = l.signLocked(q, rs, x, call)
		return err
	})
	if err != nil || row == nil {
		return "", err
	}
	_, berr := rs.broadcastRaw(row.RawTx, row.Hash)
	l.recordOutcome(ctx, address, alloc.ID, row.ID, berr)
	log.Printf("[signer] ALERT key_lane_nonce_filled_total=1 chain=%d address=%s nonce=%d tx=%s err=%v — a 0-value self-transfer took this nonce",
		l.chainID, address, a.Nonce, row.Hash, berr)
	if errors.Is(berr, ErrTxNotBroadcast) {
		return row.Hash, berr
	}
	return row.Hash, nil
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
			a, err := q.Get(h.nonce)
			if err != nil || a == nil {
				return err
			}
			a.ActedAt = l.clock()
			return q.Save(a)
		})
	}
	l.held.Store(nil)
	return nil
}

// RetrySpeedUp is a super-admin's retry of a nonce held at the speed-up
// limit: the limit is raised by keyLaneMaxBumps (the count is kept, so the
// next speed-up steps past every earlier signing — never an identical
// transaction) and the hold cleared. The fee cap still applies.
func (l *KeyLane) RetrySpeedUp(ctx context.Context, nonce uint64) error {
	err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		a, err := q.Get(nonce)
		if err != nil {
			return err
		}
		if a == nil || a.Status == models.KeyLaneNonceMined || a.Status == models.KeyLaneNonceAbandoned {
			return fmt.Errorf("key lane: nonce %d has no live allocation", nonce)
		}
		a.BumpLimit, a.ActedAt = a.Bumps+keyLaneMaxBumps, time.Time{}
		return q.Save(a)
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
// that nonce, allocated and committed before its broadcast. Refused for any
// nonce in the record, or one the chain has mined.
func (l *KeyLane) FillGap(ctx context.Context, nonce uint64) (string, error) {
	if l.nonces != nil {
		rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
		mined, err := l.nonces.LatestNonce(rctx, l.Address())
		cancel()
		if err != nil {
			return "", fmt.Errorf("key lane: read the mined nonce: %w", err)
		}
		if mined > nonce {
			return "", fmt.Errorf("key lane: nonce %d is already mined", nonce)
		}
	}
	hash, err := l.fill(ctx, l.signer, &models.KeyLaneNonce{Nonce: int64(nonce)}, true)
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
	if l == nil || l.signer == nil || hash == "" {
		return hash
	}
	h, err := l.store.FinalHash(ctx, l.chainID, l.Address(), hash)
	if err != nil || h == "" {
		return hash
	}
	return h
}

// signingsOf is every live signing of hash's call (the original and any
// speed-ups), or [hash].
func (l *KeyLane) signingsOf(ctx context.Context, hash string) []string {
	if l == nil || l.signer == nil {
		return []string{hash}
	}
	hs, err := l.store.SigningsOf(ctx, l.chainID, l.Address(), hash)
	if err != nil || len(hs) == 0 {
		return []string{hash}
	}
	return hs
}

// waitFollowingReplacements waits up to timeout for the receipt of hash's
// call: each round it polls EVERY signing of that call (the original may
// still mine after a speed-up was sent), returning the first receipt found
// and the hash it belongs to. wait is one bounded receipt wait.
func waitFollowingReplacements(ctx context.Context, lane *KeyLane, hash string, timeout time.Duration,
	wait func(ctx context.Context, hash string, d time.Duration) (*types.Receipt, error)) (*types.Receipt, string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		hashes := lane.signingsOf(ctx, hash)
		left := time.Until(deadline)
		if left <= 0 {
			return nil, hash, lastErr
		}
		round := 5 * time.Second
		if left < round {
			round = left
		}
		per := round / time.Duration(len(hashes))
		early := true
		for _, h := range hashes {
			started := time.Now()
			r, err := wait(ctx, h, per)
			if err == nil {
				return r, h, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return nil, h, lastErr
			}
			if time.Since(started) >= per-50*time.Millisecond {
				early = false
			}
		}
		// Every wait failed before its slice was up (the wait's own answer, not
		// a timeout) and no new signing appeared: stop.
		if early && len(lane.signingsOf(ctx, hash)) == len(hashes) {
			return nil, hash, lastErr
		}
	}
}

// Watch is the lane's periodic upkeep: read the mined nonce, mark the
// allocations it has passed mined (raw bytes dropped), clear a hold the chain
// has passed, and review the allocation at the mined nonce (re-send, speed
// up, fill) so an idle lane still moves. It returns an error while the lane
// is held or the mined nonce is unreadable (failure-streak ALERT).
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
	if l.held.Load() == nil {
		l.reviewGap(ctx, l.signer, mined, nil)
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
	l.pending, _ = bc.(lanePendingReader)
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
