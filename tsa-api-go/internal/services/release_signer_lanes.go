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

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
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
//     run the caller's pre-broadcast hook (WithPreBroadcast: its own business
//     write, on the same transaction), commit. A hook error rolls all of it
//     back: nothing signed survives. Chain reads happen before.
//   - B. After the commit, outside every lock: broadcast, then record the
//     outcome in a short transaction.
//   - C. A committed signing IS its nonce's transaction until the chain mines
//     that nonce. Refused or lost, the lane re-sends the same bytes or speeds
//     up the SAME call (capped) until it mines; it never gives the nonce up
//     and never fills it. Only a hole no process allocated is ever filled (a
//     super-admin's FillGap).
//   - D. Speed-ups are decided, signed and committed under the lock (bumps
//     counted in the committed row, through the fee gate and the lane's
//     reservations), broadcast after the commit.
//   - E. The caller's operation key (WithOperationRef) is the allocation's
//     idempotency key: retrying the same operation gets the same allocation
//     and its honest status (ErrPendingBroadcast until a node accepted it),
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
	receipts laneReceiptReader // nil: a cancel is never resolved (it stays in flight)
	// finality is the configured confirmation depth (RAMP_TREASURY_CONFIRMATIONS
	// and peers, set at boot); a cancel is final only at max(it, keyLaneCancelDepth).
	finality     uint64
	cancelAlerts *alertLimiter // rate-limits the unproven-cancel ALERT
	clearSeen    atomic.Int64  // the shared hold's clear version this process last applied
	store        KeyLaneStore  // never nil: the database at boot, memory otherwise
	roles        string

	reserveMu sync.Mutex
	reserves  map[string]LaneReservation

	now func() time.Time
}

type laneHold struct {
	nonce     uint64
	since     time.Time
	reason    string
	persisted atomic.Bool // written to key_lane_states (else re-persisted by Watch)
	clearAt   int64       // the shared clear version when this hold was taken
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

// laneReceiptReader reads one receipt without waiting (ethereum.NotFound while
// unmined) and the latest block: what proves a cancel deep enough to be final.
type laneReceiptReader interface {
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	BlockNumber(ctx context.Context) (uint64, error)
}

// keyLaneCancelDepth is how deep (blocks, counting its own, against the
// latest block) a cancel's receipt must be before the call is final as
// cancelled — the depth pool releases and treasury sales are proven at
// (RAMP_TREASURY_CONFIRMATIONS' default). Until then a reorg could still put
// the original call in, so its operation stays in flight.
const keyLaneCancelDepth = 5

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
		gapSeen: map[uint64]time.Time{}, cancelAlerts: &alertLimiter{last: map[string]time.Time{}}}
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

// ErrPendingBroadcast: the call is signed and committed at its nonce, but no
// node has accepted it yet. It is in flight — the lane keeps sending that same
// call until its nonce mines — so the caller treats it exactly like an
// uncertain broadcast, never as "not sent". The error carries the signing's
// hash (uncertainTxHash) and never matches ErrTxNotBroadcast.
var ErrPendingBroadcast = errors.New("transaction committed but not yet accepted by a node; the key lane keeps sending it")

// pendingBroadcast is the in-flight result for a committed signing hash.
func pendingBroadcast(hash string, cause error) error {
	err := ErrPendingBroadcast
	if cause != nil {
		err = fmt.Errorf("%w (%s)", ErrPendingBroadcast, cause.Error())
	}
	return broadcastUncertainError{err: err, txHash: hash}
}

// LaneHook is a caller's pre-broadcast write: run inside the lane's
// allocation transaction (under the lane's lock, before the commit), with the
// nonce and the hash of the signed call. It must touch only the database —
// through LaneDB(ctx, db), which is that same transaction — never the network.
// Its error rolls the allocation back: nothing is signed or sent.
type LaneHook func(ctx context.Context, nonce uint64, hash string) error

type preBroadcastKey struct{}

// WithPreBroadcast attaches the caller's pre-broadcast hook to the send; nil
// clears one inherited from ctx.
func WithPreBroadcast(ctx context.Context, hook LaneHook) context.Context {
	return context.WithValue(ctx, preBroadcastKey{}, hook)
}

func preBroadcastHook(ctx context.Context) LaneHook {
	h, _ := ctx.Value(preBroadcastKey{}).(LaneHook)
	return h
}

type laneTxKey struct{}

// LaneDB is the database a LaneHook writes through: the lane's own
// allocation transaction when ctx is a hook's, else db on ctx.
func LaneDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(laneTxKey{}).(*gorm.DB); ok && tx != nil {
		return tx
	}
	return db.WithContext(ctx)
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
// checks the key can pay, signs, commits (with the ctx's WithPreBroadcast
// hook) and broadcasts. attempted is false only when nothing was committed
// (ErrTxNotBroadcast, or a build error returned as build returned it). Once
// committed the call is in flight: a broadcast that was not accepted returns
// its hash with ErrPendingBroadcast (attempted true).
func (l *KeyLane) Send(ctx context.Context, build func() ([]byte, error)) (txHash string, attempted bool, err error) {
	return l.sendWith(ctx, l.signer, build)
}

// SendRecorded is Send with hook as the pre-broadcast write.
func (l *KeyLane) SendRecorded(ctx context.Context, build func() ([]byte, error), hook LaneHook) (string, bool, error) {
	return l.sendWith(WithPreBroadcast(ctx, hook), l.signer, build)
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
// (an error there can only over-count) — and, when reserve, the lane's
// reservation, with the caller's ctx (bounded), BEFORE any lock: a hung read
// holds up only its own send. nil on a lane without a fee gate.
func (l *KeyLane) readFeeInputs(ctx context.Context, address string, reserve bool) (*feeInputs, error) {
	if l.balances == nil && l.state == nil {
		return nil, nil
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	in := &feeInputs{}
	if reserve {
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
// sendWith is the structural give-up rule at the lane boundary: an
// operation (WithOperationRef) is only ever answered "not sent" after
// RetireOp proved, under the lane lock, that it holds no allocation — every
// refusal (a hold, a fee or floor read, a build error, the fee gate, the
// caller's hook) goes through it, and an allocation found there is answered
// in flight instead. Callers may then fail, free or retry on that answer.
func (l *KeyLane) sendWith(ctx context.Context, signer ReleaseSigner, build func() ([]byte, error)) (string, bool, error) {
	hash, attempted, err := l.sendCore(ctx, signer, build)
	op := operationRef(ctx)
	if attempted || op == "" || errors.Is(err, errOperationMismatch) {
		return hash, attempted, err
	}
	return l.giveUpRefused(ctx, signer.Address(), op, err)
}

// errOperationMismatch: the operation key already holds another call (a
// caller bug) — refused without retiring the key.
var errOperationMismatch = errors.New("key lane: this operation already holds a different call")

// giveUpRefused retires op after a refusal that committed nothing: the
// refusal stands only if op holds no allocation; otherwise that allocation is
// the honest answer (in flight), and a retire that cannot run is an unknown
// outcome — never "not sent".
func (l *KeyLane) giveUpRefused(ctx context.Context, address, op string, cause error) (string, bool, error) {
	a, err := l.RetireOp(ctx, address, op, "", false)
	switch {
	case err != nil:
		return "", true, outcomeUnknown("", fmt.Errorf("%v; give-up unproven: %w", cause, err))
	case a != nil:
		return l.inFlight(ctx, a)
	}
	return "", false, cause
}

func (l *KeyLane) sendCore(ctx context.Context, signer ReleaseSigner, build func() ([]byte, error)) (string, bool, error) {
	rs, isRaw := signer.(rawSigner)
	if !isRaw && l.store.Durable() {
		// The outbox commits the exact bytes before they leave; a signer that
		// cannot hand them over (a future KMS without raw signing) is refused.
		return "", false, notBroadcast(errors.New("key lane: this signer cannot sign without broadcasting; refusing to send unrecorded"))
	}
	address := signer.Address()
	var seenSeq int64 // the operation's give-up sequence when this attempt began
	if op := operationRef(ctx); op != "" {
		// The same operation again: its committed allocation's honest status,
		// before any fee read or hold can refuse it as "not sent" (it may be
		// in flight).
		rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
		existing, err := l.store.ByOp(rctx, l.chainID, address, op)
		if err == nil {
			seenSeq, err = l.store.RetiredSeq(rctx, l.chainID, address, op)
		}
		cancel()
		if err != nil {
			// The operation may hold an allocation we could not read: never
			// "not sent".
			return "", true, outcomeUnknown("", fmt.Errorf("key lane: read the operation's allocation: %w", err))
		}
		if existing != nil {
			return l.retried(ctx, existing, build)
		}
	}
	if err := l.heldErr(); err != nil { // refused before any read or build
		return "", false, err
	}
	fee, err := l.readFeeInputs(ctx, address, !isReservedSend(ctx))
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
	l.reviewGap(ctx, signer, pending)
	if err := l.heldErr(); err != nil {
		return "", false, err
	}
	fp, op, hook := callFingerprint(u), operationRef(ctx), preBroadcastHook(ctx)
	if pending < floor {
		pending = floor
	}
	if fee != nil && fee.minedKnown && fee.mined > pending {
		pending = fee.mined
	}
	var alloc, reused *models.KeyLaneNonce
	var row *models.KeyLaneTx
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		alloc, reused, row = nil, nil, nil
		existing, err := q.ByOp(op)
		if err != nil {
			return err
		}
		if existing == nil {
			// The shared hold: another process (or an admin elsewhere) may
			// have held this lane since this process last looked. A committed
			// operation is answered (reused, below) even on a held lane.
			if st, err := q.Hold(); err != nil {
				return err
			} else if st != nil && st.HeldNonce != nil {
				l.adoptHold(st)
				return notBroadcast(fmt.Errorf("%w (nonce %d: %s)", ErrKeyLaneHeld, *st.HeldNonce, st.HeldReason))
			}
			// A give-up of this operation after this attempt began: this is
			// the late attempt the caller gave up on — never committed.
			if seq, permanent, err := q.RetiredSeq(op); err != nil {
				return err
			} else if permanent || seq > seenSeq {
				return notBroadcast(fmt.Errorf("%w: %q", ErrOperationRetired, op))
			}
		}
		if existing != nil {
			if existing.Fingerprint != fp {
				return notBroadcast(fmt.Errorf("%w: operation %q holds nonce %d", errOperationMismatch, op, existing.Nonce))
			}
			reused = existing // the same operation again: never a new nonce
			return nil
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
			Role: l.roles, BumpLimit: keyLaneMaxBumps, ActedAt: l.clock(), Reserved: isReservedSend(ctx)}
		if err := q.Create(a); err != nil {
			return err
		}
		alloc = a
		if !isRaw {
			return nil // the in-memory test path: the hook runs once the signer reports its hash
		}
		if _, row, err = l.signLocked(q, rs, a, u); err != nil {
			return err
		}
		if hook != nil {
			if err := hook(context.WithValue(ctx, laneTxKey{}, q.DB()), n, row.Hash); err != nil {
				return notBroadcast(fmt.Errorf("key lane: the caller's pre-broadcast record: %w", err))
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) {
			return "", false, err // the body refused: rolled back
		}
		// Any other error may be a COMMIT that happened (a connection lost on
		// COMMIT) — on a fresh allocation or a reused one. Re-take the lane
		// lock (so any commit in flight has finished) and look.
		return l.recheckCommit(ctx, address, op, alloc, row, err)
	}
	switch {
	case reused != nil:
		return l.inFlight(ctx, reused)
	case alloc == nil:
		return "", false, notBroadcast(errors.New("key lane: the allocation transaction produced nothing"))
	case !isRaw:
		return l.sendUnrecorded(ctx, signer, address, alloc, u, hook)
	case row == nil:
		// Committed without a recorded signing cannot happen; if it ever
		// does, the call is not provably unsent.
		return "", true, outcomeUnknown("", errors.New("key lane: committed allocation without its signing"))
	}
	txHash, berr := rs.broadcastRaw(row.RawTx, row.Hash)
	l.recordOutcome(ctx, address, alloc.ID, row.ID, berr)
	if berr != nil {
		// Committed: the lane sends this call until its nonce mines.
		return row.Hash, true, pendingBroadcast(row.Hash, berr)
	}
	return txHash, true, nil
}

// ErrSendOutcomeUnknown: the lane could not tell whether the call was
// committed (the allocation transaction failed ambiguously and the re-check
// failed too). Callers treat it as in flight — never as not sent.
var ErrSendOutcomeUnknown = errors.New("key lane: whether this call was committed is unknown; treat it as in flight")

// outcomeUnknown is ErrSendOutcomeUnknown carrying the hash when known.
func outcomeUnknown(hash string, cause error) error {
	return broadcastUncertainError{err: fmt.Errorf("%w (%v)", ErrSendOutcomeUnknown, cause), txHash: hash}
}

// recheckCommit settles an allocation transaction that failed other than by
// its own refusal: under the re-taken lane lock (a detached, bounded ctx), the
// operation's allocation — or the one this call created — is looked up.
// Committed: in flight (its hash, ErrPendingBroadcast or its status);
// provably absent: not sent; the re-check failing: outcome unknown.
func (l *KeyLane) recheckCommit(ctx context.Context, address, op string, alloc *models.KeyLaneNonce, row *models.KeyLaneTx, cause error) (string, bool, error) {
	hash := ""
	if row != nil {
		hash = row.Hash
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyLaneReadTimeout)
	defer cancel()
	var found *models.KeyLaneNonce
	err := l.store.WithLock(rctx, l.chainID, address, func(q KeyLaneTxn) error {
		if op != "" {
			var err error
			if found, err = q.ByOp(op); err != nil || found != nil {
				return err
			}
		}
		if alloc != nil {
			a, err := q.Get(uint64(alloc.Nonce))
			if err != nil {
				return err
			}
			if a != nil && a.ID == alloc.ID {
				found = a
			}
		}
		return nil
	})
	switch {
	case err != nil:
		log.Printf("[signer] ALERT key_lane_commit_unknown_total=1 chain=%d address=%s err=%v recheck=%v", l.chainID, address, cause, err)
		return hash, true, outcomeUnknown(hash, cause)
	case found != nil:
		log.Printf("[signer] ALERT key_lane_commit_error_but_committed_total=1 chain=%d address=%s nonce=%d err=%v", l.chainID, address, found.Nonce, cause)
		h, attempted, ierr := l.inFlight(rctx, found)
		if ierr == nil && found.AcceptedHash == "" {
			ierr = pendingBroadcast(h, cause)
		}
		return h, attempted, ierr
	}
	return "", false, notBroadcast(fmt.Errorf("key lane: allocate and sign: %w", cause))
}

// emulatePreBroadcast runs ctx's pre-broadcast hook outside any lane, for a
// test ChainSender standing in for the lane (fakeReleaseSigner): there is no
// allocation transaction, so LaneDB falls back to the caller's database.
func emulatePreBroadcast(ctx context.Context, nonce uint64, hash string) error {
	if hook := preBroadcastHook(ctx); hook != nil {
		return hook(ctx, nonce, hash)
	}
	return nil
}

// retried answers an operation that already holds an allocation: a
// different call under the same operation key is refused (built under the
// lane lock to compare); otherwise — or when the build cannot run — the
// allocation's honest status. Never a new nonce.
func (l *KeyLane) retried(ctx context.Context, a *models.KeyLaneNonce, build func() ([]byte, error)) (string, bool, error) {
	if err := l.lock(ctx); err == nil {
		unsigned, berr := build()
		l.unlock()
		var u blockchain.UnsignedTx
		if berr == nil && json.Unmarshal(unsigned, &u) == nil && callFingerprint(u) != a.Fingerprint {
			return "", false, notBroadcast(fmt.Errorf("%w: operation %q holds nonce %d", errOperationMismatch, a.OpKey, a.Nonce))
		}
	}
	return l.inFlight(ctx, a)
}

// inFlight is a retried operation's answer: its allocation's current hash,
// with ErrPendingBroadcast until a node has accepted a signing of it.
func (l *KeyLane) inFlight(ctx context.Context, a *models.KeyLaneNonce) (string, bool, error) {
	hash := l.currentHash(ctx, a)
	switch {
	case a.Status == models.KeyLaneNonceCancelled:
		return hash, true, ErrCallCancelled
	case a.Cancel:
		return hash, true, pendingBroadcast(hash, errors.New("being cancelled: it can never be sent"))
	case a.AcceptedHash != "" || a.Status == models.KeyLaneNonceMined:
		return hash, true, nil
	}
	return hash, true, pendingBroadcast(hash, nil)
}

// sendUnrecorded is the in-memory store's path for a signer that cannot sign
// without broadcasting (test fakes; never on the database store): the
// allocation is committed first as always, the signing (and the caller's hook,
// which therefore runs after this broadcast) recorded once the signer reports
// its hash.
func (l *KeyLane) sendUnrecorded(ctx context.Context, signer ReleaseSigner, address string, alloc *models.KeyLaneNonce, u blockchain.UnsignedTx, hook LaneHook) (string, bool, error) {
	unsigned, err := json.Marshal(u)
	if err != nil {
		_ = l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error { return q.Withdraw(alloc.ID) })
		return "", false, notBroadcast(err)
	}
	txHash, berr := signer.SignAndBroadcast(ctx, unsigned)
	h := txHash
	if berr != nil {
		h = uncertainTxHash(berr)
	}
	known := h != ""
	if !known {
		h = "unrecorded-" + alloc.ID.String()
	}
	row := laneTxRow(alloc, u, h, nil, l.clock())
	if err := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error {
		if err := q.RecordTx(row); err != nil {
			return err
		}
		if hook != nil && known && !errors.Is(berr, ErrTxNotBroadcast) {
			// After the broadcast here (it already happened): the record must
			// land even if the caller's ctx ended meanwhile.
			return hook(context.WithValue(context.WithoutCancel(ctx), laneTxKey{}, q.DB()), u.Nonce, h)
		}
		return nil
	}); err != nil {
		log.Printf("[signer] key lane: record a test signer's tx: %v", err)
	}
	if errors.Is(berr, ErrTxNotBroadcast) {
		// Signed and broadcast in one call, refused: nothing left. On this
		// test-only path the allocation is withdrawn, so "not sent" stays true
		// for the give-up rule.
		_ = l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error { return q.Withdraw(alloc.ID) })
		return "", false, berr
	}
	l.recordOutcome(ctx, address, alloc.ID, row.ID, berr)
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

// currentHash is the current signing of the allocation's OWN call ("" if
// none) — never its cancel.
func (l *KeyLane) currentHash(ctx context.Context, a *models.KeyLaneNonce) string {
	txs, err := l.store.Signings(ctx, a.ID)
	if err != nil {
		return ""
	}
	if cur := currentSigning(a, sameCall(txs, a.Fingerprint)); cur != nil {
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

// allocByID reloads an allocation by id inside the lock.
func (l *KeyLane) allocByID(q KeyLaneTxn, allocID uuid.UUID) (*models.KeyLaneNonce, error) {
	return q.ByID(allocID)
}

// recordOutcome records one broadcast's outcome (a short transaction after
// it, never around it): accepted — the allocation's current signing, its
// others replaced; refused — those bytes never left (the allocation stays
// committed: the upkeep re-sends or speeds up the same call); otherwise
// uncertain.
func (l *KeyLane) recordOutcome(ctx context.Context, address string, allocID, txID uuid.UUID, berr error) {
	var holdNonce uint64
	var holdReason string
	defer func() {
		if holdReason != "" {
			l.hold(address, holdNonce, holdReason) // after the lock: hold takes it itself
		}
	}()
	err := l.store.WithLock(context.WithoutCancel(ctx), l.chainID, address, func(q KeyLaneTxn) error {
		a, err := l.allocByID(q, allocID)
		if err != nil || a == nil || a.Status == models.KeyLaneNonceMined {
			return err // a mined allocation is final: an outcome never moves it back
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
			kind := unsendableKind(berr)
			if kind == "" || a.Cancel || callFingerprint(blockchain.UnsignedTx{To: row.To, Data: row.Data, Value: row.Value}) != a.Fingerprint {
				return nil
			}
			switch {
			case a.AcceptedHash != "":
				// An earlier, cheaper signing of the call is accepted and may
				// still mine: stop bumping (a price refusal only grows), never
				// cancel it.
				holdNonce, holdReason = uint64(a.Nonce), "speed-up refused for good ("+kind+"); an earlier signing is accepted: not bumped further"
				a.BumpLimit = a.Bumps
				return q.Save(a)
			case kind == unsendableIntrinsic || lowestPriced(row, sameCall(txs, a.Fingerprint)):
				// No signing of this call can ever be accepted: replace it at
				// its nonce with a cancel, now.
				log.Printf("[signer] ALERT key_lane_call_unsendable_total=1 chain=%d address=%s nonce=%d tx=%s kind=%s err=%s — cancelling it",
					l.chainID, address, a.Nonce, row.Hash, kind, RedactAlertText(berr.Error()))
				a.Cancel, a.ActedAt = true, time.Time{}
				return q.Save(a)
			default:
				holdNonce, holdReason = uint64(a.Nonce), "a speed-up was refused for good ("+kind+") above a cheaper signing that was refused for another reason"
				return nil
			}
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
	in, err := l.readFeeInputs(ctx, address, !isReservedSend(ctx))
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
// then a capped same-call speed-up, until it mines), or a hole no process
// allocated (hold). Every chain read happens here, outside any lock; each
// mutation is a short locked transaction that re-checks the state it decided
// on, signing there and broadcasting after its commit. Transient read failures
// do nothing (the next send or watch tick retries).
func (l *KeyLane) reviewGap(ctx context.Context, signer ReleaseSigner, p uint64) {
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
			l.hold(address, p, "no process allocated this nonce — something outside the server used the key")
		}
		return
	}
	if a.Status == models.KeyLaneNonceMined || a.Status == models.KeyLaneNonceCancelled {
		return
	}
	if a.Cancel {
		l.reviewCancel(ctx, signer, a)
		return
	}
	if now.Sub(a.ActedAt) < keyLaneGapGrace {
		return
	}
	if l.nonces != nil {
		if mined, err := l.nonces.LatestNonce(rctx, address); err == nil && mined > p {
			return
		}
	}
	txs, err := l.store.Signings(rctx, a.ID)
	if err != nil {
		return
	}
	cur := currentSigning(a, txs)
	if cur == nil {
		l.hold(address, p, "this nonce is allocated but no signing of it is recorded")
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
	if isRaw && cur.RawTx != nil && cur.Status != models.KeyLaneTxRefused && a.Rebroadcasts == 0 {
		claimed := false
		_ = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
			x, err := l.allocByID(q, a.ID)
			if err != nil || x == nil || x.Rebroadcasts != 0 || x.Version != a.Version {
				return err
			}
			x.Rebroadcasts, x.ActedAt = 1, l.clock()
			if err := q.Save(x); err != nil {
				return err
			}
			claimed = true
			return nil
		})
		if !claimed {
			return
		}
		a.Version++
		_, berr := rs.broadcastRaw(cur.RawTx, cur.Hash)
		log.Printf("[signer] ALERT key_lane_nonce_gap_rebroadcast_total=1 chain=%d address=%s nonce=%d tx=%s err=%v",
			l.chainID, address, p, cur.Hash, berr)
		l.recordOutcome(ctx, address, a.ID, cur.ID, berr)
		if !errors.Is(berr, ErrTxNotBroadcast) {
			return // accepted, or uncertain: look again after the grace
		}
		// Definitely refused (underpriced, below the base fee): speed it up now.
		if fresh, err := l.store.Get(rctx, l.chainID, address, p); err == nil && fresh != nil {
			a = fresh
		}
	}
	l.speedUp(ctx, signer, a, cur)
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
// BumpLimit bumps, through the fee gate with the lane's reservations (a
// speed-up never spends what owed work needs). Past a cap, or without a raw
// signer or price, the lane holds; a speed-up the balance cannot pay never
// holds — it is retried every tick and goes once the key is funded.
func (l *KeyLane) speedUp(ctx context.Context, signer ReleaseSigner, a *models.KeyLaneNonce, cur *models.KeyLaneTx) {
	p := uint64(a.Nonce)
	rs, isRaw := signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		l.hold(signer.Address(), p, "the transaction at this nonce is gone and cannot be re-signed here")
		return
	}
	address := signer.Address()
	// An owed call (ReservedSend) is what the reservation is held for: its
	// speed-up is gated as its send was, never against its own reservation.
	fee, err := l.readFeeInputs(ctx, address, !a.Reserved) // network: before the lock
	if err != nil {
		log.Printf("[signer] key lane: nonce %d speed-up deferred: %s", p, RedactAlertText(err.Error()))
		return // transient
	}
	live, err := l.pricer.SuggestGasPrice()
	if err != nil || live == nil || live.Sign() <= 0 {
		return // transient
	}
	var row *models.KeyLaneTx
	var holdReason string
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		x, err := l.allocByID(q, a.ID)
		if err != nil || x == nil {
			return err
		}
		if x.Status == models.KeyLaneNonceMined || x.Status == models.KeyLaneNonceCancelled || x.Cancel || x.Version != a.Version {
			return nil // decided elsewhere meanwhile (a cancel is never sped up as the call)
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
			if err := l.requireFee(address, fee, committed, &call); err != nil {
				return nil // alerted; retried every tick until the key is funded
			}
		}
		if _, row, err = l.signLocked(q, rs, x, call); err != nil {
			return err
		}
		x.Bumps, x.ActedAt = x.Bumps+1, l.clock()
		return q.Save(x)
	})
	if holdReason != "" {
		l.hold(address, p, holdReason)
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
// min(keyLaneLiveCapMultiple × live, the chain's ceiling) — or at the
// allocation's own PriceCap when a super-admin raised it.
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
	if override, ok := new(big.Int).SetString(a.PriceCap, 10); ok && override.Sign() > 0 {
		ceiling = override
	}
	if price.Cmp(ceiling) > 0 {
		return nil, fmt.Sprintf("speed-up fee cap reached: next price %s wei, cap %s", price, ceiling)
	}
	return price, ""
}

// fillHole signs a 0-value self-transfer at a hole NO process allocated (a
// super-admin's FillGap): allocated, signed and committed under the lock,
// broadcast after. Refused for any nonce in the record.
func (l *KeyLane) fillHole(ctx context.Context, nonce uint64) (string, error) {
	rs, isRaw := l.signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		return "", errors.New("key lane: this lane cannot sign a fill")
	}
	price, err := l.pricer.SuggestGasPrice() // network: before the lock
	if err != nil {
		return "", fmt.Errorf("key lane: gas price: %w", err)
	}
	price = new(big.Int).Div(new(big.Int).Mul(price, big.NewInt(keyLaneSpeedUpBumpPermille)), big.NewInt(1000))
	address := l.signer.Address()
	call := blockchain.UnsignedTx{Nonce: nonce, GasPrice: price.String(), GasLimit: 21_000, To: address, Value: "0",
		ChainID: strconv.FormatInt(l.chainID, 10)}
	var alloc *models.KeyLaneNonce
	var row *models.KeyLaneTx
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		x, err := q.Get(nonce)
		if err != nil {
			return err
		}
		if x != nil {
			return fmt.Errorf("key lane: nonce %d is in the record (%s, role %q); only a hole no process allocated is filled", nonce, x.Status, x.Role)
		}
		x = &models.KeyLaneNonce{Nonce: int64(nonce), Fingerprint: callFingerprint(call), Status: models.KeyLaneNonceSigned,
			Role: "gap_fill", BumpLimit: keyLaneMaxBumps, ActedAt: l.clock()}
		if err := q.Create(x); err != nil {
			return err
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
	log.Printf("[signer] ALERT key_lane_nonce_filled_total=1 chain=%d address=%s nonce=%d tx=%s err=%v — a 0-value self-transfer took this foreign hole",
		l.chainID, address, nonce, row.Hash, berr)
	return row.Hash, nil // committed: the upkeep drives it like any other call
}

// ErrCallCancelled: the call's nonce mined the lane's cancel (a 0-value
// self-transfer), keyLaneCancelDepth deep — the call provably never executed
// and never will. Shallower, the call is still in flight. The
// caller fails its operation and retries it as a NEW operation (new nonce).
var ErrCallCancelled = errors.New("the key lane cancelled this call: its nonce mined a 0-value self-transfer, the call never executed")

// Kinds of a node refusal no re-send of the SAME signing gets past.
const (
	unsendableIntrinsic = "call_intrinsic" // gas the call can never fit (intrinsic gas, block gas limit)
	unsendableFeeCap    = "fee_cap"        // the RPC's tx-fee cap: price dependent
)

// unsendableKind classifies a node refusal explicitly ("" = retried): a
// call-intrinsic refusal no signing of the call can pass; a fee-cap refusal
// only a cheaper signing could.
func unsendableKind(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "intrinsic gas too low"), strings.Contains(msg, "exceeds block gas limit"):
		return unsendableIntrinsic
	case strings.Contains(msg, "exceeds the configured cap"): // geth/bor txfeecap
		return unsendableFeeCap
	}
	return ""
}

// lowestPriced: row is the cheapest signing among txs (no cheaper one exists
// to fall back to).
func lowestPriced(row *models.KeyLaneTx, txs []models.KeyLaneTx) bool {
	p, ok := new(big.Int).SetString(row.GasPrice, 10)
	if !ok {
		return false
	}
	for _, t := range txs {
		if q, ok := new(big.Int).SetString(t.GasPrice, 10); ok && q.Cmp(p) < 0 {
			return false
		}
	}
	return true
}

// cancelFingerprint is the lane's cancel call: a 0-value self-transfer.
func cancelFingerprint(address string) string {
	return callFingerprint(blockchain.UnsignedTx{To: address, Value: "0"})
}

// reviewCancel drives an allocation marked Cancel: a cancel signing the node
// holds is left alone; otherwise (none yet, or the last one lost past the
// grace) a cancel is signed at the nonce priced strictly above every earlier
// signing (+12.5%) and at least today's price, within the same cap as a
// speed-up (the allocation's PriceCap override included), through the fee
// gate. Past the cap the lane holds.
func (l *KeyLane) reviewCancel(ctx context.Context, signer ReleaseSigner, a *models.KeyLaneNonce) {
	p, address := uint64(a.Nonce), signer.Address()
	rs, isRaw := signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		l.hold(address, p, "a call that can never be sent must be cancelled, and this lane cannot sign the cancel")
		return
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	txs, err := l.store.Signings(rctx, a.ID)
	if err != nil {
		return
	}
	cfp := cancelFingerprint(address)
	if cancels := sameCall(txs, cfp); len(cancels) > 0 {
		if l.clock().Sub(a.ActedAt) < keyLaneGapGrace || l.lookup == nil {
			return
		}
		for _, t := range cancels {
			if known, err := l.lookup.TransactionKnown(rctx, t.Hash); err != nil || known {
				return // held by the node (or unknowable): wait
			}
		}
	}
	fee, err := l.readFeeInputs(ctx, address, !a.Reserved)
	if err != nil {
		return
	}
	live, err := l.pricer.SuggestGasPrice()
	if err != nil || live == nil || live.Sign() <= 0 {
		return
	}
	var row *models.KeyLaneTx
	var holdReason string
	err = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
		x, err := l.allocByID(q, a.ID)
		if err != nil || x == nil || x.Version != a.Version || !x.Cancel || x.Status == models.KeyLaneNonceMined || x.Status == models.KeyLaneNonceCancelled {
			return err
		}
		txs, err := q.Signings(x.ID)
		if err != nil {
			return err
		}
		price := new(big.Int).Set(live)
		for _, t := range txs {
			if prev, ok := new(big.Int).SetString(t.GasPrice, 10); ok {
				step := new(big.Int).Div(new(big.Int).Mul(prev, big.NewInt(keyLaneSpeedUpBumpPermille)), big.NewInt(1000))
				step.Add(step, big.NewInt(1))
				if step.Cmp(price) > 0 {
					price = step
				}
			}
		}
		ceiling := new(big.Int).Mul(live, big.NewInt(keyLaneLiveCapMultiple))
		if abs := keyLaneFeeCeiling(l.chainID); abs.Cmp(ceiling) < 0 {
			ceiling = abs
		}
		if override, ok := new(big.Int).SetString(x.PriceCap, 10); ok && override.Sign() > 0 {
			ceiling = override
		}
		if price.Cmp(ceiling) > 0 {
			holdReason = fmt.Sprintf("cancel fee cap reached: cancel price %s wei, cap %s (raise-price-cap)", price, ceiling)
			return nil
		}
		call := blockchain.UnsignedTx{Nonce: p, GasPrice: price.String(), GasLimit: 21_000, To: address, Value: "0",
			ChainID: strconv.FormatInt(l.chainID, 10)}
		if fee != nil {
			committed, err := q.Committed(fee.committedFrom())
			if err != nil {
				return err
			}
			replacing := new(big.Int)
			addSpend(replacing, currentSigning(x, txs))
			committed.Sub(committed, replacing)
			if err := l.requireFee(address, fee, committed, &call); err != nil {
				return nil // alerted; retried every tick until the key is funded
			}
		}
		if _, row, err = l.signLocked(q, rs, x, call); err != nil {
			return err
		}
		x.ActedAt = l.clock()
		return q.Save(x)
	})
	if holdReason != "" {
		l.hold(address, p, holdReason)
		return
	}
	if err != nil || row == nil {
		return
	}
	_, berr := rs.broadcastRaw(row.RawTx, row.Hash)
	l.recordOutcome(ctx, address, a.ID, row.ID, berr)
	log.Printf("[signer] ALERT key_lane_cancel_sent_total=1 chain=%d address=%s nonce=%d tx=%s gas_price=%s err=%v",
		l.chainID, address, p, row.Hash, row.GasPrice, berr)
}

// resolveCancels settles every cancelling allocation the mined nonce has
// passed, re-read on every tick from receipts (non-waiting): a receipt of the
// call's OWN signing means the call executed — mined, whatever was decided
// before (a reorg that put the original in); a cancel receipt
// keyLaneCancelDepth deep against the latest block makes it final as
// cancelled (its operation key freed; verifiers report ErrCallCancelled).
// Anything shallower, missing or unreadable stays in flight for the next tick.
func (l *KeyLane) resolveCancels(ctx context.Context, mined uint64) {
	if l.receipts == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	address := l.Address()
	list, err := l.store.Cancelling(rctx, l.chainID, address, mined)
	if err != nil || len(list) == 0 {
		return
	}
	head, err := l.receipts.BlockNumber(rctx)
	if err != nil {
		return
	}
	cfp := cancelFingerprint(address)
	for i := range list {
		a := list[i]
		txs, err := l.store.Signings(rctx, a.ID)
		if err != nil {
			continue
		}
		minedAt := func(ts []models.KeyLaneTx) (uint64, bool, error) {
			for _, t := range ts {
				b, ok, err := l.receiptBlock(rctx, t.Hash)
				if err != nil || ok {
					return b, ok, err
				}
			}
			return 0, false, nil
		}
		_, executed, err1 := minedAt(sameCall(txs, a.Fingerprint))
		cancelBlock, cancelMined, err2 := minedAt(sameCall(txs, cfp))
		if err1 != nil || err2 != nil {
			continue // unreadable: next tick
		}
		var to string
		switch {
		case executed:
			to = models.KeyLaneNonceMined
		case cancelMined && l.deepEnough(head, cancelBlock):
			to = models.KeyLaneNonceCancelled
		default:
			continue // not mined yet, or the cancel not yet deep: in flight
		}
		_ = l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error {
			x, err := l.allocByID(q, a.ID)
			if err != nil || x == nil || x.Version != a.Version {
				return err
			}
			x.Status, x.OpKey = to, ""
			return q.Save(x)
		})
		log.Printf("[signer] key lane nonce %d resolved %s (chain=%d address=%s head=%d)", a.Nonce, to, l.chainID, address, head)
	}
}

// RequestCancel is a super-admin's cancel of a committed call that is stuck
// (it can never be sent, or must not be): the upkeep replaces it at its nonce
// with a 0-value self-transfer, as for a call refused permanently. The hold at
// that nonce, if any, is cleared. Refused for a nonce with no live allocation.
func (l *KeyLane) RequestCancel(ctx context.Context, nonce uint64) error {
	err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		a, err := q.Get(nonce)
		if err != nil {
			return err
		}
		if a == nil || a.Status == models.KeyLaneNonceMined || a.Status == models.KeyLaneNonceCancelled {
			return fmt.Errorf("key lane: nonce %d has no live allocation", nonce)
		}
		a.Cancel, a.ActedAt = true, time.Time{}
		if err := q.Save(a); err != nil {
			return err
		}
		return unholdLocked(q, &nonce)
	})
	if err != nil {
		return err
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	return nil
}

// ErrOperationRetired: the caller retired this operation (RetireOp) after
// proving it was never allocated; it is never sent (nothing is committed).
var ErrOperationRetired = errors.New("key lane: this operation was retired and is never sent")

// RetireOp is the ONLY way a caller gives up on an operation (fails or
// frees its record, clears a claim, rolls back, retries on a new operation):
// under the lane lock it proves op holds no live allocation — by its key and,
// when hash is given, by the allocation hash belongs to (live or executed
// counts; one resolved cancelled does not) — and bumps op's tombstone so an
// attempt of it begun earlier is refused at its commit. A non-nil allocation
// means the operation is in flight: the caller must not give up. A caller
// giving up on a cancelled call keeps its CallCancelled proof first.
//
// permanent: no attempt of op may ever commit again (its retry is a new
// operation); otherwise only attempts begun before this give-up are refused,
// and a retry of the same operation begun after it proceeds.
func (l *KeyLane) RetireOp(ctx context.Context, address, op, hash string, permanent bool) (*models.KeyLaneNonce, error) {
	if l == nil || (op == "" && hash == "") {
		return nil, nil
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyLaneReadTimeout)
	defer cancel()
	var found *models.KeyLaneNonce
	err := l.store.WithLock(rctx, l.chainID, address, func(q KeyLaneTxn) error {
		var err error
		found, err = q.RetireOp(op, hash, permanent)
		return err
	})
	return found, err
}

// GiveUp is RetireOp for a caller: true only when it may abandon the
// operation (nothing live or executed holds it); any doubt is false (in
// flight). A nil lane (no key lane wired) cannot have committed it.
func (l *KeyLane) GiveUp(ctx context.Context, address, op, hash string) bool {
	if l == nil {
		return true
	}
	a, err := l.RetireOp(ctx, address, op, hash, false)
	return err == nil && a == nil
}

// cancelDepth is the depth a cancel must reach before its call is final as
// cancelled: max(keyLaneCancelDepth, the configured confirmations).
func (l *KeyLane) cancelDepth() uint64 {
	if l.finality > keyLaneCancelDepth {
		return l.finality
	}
	return keyLaneCancelDepth
}

// CallOutcome is the ONE verifier result every caller reads for a hash this
// lane sent, from one store read (allocation + its signings): cancelled —
// the allocation resolved as cancelled AND the chain shows the nonce mined by
// the cancel signing (not the call) at cancelDepth against the latest block,
// re-checked now (ErrCallCancelled; anything else is in flight or the
// original); otherwise, with hasReceipt, the signing of the SAME call that
// has a receipt (refused ones included; hash when none). A read error is
// returned with hash: callers defer.
func (l *KeyLane) CallOutcome(ctx context.Context, hash string, hasReceipt func(ctx context.Context, h string) (bool, error)) (mined string, cancelled bool, err error) {
	if l == nil || l.signer == nil || hash == "" {
		return hash, false, nil
	}
	a, txs, err := l.store.AllocationOf(ctx, l.chainID, l.Address(), hash)
	if err != nil || a == nil {
		return hash, false, err
	}
	if a.Status == models.KeyLaneNonceCancelled {
		ok, err := l.cancelProven(ctx, a, txs)
		if err != nil {
			return hash, false, err
		}
		if ok {
			return hash, true, nil
		}
		// Recorded cancelled but not proven now: the original (or a speed-up)
		// may be what mined — follow the call's signings like any other.
		if l.cancelAlerts == nil || l.cancelAlerts.allow(fmt.Sprintf("%d", a.Nonce), time.Hour) {
			log.Printf("[signer] ALERT key_lane_cancel_unproven_total=1 chain=%d nonce=%d tx=%s — recorded cancelled, the chain does not show the cancel final: treated as in flight (repeats hourly)", l.chainID, a.Nonce, hash)
		}
	}
	if hasReceipt == nil {
		return hash, false, nil
	}
	hashes := signingsOfCall(txs, hash)
	if len(hashes) <= 1 {
		return hash, false, nil
	}
	for _, h := range hashes {
		ok, err := hasReceipt(ctx, h)
		if err != nil {
			return hash, false, err
		}
		if ok {
			if strings.EqualFold(h, hash) {
				return hash, false, nil
			}
			return h, false, nil
		}
	}
	return hash, false, nil
}

// cancelProven re-reads the chain: a receipt of one of a's cancel signings
// at cancelDepth against the latest block, and none of its call's signings.
func (l *KeyLane) cancelProven(ctx context.Context, a *models.KeyLaneNonce, txs []models.KeyLaneTx) (bool, error) {
	if l.receipts == nil {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	head, err := l.receipts.BlockNumber(rctx)
	if err != nil {
		return false, err
	}
	for _, t := range sameCall(txs, a.Fingerprint) {
		if _, ok, err := l.receiptBlock(rctx, t.Hash); err != nil || ok {
			return false, err
		}
	}
	for _, t := range sameCall(txs, cancelFingerprint(l.Address())) {
		b, ok, err := l.receiptBlock(rctx, t.Hash)
		if err != nil {
			return false, err
		}
		if ok && l.deepEnough(head, b) {
			return true, nil
		}
	}
	return false, nil
}

// deepEnough: a receipt in block b is cancelDepth deep against head.
func (l *KeyLane) deepEnough(head, b uint64) bool {
	return head >= b && head-b+1 >= l.cancelDepth()
}

// receiptBlock is the lane's one non-waiting receipt read: (block, true)
// when mined. Shared by resolveCancels and cancelProven.
func (l *KeyLane) receiptBlock(ctx context.Context, hash string) (uint64, bool, error) {
	r, err := l.receipts.TransactionReceipt(ctx, common.HexToHash(hash))
	if errors.Is(err, ethereum.NotFound) || (err == nil && (r == nil || r.BlockNumber == nil)) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return r.BlockNumber.Uint64(), true, nil
}

// CallCancelled reports whether hash's call is final as cancelled
// (CallOutcome): the caller may then fail its operation and pay anew.
func (l *KeyLane) CallCancelled(ctx context.Context, hash string) (bool, error) {
	_, cancelled, err := l.CallOutcome(ctx, hash, nil)
	return cancelled, err
}

// hold holds the lane at nonce p — in this process at once, and in the
// lane's shared state (key_lane_states) so every process refuses too.
func (l *KeyLane) hold(address string, p uint64, reason string) {
	if l.held.Load() != nil {
		return
	}
	at := l.clock()
	h := &laneHold{nonce: p, since: at, reason: reason, clearAt: l.clearSeen.Load()}
	l.held.Store(h)
	h.persisted.Store(l.persistHold(address, h) == nil)
	log.Printf("[signer] ALERT key_lane_nonce_gap_held_total=1 chain=%d address=%s nonce=%d reason=%q — every send on this key and chain is refused until the mined nonce passes it or a super-admin acts (POST /api/admin/signer/lanes/fill-gap, /retry, /clear-hold)",
		l.chainID, address, p, reason)
}

// persistHold writes the hold to the shared state (logged when it fails:
// Watch re-persists it, never clears it for want of a row).
func (l *KeyLane) persistHold(address string, h *laneHold) error {
	ctx, cancel := context.WithTimeout(context.Background(), keyLaneReadTimeout)
	defer cancel()
	n := h.nonce
	err := l.store.WithLock(ctx, l.chainID, address, func(q KeyLaneTxn) error { return q.SetHold(&n, h.reason, h.since) })
	if err != nil {
		log.Printf("[signer] key lane: persist the hold at %d: %v (held in this process; re-persisted on watch)", n, err)
	}
	return err
}

// unholdLocked clears the shared hold inside a lane transaction — any hold,
// or only one at nonce when nonce is given.
func unholdLocked(q KeyLaneTxn, nonce *uint64) error {
	st, err := q.Hold()
	if err != nil || st == nil || st.HeldNonce == nil {
		return err
	}
	if nonce != nil && uint64(*st.HeldNonce) != *nonce {
		return nil
	}
	return q.SetHold(nil, "", time.Time{})
}

// adoptHold makes this process's view of the hold the shared one; a clear
// made anywhere (the clear version moved) also resets this process's gap
// timers. A local hold whose persist failed is not dropped for want of a row:
// it is persisted again.
func (l *KeyLane) adoptHold(st *models.KeyLaneState) {
	if st != nil {
		for {
			seen := l.clearSeen.Load()
			if st.ClearVersion <= seen {
				break
			}
			if l.clearSeen.CompareAndSwap(seen, st.ClearVersion) {
				l.gapMu.Lock()
				l.gapSeen = map[uint64]time.Time{}
				l.gapMu.Unlock()
				break
			}
		}
	}
	if st == nil || st.HeldNonce == nil {
		// A local hold whose persist failed is written again — unless a clear
		// happened anywhere since it was taken (the admin's clear wins).
		clearedSince := st != nil && st.ClearVersion > 0
		if h := l.held.Load(); h != nil && !h.persisted.Load() && l.signer != nil && !(clearedSince && st.ClearVersion > h.clearAt) {
			h.persisted.Store(l.persistHold(l.Address(), h) == nil)
			return
		}
		l.held.Store(nil)
		return
	}
	since := l.clock()
	if st.HeldSince != nil {
		since = *st.HeldSince
	}
	h := &laneHold{nonce: uint64(*st.HeldNonce), since: since, reason: st.HeldReason, clearAt: st.ClearVersion}
	h.persisted.Store(true)
	l.held.Store(h)
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
	var heldAt *uint64
	if h := l.held.Load(); h != nil {
		n := h.nonce
		heldAt = &n
		l.forgetGap(n)
	}
	err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		if st, err := q.Hold(); err != nil {
			return err
		} else if st != nil && st.HeldNonce != nil && heldAt == nil {
			n := uint64(*st.HeldNonce)
			heldAt = &n
		}
		var a *models.KeyLaneNonce
		if heldAt != nil {
			var err error
			if a, err = q.Get(*heldAt); err != nil {
				return err
			}
			if a != nil && (a.Cancel || a.Status == models.KeyLaneNonceCancelled) {
				return fmt.Errorf("%w (nonce %d): its cancel drives it — raise its price cap instead", ErrNonceCancelling, *heldAt)
			}
		}
		if err := unholdLocked(q, nil); err != nil {
			return err
		}
		if a == nil || a.Status != models.KeyLaneNonceSigned && a.Status != models.KeyLaneNonceSent {
			return nil
		}
		// The admin vouched for it: a fresh grace before it is judged again.
		a.ActedAt = l.clock()
		return q.Save(a)
	})
	if err != nil {
		return err
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
		if a != nil && (a.Cancel || a.Status == models.KeyLaneNonceCancelled) {
			return fmt.Errorf("%w (nonce %d)", ErrNonceCancelling, nonce)
		}
		if a == nil || a.Status == models.KeyLaneNonceMined {
			return fmt.Errorf("key lane: nonce %d has no live allocation", nonce)
		}
		a.BumpLimit, a.ActedAt = a.Bumps+keyLaneMaxBumps, time.Time{}
		if err := q.Save(a); err != nil {
			return err
		}
		return unholdLocked(q, &nonce)
	})
	if err != nil {
		return err
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	return nil
}

// ErrNonceCancelling: the nonce's call is being (or was) cancelled; a
// speed-up retry, price-cap raise or hold clear there is refused.
var ErrNonceCancelling = errors.New("key lane: this nonce's call is being cancelled or is cancelled")

// keyLaneMaxPriceCapMultiple bounds a super-admin's price-cap raise: never
// above this × the chain's default ceiling (a typo must not become a drain).
const keyLaneMaxPriceCapMultiple = 10

// RaisePriceCap is a super-admin's exit from a lane held at the speed-up fee
// cap: the allocation at nonce gets its own gas-price ceiling capWei (above
// every signing of it, never above keyLaneMaxPriceCapMultiple × the chain's
// ceiling), its bump limit is raised as RetrySpeedUp does, and the hold is
// cleared. The caller audits it. Returns the previous override ("" = none).
func (l *KeyLane) RaisePriceCap(ctx context.Context, nonce uint64, capWei *big.Int) (string, error) {
	if capWei == nil || capWei.Sign() <= 0 {
		return "", errors.New("key lane: the price cap must be positive")
	}
	if max := new(big.Int).Mul(keyLaneFeeCeiling(l.chainID), big.NewInt(keyLaneMaxPriceCapMultiple)); capWei.Cmp(max) > 0 {
		return "", fmt.Errorf("key lane: a price cap above %s wei is refused on chain %d", max, l.chainID)
	}
	var prev string
	err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		a, err := q.Get(nonce)
		if err != nil {
			return err
		}
		// A cancel-flagged nonce may have its CANCEL's cap raised (the exit
		// from a cancel held at the fee cap); a cancelled one is final.
		if a != nil && a.Status == models.KeyLaneNonceCancelled {
			return fmt.Errorf("%w (nonce %d)", ErrNonceCancelling, nonce)
		}
		if a == nil || a.Status == models.KeyLaneNonceMined {
			return fmt.Errorf("key lane: nonce %d has no live allocation", nonce)
		}
		txs, err := q.Signings(a.ID)
		if err != nil {
			return err
		}
		for _, t := range txs {
			if p, ok := new(big.Int).SetString(t.GasPrice, 10); ok && p.Cmp(capWei) >= 0 {
				return fmt.Errorf("key lane: nonce %d is already signed at %s wei; a cap must be above it", nonce, p)
			}
		}
		prev = a.PriceCap
		a.PriceCap, a.BumpLimit, a.ActedAt = capWei.String(), a.Bumps+keyLaneMaxBumps, time.Time{}
		if err := q.Save(a); err != nil {
			return err
		}
		return unholdLocked(q, &nonce)
	})
	if err != nil {
		return "", err
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	return prev, nil
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
	// A true hole lies BELOW the highest allocated nonce (our own calls wait
	// above it): a nonce at or above it is a typo that would take a nonce the
	// lane is about to use.
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	max, ok, err := l.store.MaxNonce(rctx, l.chainID, l.Address())
	cancel()
	if err != nil {
		return "", fmt.Errorf("key lane: read the lane's allocations: %w", err)
	}
	if !ok || nonce >= uint64(max) {
		return "", fmt.Errorf("key lane: nonce %d is not a hole below the highest allocated nonce", nonce)
	}
	hash, err := l.fillHole(ctx, nonce)
	if err != nil {
		return "", err
	}
	if err := l.store.WithLock(ctx, l.chainID, l.Address(), func(q KeyLaneTxn) error { return unholdLocked(q, &nonce) }); err != nil {
		log.Printf("[signer] key lane: clear the shared hold at %d: %v", nonce, err)
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	l.forgetGap(nonce)
	log.Printf("[signer] AUDIT key_lane_gap_filled=1 chain=%d address=%s nonce=%d tx=%s", l.chainID, l.Address(), nonce, hash)
	return hash, nil
}

// FinalHash follows a transaction this lane sent to the current signing of
// the SAME call (a speed-up), or returns hash unchanged.
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

// receiptReader is a chain client's non-waiting receipt read.
type receiptReader interface {
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
}

// receiptExists is hasReceipt over a receiptReader: ethereum.NotFound is
// (false, nil).
func receiptExists(p receiptReader) func(ctx context.Context, h string) (bool, error) {
	return func(ctx context.Context, h string) (bool, error) {
		r, err := p.TransactionReceipt(ctx, common.HexToHash(h))
		if errors.Is(err, ethereum.NotFound) {
			return false, nil
		}
		return err == nil && r != nil, err
	}
}

// followMinedHash moves a recorded hash column to the signing of the same
// call that mined (MinedSigning), only while the row still holds old; it
// returns the hash to verify with. A read failure keeps old (the next tick
// looks again).
func followMinedHash(ctx context.Context, lane *KeyLane, db *gorm.DB, model any, column string, id uuid.UUID, old string,
	hasReceipt func(ctx context.Context, h string) (bool, error)) (string, bool) {
	mined, cancelled, err := lane.CallOutcome(ctx, old, hasReceipt)
	if err != nil || cancelled || mined == old {
		return old, cancelled && err == nil
	}
	res := db.WithContext(ctx).Model(model).Where("id = ? AND "+column+" = ?", id, old).Update(column, mined)
	if res.Error != nil || res.RowsAffected != 1 {
		return old, false // changed under us: the next tick re-reads it
	}
	log.Printf("[signer] key lane: %s %s followed to the mined speed-up %s (was %s)", column, id, mined, old)
	return mined, false
}

// signingsOf is every signing (not refused) of hash's call — the original
// and its speed-ups, never another call — or [hash].
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
// allocations it has passed mined under the lane lock (raw bytes dropped),
// clear a hold the chain has passed, and review the allocation at the mined
// nonce (re-send, or speed up through the full fee gate) so an idle lane still
// moves. It returns an error while the lane is held or the mined nonce is
// unreadable (failure-streak ALERT).
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
	if err := l.store.WithLock(mctx, l.chainID, l.Address(), func(q KeyLaneTxn) error {
		if err := q.MarkMined(mined); err != nil {
			return err
		}
		// The chain passed a hold: clear it for every process.
		if st, err := q.Hold(); err != nil || st == nil || st.HeldNonce == nil || uint64(*st.HeldNonce) >= mined {
			return err
		}
		return q.SetHold(nil, "", time.Time{})
	}); err != nil {
		log.Printf("[signer] key lane: mark allocations below %d mined: %v", mined, err)
	}
	mcancel()
	l.resolveCancels(ctx, mined)
	l.gapMu.Lock()
	for n := range l.gapSeen {
		if n < mined {
			delete(l.gapSeen, n)
		}
	}
	l.gapMu.Unlock()
	// A hold the chain has passed is dropped first, so it is never re-persisted.
	if h := l.held.Load(); h != nil && mined > h.nonce {
		log.Printf("[signer] key lane hold cleared chain=%d address=%s nonce=%d (mined nonce %d)", l.chainID, l.Address(), h.nonce, mined)
		l.held.Store(nil)
	}
	// Every process sees one hold: adopt the shared state (another process's
	// hold, or an admin's clear made elsewhere).
	hctx, hcancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	if st, err := l.store.Hold(hctx, l.chainID, l.Address()); err == nil {
		l.adoptHold(st)
	}
	hcancel()
	if l.held.Load() == nil {
		l.reviewGap(ctx, l.signer, mined)
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
	mu       sync.Mutex
	finality uint64
	lanes    map[keyLaneID]*KeyLane
	roles    map[keyLaneID][]string
	store    KeyLaneStore
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

// SetFinalityDepth gives every lane, existing and future, the configured
// confirmation depth (RAMP_TREASURY_CONFIRMATIONS and peers): a cancel is
// final only at max(depth, keyLaneCancelDepth). Boot sets it.
func (r *SignerRegistry) SetFinalityDepth(depth uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finality = depth
	for _, l := range r.lanes {
		l.finality = depth
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
		lane.finality = r.finality
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
	l.receipts, _ = bc.(laneReceiptReader)
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
