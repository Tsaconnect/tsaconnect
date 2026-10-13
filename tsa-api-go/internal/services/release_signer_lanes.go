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

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// KeyLane is the ONE send lane of one hot key on one chain (signer
// consolidation 2026-10-13): the lock held from nonce read to broadcast, the
// nonce floor, the durable record of every transaction it signed, and the one
// fee gate every send on the key passes. Every role signing with that key on
// that chain (PM release, expiry cancel, IT operator, gas sponsor, treasury)
// sends through the same KeyLane. Get one from a SignerRegistry; the lint test
// forbids building one anywhere else.
//
// Nonce rule (owner): a nonce may only ever carry ONE call (to, data, value).
// Every signed transaction is written to key_lane_txs BEFORE its broadcast;
// a restarted process seeds its floor and records from the pending rows. When
// the chain's pending nonce sits below the floor at a transaction of ours the
// node does not have past keyLaneGapGrace, the lane re-broadcasts the exact
// stored bytes once, then re-signs the SAME call at a higher fee (a speed-up)
// — never a different call. A hole at a nonce the lane has no record of (a
// send from outside this process) holds the lane: every send refused, a
// failure-streak ALERT from the watch job, until the mined nonce passes it or
// a super-admin fills it (FillGap, a 0-value self-transfer) or clears it.
// Transient read failures never hold. The floor only rises.
type KeyLane struct {
	sem     chan struct{} // the lane lock, acquired ctx-aware
	signer  ReleaseSigner
	chainID int64 // what the lane's broadcaster reports; 0 = unchecked (a solo lane)

	nonceFloor uint64               // the next nonce this lane may sign
	sent       map[uint64]*laneSent // the current tx at each unmined nonce this lane signed
	gapSeen    map[uint64]time.Time // when a hole with no record was first seen
	seeded     bool                 // records loaded from the store
	held       atomic.Pointer[laneHold]

	balances laneBalanceReader // nil: no fee gate (a solo test lane)
	lookup   laneTxLookup      // nil: an unseen tx is treated as unknown to the node
	nonces   laneNonceReader   // nil: records are never pruned, holds never clear by themselves
	pricer   laneGasPricer     // nil: no speed-up (a stuck tx then holds the lane)
	store    KeyLaneStore      // nil: records live only in memory (tests)
	roles    string

	reserveMu sync.Mutex
	reserves  map[string]LaneReservation

	now func() time.Time
}

// laneSent is the transaction at one nonce: the call it carries (never
// changed), the bytes and hash of its current signing, and its store row.
type laneSent struct {
	call          blockchain.UnsignedTx
	hash          string
	raw           []byte
	rowID         uuid.UUID
	at            time.Time
	rebroadcastAt time.Time
	rebroadcasts  int
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

// rawSigner is a signer that can sign without broadcasting, so the lane keeps
// the bytes for an identical re-broadcast (*LocalKeyReleaseSigner).
type rawSigner interface {
	signRaw(unsigned []byte) (raw []byte, hash string, err error)
	broadcastRaw(raw []byte, hash string) (string, error)
}

// ErrKeyLaneHeld: the lane found a hole in its nonce sequence it cannot fill
// with its own call, and refuses every send until the mined nonce passes it or
// a super-admin fills or clears it.
var ErrKeyLaneHeld = errors.New("the signing key's lane is held at a nonce gap it cannot fill")

const (
	// keyLaneGapGrace is how long a hole may persist before the lane acts on
	// it, and the spacing between re-sends of one stuck nonce.
	keyLaneGapGrace = 15 * time.Second
	// keyLaneReadTimeout bounds every read the lane makes.
	keyLaneReadTimeout = 10 * time.Second
	// keyLaneMaxUnmined bounds the records: past it (the mined nonce has not
	// been readable for a long time) the lane refuses sends rather than grow.
	keyLaneMaxUnmined = 1024
	// keyLaneSpeedUpBumpPermille is a speed-up's minimum price bump: geth's
	// replacement rule is +10%.
	keyLaneSpeedUpBumpPermille = 1125
)

func newKeyLane(signer ReleaseSigner, chainID int64) *KeyLane {
	return &KeyLane{sem: make(chan struct{}, 1), signer: signer, chainID: chainID,
		sent: map[uint64]*laneSent{}, gapSeen: map[uint64]time.Time{}}
}

// soloLane is a lane outside any registry, for a signer handed in directly in
// a test. balances nil = no fee gate. Only this file may build a lane (lint:
// TestKeyLanes_OnlyFromTheRegistry); production code takes registry lanes.
func soloLane(signer ReleaseSigner, balances laneBalanceReader) *KeyLane {
	l := newKeyLane(signer, 0)
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

// lock acquires the lane, or gives up when ctx ends first.
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

// Send builds (prepare + nonce read), pins the nonce to the lane's floor,
// checks the key can pay, records, signs and broadcasts, all under the lane's
// lock. attempted separates a build failure or a definite refusal
// (ErrTxNotBroadcast: nothing left this process) from a broadcast whose
// outcome is unknown, which callers must treat as possibly sent. A build error
// is returned as build returned it.
func (l *KeyLane) Send(ctx context.Context, build func() ([]byte, error)) (txHash string, attempted bool, err error) {
	return l.sendWith(ctx, l.signer, build, nil)
}

// SendRecorded is Send with a hook that receives the nonce about to be signed
// BEFORE the broadcast (review C1), so a broadcast whose hash is lost can still
// be told apart from one that never happened. A hook error broadcasts nothing
// and hands the nonce back.
func (l *KeyLane) SendRecorded(ctx context.Context, build func() ([]byte, error), onPrepared func(nonce uint64) error) (string, bool, error) {
	return l.sendWith(ctx, l.signer, build, onPrepared)
}

// feeInputs is what the fee gate read before taking the lock.
type feeInputs struct {
	balance       *big.Int
	reservedUnits uint64
}

// readFeeInputs reads the balance and the reservation with the caller's ctx
// (bounded), BEFORE the lane lock: a hung read holds up only its own send.
// nil on a lane without a fee gate.
func (l *KeyLane) readFeeInputs(ctx context.Context, address string) (*feeInputs, error) {
	if l.balances == nil {
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
	bal, err := l.balances.NativeBalance(rctx, address)
	if err != nil {
		return nil, notBroadcast(fmt.Errorf("key lane: read the balance: %w", err))
	}
	in.balance = bal
	return in, nil
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
	fee, err := l.readFeeInputs(ctx, signer.Address())
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
	if err := l.seedLocked(ctx); err != nil {
		return "", false, err
	}
	if len(l.sent) >= keyLaneMaxUnmined {
		log.Printf("[signer] ALERT key_lane_backlog_total=1 chain=%d address=%s unmined=%d — the mined nonce has not been readable; sends refused until it is",
			l.chainID, l.logAddress(), len(l.sent))
		return "", false, notBroadcast(fmt.Errorf("key lane: %d unmined transactions; refusing to add more", len(l.sent)))
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
	prepared := u.Nonce
	if prepared < l.nonceFloor {
		l.checkGapLocked(ctx, prepared)
		if err := l.heldErr(); err != nil {
			return "", false, err
		}
		u.Nonce = l.nonceFloor // never a second call at a nonce this lane used
	}
	l.nonceFloor = u.Nonce + 1
	if u.Nonce != prepared {
		if unsigned, err = json.Marshal(u); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, notBroadcast(fmt.Errorf("key lane: re-encode the pinned tx: %w", err))
		}
	}
	if err := l.requireFee(signer.Address(), fee, l.committedLocked(), &u); err != nil {
		l.stepBackLocked(u.Nonce)
		return "", false, err
	}
	if onPrepared != nil {
		if err := onPrepared(u.Nonce); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, fmt.Errorf("key lane: record the nonce before broadcast: %w", err)
		}
	}
	return l.signAndSendLocked(ctx, signer, u, unsigned)
}

// seedLocked loads this lane's pending rows once (a restart): the floor and
// the call at every nonce it used. A store read failure refuses the send
// (transient — retried on the next one); signing without the records could
// put a second call at a used nonce.
func (l *KeyLane) seedLocked(ctx context.Context) error {
	if l.seeded {
		return nil
	}
	if l.store == nil {
		l.seeded = true
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	rows, err := l.store.Pending(rctx, l.chainID, l.logAddress())
	if err != nil {
		return notBroadcast(fmt.Errorf("key lane: load the lane's records: %w", err))
	}
	for i := range rows {
		r := rows[i]
		n := uint64(r.Nonce)
		l.sent[n] = &laneSent{
			call: blockchain.UnsignedTx{Nonce: n, GasPrice: r.GasPrice, GasLimit: uint64(r.GasLimit), To: r.To,
				Value: r.Value, Data: r.Data, ChainID: strconv.FormatInt(l.chainID, 10)},
			hash: r.Hash, raw: r.RawTx, rowID: r.ID, at: r.CreatedAt,
		}
		if n+1 > l.nonceFloor {
			l.nonceFloor = n + 1
		}
	}
	l.seeded = true
	if len(rows) > 0 {
		log.Printf("[signer] key lane seeded chain=%d address=%s unmined=%d floor=%d", l.chainID, l.logAddress(), len(rows), l.nonceFloor)
	}
	return nil
}

// committedLocked is what this lane's broadcast-but-unmined transactions will
// still spend (value + gas limit × price each): the balance a chain read shows
// has not paid them yet.
func (l *KeyLane) committedLocked() *big.Int {
	sum := new(big.Int)
	for _, rec := range l.sent {
		price, _ := new(big.Int).SetString(rec.call.GasPrice, 10)
		if price != nil {
			sum.Add(sum, new(big.Int).Mul(price, new(big.Int).SetUint64(rec.call.GasLimit)))
		}
		if v, ok := new(big.Int).SetString(rec.call.Value, 10); ok {
			sum.Add(sum, v)
		}
	}
	return sum
}

// signAndSendLocked signs, records (store first, then memory) and broadcasts
// one transaction. A signer that can sign without sending hands the lane its
// bytes first, so a later gap is refilled with exactly these bytes.
func (l *KeyLane) signAndSendLocked(ctx context.Context, signer ReleaseSigner, u blockchain.UnsignedTx, unsigned []byte) (string, bool, error) {
	nonce := u.Nonce
	rec := &laneSent{call: u, at: l.clock()}
	rs, isRaw := signer.(rawSigner)
	if isRaw {
		raw, hash, err := rs.signRaw(unsigned)
		if err != nil {
			l.stepBackLocked(nonce)
			return "", false, err
		}
		rec.raw, rec.hash = raw, hash
		if err := l.recordLocked(ctx, rec, models.KeyLaneTxPending); err != nil {
			l.stepBackLocked(nonce)
			return "", false, err
		}
	}
	l.sent[nonce] = rec
	var txHash string
	var err error
	if isRaw {
		txHash, err = rs.broadcastRaw(rec.raw, rec.hash)
	} else {
		txHash, err = signer.SignAndBroadcast(ctx, unsigned)
		rec.hash = txHash
		if err != nil {
			rec.hash = uncertainTxHash(err)
		}
	}
	if errors.Is(err, ErrTxNotBroadcast) {
		l.forgetRowLocked(ctx, rec)
		l.stepBackLocked(nonce)
		return "", false, err
	}
	return txHash, true, err
}

// recordLocked writes rec to the store before it is broadcast. No store: a
// no-op (tests).
func (l *KeyLane) recordLocked(ctx context.Context, rec *laneSent, status string) error {
	if l.store == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyLaneReadTimeout)
	defer cancel()
	row := &models.KeyLaneTx{ID: uuid.New(), ChainID: l.chainID, Address: strings.ToLower(l.logAddress()),
		Nonce: int64(rec.call.Nonce), Hash: rec.hash, RawTx: rec.raw, To: rec.call.To, Data: rec.call.Data,
		Value: rec.call.Value, GasLimit: int64(rec.call.GasLimit), GasPrice: rec.call.GasPrice, Status: status, Role: l.roles}
	if err := l.store.Record(rctx, row); err != nil {
		return notBroadcast(fmt.Errorf("key lane: record the transaction before broadcast: %w", err))
	}
	rec.rowID = row.ID
	return nil
}

// forgetRowLocked deletes the row of a transaction the node definitely
// refused (it never left: its nonce is free again).
func (l *KeyLane) forgetRowLocked(ctx context.Context, rec *laneSent) {
	if l.store == nil || rec.rowID == uuid.Nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyLaneReadTimeout)
	defer cancel()
	if err := l.store.Delete(rctx, rec.rowID); err != nil {
		log.Printf("[signer] key lane: drop the row of a refused tx %s: %v", rec.hash, err)
	}
}

// requireFee is the lane's one balance gate (floors only alert, owner
// 2026-10-13): the balance, less what this lane's unmined transactions will
// still spend, must cover this transaction's gas limit × gas price ×
// gasFeeBuffer, plus its value, plus the lane's reservation priced at the same
// gas price (unless this is an owed send). Fails closed when the transaction
// cannot be priced. A lane without a fee gate (in == nil) is not gated.
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
	if committed != nil {
		available.Sub(available, committed)
	}
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
// unless ctx is a ReservedSend. A solo lane without a balance reader always can.
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
// It does not count unmined transactions (no lock); the send does.
func (l *KeyLane) canPayAs(ctx context.Context, address string, units uint64, price, value *big.Int) (*big.Int, error) {
	if l == nil || l.balances == nil {
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
	return in.balance, l.requireFee(address, in, nil, &blockchain.UnsignedTx{GasLimit: units, GasPrice: price.String(), Value: value.String()})
}

// checkGapLocked looks at the chain's pending nonce p, below the floor: a
// lagging view, our own transaction the node lost, or a hole we never sent.
// Never puts a different call at p. Transient read failures do nothing (the
// next send or watch tick retries). Caller holds the lane.
func (l *KeyLane) checkGapLocked(ctx context.Context, p uint64) {
	now := l.clock()
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	rec := l.sent[p]
	if rec == nil {
		if !l.hasRecordAbove(p) {
			return // nothing of ours waits behind p: a lagging view
		}
		if l.nonces == nil {
			return
		}
		mined, err := l.nonces.LatestNonce(rctx, l.logAddress())
		if err != nil {
			return
		}
		if mined > p {
			l.dropMinedLocked(ctx, mined)
			return
		}
		first, ok := l.gapSeen[p]
		if !ok {
			l.gapSeen[p] = now
			return
		}
		if now.Sub(first) < keyLaneGapGrace {
			return
		}
		l.holdLocked(p, "no record of a transaction at this nonce — something outside this process used the key")
		return
	}
	if now.Sub(rec.at) < keyLaneGapGrace || now.Sub(rec.rebroadcastAt) < keyLaneGapGrace {
		return
	}
	if l.nonces != nil {
		if mined, err := l.nonces.LatestNonce(rctx, l.logAddress()); err == nil && mined > p {
			l.dropMinedLocked(ctx, mined)
			return
		}
	}
	if l.lookup != nil && rec.hash != "" {
		known, err := l.lookup.TransactionKnown(rctx, rec.hash)
		if err != nil {
			log.Printf("[signer] key lane: nonce %d tx %s unverified (%s); not re-sent, retried later",
				p, rec.hash, RedactAlertText(err.Error()))
			return
		}
		if known {
			return
		}
	}
	rs, isRaw := l.signer.(rawSigner)
	if rec.raw != nil && isRaw && rec.rebroadcasts == 0 {
		rec.rebroadcasts++
		rec.rebroadcastAt = now
		_, err := rs.broadcastRaw(rec.raw, rec.hash)
		log.Printf("[signer] ALERT key_lane_nonce_gap_rebroadcast_total=1 chain=%d address=%s nonce=%d tx=%s err=%v",
			l.chainID, l.logAddress(), p, rec.hash, err)
		if !errors.Is(err, ErrTxNotBroadcast) {
			return // accepted, or uncertain: look again after the grace
		}
		// Definitely refused (underpriced, below the base fee): speed it up now.
	}
	l.speedUpLocked(ctx, p, rec)
}

// speedUpLocked re-signs the SAME call at p at a higher fee: at least the
// replacement bump over its last price, and at least today's price. Without a
// raw signer or a price it holds the lane rather than guess.
func (l *KeyLane) speedUpLocked(ctx context.Context, p uint64, rec *laneSent) {
	rs, isRaw := l.signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		l.holdLocked(p, "the transaction at this nonce is gone and cannot be re-signed here")
		return
	}
	old, ok := new(big.Int).SetString(rec.call.GasPrice, 10)
	if !ok {
		l.holdLocked(p, "the transaction at this nonce has no readable gas price")
		return
	}
	bumped := new(big.Int).Div(new(big.Int).Mul(old, big.NewInt(keyLaneSpeedUpBumpPermille)), big.NewInt(1000))
	bumped.Add(bumped, big.NewInt(1))
	if live, err := l.pricer.SuggestGasPrice(); err == nil && live.Cmp(bumped) > 0 {
		bumped = live
	}
	call := rec.call // to, data, value, gas limit, chain: unchanged
	call.Nonce, call.GasPrice = p, bumped.String()
	unsigned, err := json.Marshal(call)
	if err != nil {
		return
	}
	raw, hash, err := rs.signRaw(unsigned)
	if err != nil {
		return
	}
	next := &laneSent{call: call, raw: raw, hash: hash, at: rec.at, rebroadcastAt: l.clock(), rebroadcasts: rec.rebroadcasts + 1}
	if err := l.recordLocked(ctx, next, models.KeyLaneTxPending); err != nil {
		log.Printf("[signer] key lane: record the speed-up at nonce %d: %v", p, err)
		return // never broadcast an unrecorded signing
	}
	if l.store != nil && rec.rowID != uuid.Nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyLaneReadTimeout)
		_ = l.store.SetStatus(rctx, rec.rowID, models.KeyLaneTxReplaced)
		cancel()
	}
	l.sent[p] = next
	_, berr := rs.broadcastRaw(raw, hash)
	log.Printf("[signer] ALERT key_lane_speed_up_total=1 chain=%d address=%s nonce=%d old_tx=%s new_tx=%s gas_price=%s err=%v",
		l.chainID, l.logAddress(), p, rec.hash, hash, bumped, berr)
}

func (l *KeyLane) hasRecordAbove(p uint64) bool {
	for n := range l.sent {
		if n > p {
			return true
		}
	}
	return false
}

// holdLocked holds the lane at nonce p.
func (l *KeyLane) holdLocked(p uint64, reason string) {
	if l.held.Load() != nil {
		return
	}
	l.held.Store(&laneHold{nonce: p, since: l.clock(), reason: reason})
	log.Printf("[signer] ALERT key_lane_nonce_gap_held_total=1 chain=%d address=%s nonce=%d reason=%q — every send on this key and chain is refused until the mined nonce passes it or a super-admin fills or clears it (POST /api/admin/signer/lanes/fill-gap, /clear-hold)",
		l.chainID, l.logAddress(), p, reason)
}

// Held is the lane's hold, if any: (nonce, reason, true).
func (l *KeyLane) Held() (uint64, string, bool) {
	h := l.held.Load()
	if h == nil {
		return 0, "", false
	}
	return h.nonce, h.reason, true
}

// ClearHold is a super-admin's exit from a held lane after manual inspection
// (the gap was filled by hand, or proven filled): the floor and records stay.
func (l *KeyLane) ClearHold(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	if h := l.held.Load(); h != nil {
		delete(l.gapSeen, h.nonce)
		if rec := l.sent[h.nonce]; rec != nil {
			rec.at = l.clock() // the admin vouched for it: a fresh grace before it is judged again
		}
	}
	l.held.Store(nil)
	return nil
}

// FillGap is a super-admin's fix for a hole at nonce no record of ours
// covers: a 0-value self-transfer at that nonce, recorded (status filled)
// before its broadcast. Refused for a nonce this lane holds a call at, or one
// the chain has already mined.
func (l *KeyLane) FillGap(ctx context.Context, nonce uint64) (string, error) {
	if err := l.lock(ctx); err != nil {
		return "", err
	}
	defer l.unlock()
	if err := l.seedLocked(ctx); err != nil {
		return "", err
	}
	if _, ours := l.sent[nonce]; ours {
		return "", fmt.Errorf("key lane: nonce %d carries this lane's own call; it is re-sent, never filled", nonce)
	}
	if nonce >= l.nonceFloor {
		return "", fmt.Errorf("key lane: nonce %d is not below the lane's floor %d", nonce, l.nonceFloor)
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	if l.nonces != nil {
		mined, err := l.nonces.LatestNonce(rctx, l.logAddress())
		if err != nil {
			return "", fmt.Errorf("key lane: read the mined nonce: %w", err)
		}
		if mined > nonce {
			return "", fmt.Errorf("key lane: nonce %d is already mined", nonce)
		}
	}
	rs, isRaw := l.signer.(rawSigner)
	if !isRaw || l.pricer == nil {
		return "", errors.New("key lane: this lane cannot sign a gap fill")
	}
	price, err := l.pricer.SuggestGasPrice()
	if err != nil {
		return "", fmt.Errorf("key lane: gas price: %w", err)
	}
	price = new(big.Int).Div(new(big.Int).Mul(price, big.NewInt(keyLaneSpeedUpBumpPermille)), big.NewInt(1000))
	self := l.logAddress()
	call := blockchain.UnsignedTx{Nonce: nonce, GasPrice: price.String(), GasLimit: 21_000, To: self, Value: "0",
		ChainID: strconv.FormatInt(l.chainID, 10)}
	unsigned, _ := json.Marshal(call)
	raw, hash, err := rs.signRaw(unsigned)
	if err != nil {
		return "", err
	}
	rec := &laneSent{call: call, raw: raw, hash: hash, at: l.clock()}
	if err := l.recordLocked(ctx, rec, models.KeyLaneTxFilled); err != nil {
		return "", err
	}
	l.sent[nonce] = rec
	if _, err := rs.broadcastRaw(raw, hash); err != nil && errors.Is(err, ErrTxNotBroadcast) {
		l.forgetRowLocked(ctx, rec)
		delete(l.sent, nonce)
		return "", err
	}
	if h := l.held.Load(); h != nil && h.nonce == nonce {
		l.held.Store(nil)
	}
	delete(l.gapSeen, nonce)
	log.Printf("[signer] AUDIT key_lane_gap_filled=1 chain=%d address=%s nonce=%d tx=%s", l.chainID, self, nonce, hash)
	return hash, nil
}

// Watch is the lane's periodic upkeep, outside the send path: read the mined
// nonce, mark the rows it has passed mined (raw bytes dropped), forget their
// records, and clear a hold the chain has passed. It returns an error while
// the lane is held or the mined nonce is unreadable, so the scheduler's
// failure-streak ALERT fires while either lasts.
func (l *KeyLane) Watch(ctx context.Context) error {
	if l.nonces == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	mined, err := l.nonces.LatestNonce(rctx, l.logAddress())
	cancel()
	if err != nil {
		return fmt.Errorf("key lane %d/%s: read the mined nonce: %w", l.chainID, l.logAddress(), err)
	}
	if err := l.lock(ctx); err != nil {
		return err
	}
	l.dropMinedLocked(ctx, mined)
	if h := l.held.Load(); h != nil && mined > h.nonce {
		log.Printf("[signer] key lane hold cleared chain=%d address=%s nonce=%d (mined nonce %d)", l.chainID, l.logAddress(), h.nonce, mined)
		l.held.Store(nil)
	}
	l.unlock()
	if err := l.heldErr(); err != nil {
		return err
	}
	return nil
}

// dropMinedLocked forgets records (and marks rows mined) the chain's mined
// nonce has passed.
func (l *KeyLane) dropMinedLocked(ctx context.Context, mined uint64) {
	for n := range l.sent {
		if n < mined {
			delete(l.sent, n)
		}
	}
	for n := range l.gapSeen {
		if n < mined {
			delete(l.gapSeen, n)
		}
	}
	if l.store != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyLaneReadTimeout)
		if err := l.store.MarkMined(rctx, l.chainID, l.logAddress(), mined); err != nil {
			log.Printf("[signer] key lane: mark rows below %d mined: %v", mined, err)
		}
		cancel()
	}
}

// stepBackLocked hands back the one nonce a send provably never used — the
// send that just took it — and only while the floor still sits just past it
// (re-review N3). Caller holds the lane.
func (l *KeyLane) stepBackLocked(n uint64) {
	delete(l.sent, n)
	if l.nonceFloor == n+1 {
		l.nonceFloor = n
	}
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
	Floor      uint64 `json:"floor"`
	Unmined    int    `json:"unmined"`
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
		if l.lock(ctx) == nil {
			st.Floor, st.Unmined = l.nonceFloor, len(l.sent)
			l.unlock()
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

func NewSignerRegistry() *SignerRegistry {
	return &SignerRegistry{lanes: map[keyLaneID]*KeyLane{}, roles: map[keyLaneID][]string{}}
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
		return newBroadcasterLane(key, bc, chainID)
	}
	id := keyLaneID{address: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), chainID: chainID}
	r.mu.Lock()
	defer r.mu.Unlock()
	lane := r.lanes[id]
	if lane == nil {
		lane = newBroadcasterLane(key, bc, chainID)
		lane.store = r.store
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
func newBroadcasterLane(key *ecdsa.PrivateKey, bc ReleaseBroadcaster, chainID int64) *KeyLane {
	l := newKeyLane(NewLocalKeyReleaseSigner(key, bc), chainID)
	l.balances, _ = bc.(laneBalanceReader)
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
