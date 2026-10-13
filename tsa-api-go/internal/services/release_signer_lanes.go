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
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// KeyLane is the ONE send lane of one hot key on one chain (signer
// consolidation 2026-10-13): the lock held from nonce read to broadcast, the
// nonce floor that covers an RPC whose pending view lags our own sends, and the
// one fee gate every send on the key passes. Every role signing with that key
// on that chain (PM release, expiry cancel, IT operator, gas sponsor, treasury)
// sends through the same KeyLane, so two roles on one key can never read the
// same pending nonce or spend the same balance twice. Get one from a
// SignerRegistry; the lint test forbids building one anywhere else.
//
// Nonce rule (owner): a nonce this lane signed is never signed again with
// different bytes. The floor only rises (it steps back only for the send that
// just took a nonce and was definitely refused). When the chain's pending
// nonce sits below the floor at a transaction this lane sent more than
// keyLaneGapGrace ago and the node does not have it, the lane RE-BROADCASTS
// the exact signed bytes it kept; if it has no bytes (a signer that cannot
// hand them over) or cannot tell, it holds the lane and alerts rather than
// guess. Records are dropped only once the chain's latest (mined) nonce has
// passed them.
type KeyLane struct {
	mu      sync.Mutex
	signer  ReleaseSigner
	chainID int64 // what the lane's broadcaster reports; 0 = unchecked (a solo lane)

	nonceFloor uint64               // the next nonce this lane may sign
	sent       map[uint64]*laneSent // every nonce signed and not yet passed by the latest mined nonce
	held       *laneHold            // a gap the lane cannot refill: every send refused until it clears

	balances laneBalanceReader // nil: no fee gate (a solo test lane)
	lookup   laneTxLookup      // nil: a suspected gap is re-broadcast without asking
	nonces   laneNonceReader   // nil: records are never pruned, holds never clear by themselves

	reserveMu sync.Mutex
	reserves  map[string]LaneReservation

	now func() time.Time
}

// laneSent is one transaction this lane signed: its hash and, when the signer
// can hand them over, the exact signed bytes (kept in memory for the process;
// the onPrepared hook records only the nonce, so after a restart a gap at an
// old nonce holds the lane instead of being refilled).
type laneSent struct {
	hash          string
	raw           []byte
	at            time.Time
	rebroadcastAt time.Time
}

type laneHold struct {
	nonce uint64
	since time.Time
}

// laneBalanceReader is the lane broadcaster's native balance read
// (*blockchain.EVMClient), bounded by the caller's ctx.
type laneBalanceReader interface {
	NativeBalance(ctx context.Context, address string) (*big.Int, error)
}

// laneTxLookup asks the node whether it holds a transaction, pending or mined.
type laneTxLookup interface {
	TransactionKnown(ctx context.Context, txHash string) (bool, error)
}

// laneNonceReader is the chain's latest MINED nonce for an address.
type laneNonceReader interface {
	LatestNonce(ctx context.Context, address string) (uint64, error)
}

// rawSigner is a signer that can sign without broadcasting, so the lane keeps
// the bytes for an identical re-broadcast (*LocalKeyReleaseSigner).
type rawSigner interface {
	signRaw(unsigned []byte) (raw []byte, hash string, err error)
	broadcastRaw(raw []byte, hash string) (string, error)
}

// ErrKeyLaneHeld: the lane found a hole in its nonce sequence it cannot refill
// (no stored bytes for the missing transaction) and refuses every send until
// the chain's mined nonce passes it or an operator clears it (ClearHold).
var ErrKeyLaneHeld = errors.New("the signing key's lane is held at a nonce gap it cannot refill")

const (
	// keyLaneGapGrace is how long a sent transaction may stay unseen in the
	// chain's pending view before the lane checks it, and how often it may be
	// re-broadcast.
	keyLaneGapGrace = 15 * time.Second
	// keyLaneReadTimeout bounds the fee gate's balance and reservation reads.
	keyLaneReadTimeout = 10 * time.Second
	// keyLanePruneAt: past this many records the lane reads the mined nonce to
	// drop the ones the chain has passed.
	keyLanePruneAt = 256
)

// soloLane is a lane outside any registry, for a signer handed in directly in
// a test. balances nil = no fee gate. Only this file may build a lane (lint:
// TestKeyLanes_OnlyFromTheRegistry); production code takes registry lanes.
func soloLane(signer ReleaseSigner, balances laneBalanceReader) *KeyLane {
	return &KeyLane{signer: signer, balances: balances}
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
// checks the key can pay, signs and broadcasts, all under the lane's lock.
// attempted separates a build failure or a definite refusal (ErrTxNotBroadcast:
// nothing left this process) from a broadcast whose outcome is unknown, which
// callers must treat as possibly sent. A build error is returned as build
// returned it.
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

// sendWith is Send signing with signer. Only a SponsorLane built without a
// registry lane passes a signer other than the lane's own (tests swap
// SponsorLane.Signer); every registry lane signs with its own.
func (l *KeyLane) sendWith(ctx context.Context, signer ReleaseSigner, build func() ([]byte, error), onPrepared func(uint64) error) (string, bool, error) {
	fee, err := l.readFeeInputs(ctx, signer.Address())
	if err != nil {
		return "", false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
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
	if err := l.checkHoldLocked(ctx); err != nil {
		return "", false, err
	}
	prepared := u.Nonce
	if prepared < l.nonceFloor {
		l.checkGapLocked(ctx, prepared)
		if err := l.checkHoldLocked(ctx); err != nil {
			return "", false, err
		}
		u.Nonce = l.nonceFloor // never sign a nonce this lane already used
	}
	l.nonceFloor = u.Nonce + 1
	if u.Nonce != prepared {
		if unsigned, err = json.Marshal(u); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, notBroadcast(fmt.Errorf("key lane: re-encode the pinned tx: %w", err))
		}
	}
	if err := l.requireFee(ctx, signer.Address(), fee, &u); err != nil {
		l.stepBackLocked(u.Nonce)
		return "", false, err
	}
	if onPrepared != nil {
		if err := onPrepared(u.Nonce); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, fmt.Errorf("key lane: record the nonce before broadcast: %w", err)
		}
	}
	return l.signAndSendLocked(ctx, signer, u.Nonce, unsigned)
}

// signAndSendLocked signs, records and broadcasts one transaction. A signer
// that can sign without sending hands the lane its bytes first, so a later
// gap is refilled with exactly these bytes.
func (l *KeyLane) signAndSendLocked(ctx context.Context, signer ReleaseSigner, nonce uint64, unsigned []byte) (string, bool, error) {
	if l.sent == nil {
		l.sent = map[uint64]*laneSent{}
	}
	rec := &laneSent{at: l.clock()}
	var txHash string
	var err error
	if rs, ok := signer.(rawSigner); ok {
		raw, hash, serr := rs.signRaw(unsigned)
		if serr != nil {
			l.stepBackLocked(nonce)
			return "", false, serr
		}
		rec.raw, rec.hash = raw, hash
		l.sent[nonce] = rec
		txHash, err = rs.broadcastRaw(raw, hash)
	} else {
		l.sent[nonce] = rec
		txHash, err = signer.SignAndBroadcast(ctx, unsigned)
		rec.hash = txHash
		if err != nil {
			rec.hash = uncertainTxHash(err)
		}
	}
	if errors.Is(err, ErrTxNotBroadcast) {
		l.stepBackLocked(nonce)
		return "", false, err
	}
	l.pruneLocked()
	return txHash, true, err
}

// requireFee is the lane's one balance gate (floors only alert, owner
// 2026-10-13): the key must hold this transaction's gas limit × gas price ×
// gasFeeBuffer, plus its value, plus the lane's reservation priced at the same
// gas price (unless this is an owed send). The balance and reservation were
// read before the lock (in). Fails closed when the transaction cannot be
// priced. A lane without a fee gate (in == nil) is not gated.
func (l *KeyLane) requireFee(ctx context.Context, address string, in *feeInputs, u *blockchain.UnsignedTx) error {
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
	if in.balance.Cmp(need) < 0 {
		log.Printf("[signer] ALERT key_lane_cannot_pay_total=1 chain=%d address=%s balance=%s needed=%s reserved=%s",
			l.chainID, address, in.balance, need, reserved)
		return notBroadcast(fmt.Errorf("%w: balance %s wei, this transaction needs %s (%s of it reserved)", ErrRelayerUnderfunded, in.balance, need, reserved))
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
	return in.balance, l.requireFee(ctx, address, in, &blockchain.UnsignedTx{GasLimit: units, GasPrice: price.String(), Value: value.String()})
}

// checkGapLocked looks at the chain's pending nonce p, below the floor: a
// lagging view, or a transaction of ours the node lost. Never reuses p.
// Caller holds mu.
func (l *KeyLane) checkGapLocked(ctx context.Context, p uint64) {
	rec := l.sent[p]
	if rec == nil && !l.sentWithin(p) {
		return // we never sent p in this process: the view lags behind sends we did
	}
	now := l.clock()
	if rec != nil && (now.Sub(rec.at) < keyLaneGapGrace || now.Sub(rec.rebroadcastAt) < keyLaneGapGrace) {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
	defer cancel()
	if l.nonces != nil {
		if mined, err := l.nonces.LatestNonce(rctx, l.logAddress()); err == nil {
			l.dropMinedLocked(mined)
			if mined > p {
				return // p is mined: the pending view lags
			}
		}
	}
	if rec != nil && l.lookup != nil && rec.hash != "" {
		known, err := l.lookup.TransactionKnown(rctx, rec.hash)
		if err != nil {
			log.Printf("[signer] ALERT key_lane_gap_unverified_total=1 chain=%d address=%s nonce=%d err=%s — not reused, re-checked on the next send",
				l.chainID, l.logAddress(), p, RedactAlertText(err.Error()))
			return
		}
		if known {
			return
		}
	}
	rs, ok := l.signer.(rawSigner)
	if rec == nil || rec.raw == nil || !ok {
		l.held = &laneHold{nonce: p, since: now}
		log.Printf("[signer] ALERT key_lane_nonce_gap_held_total=1 chain=%d address=%s nonce=%d — the transaction at this nonce is gone and its bytes are not kept; every send on this key and chain is refused until the chain's mined nonce passes it or an operator clears the hold",
			l.chainID, l.logAddress(), p)
		return
	}
	rec.rebroadcastAt = now
	_, err := rs.broadcastRaw(rec.raw, rec.hash)
	log.Printf("[signer] ALERT key_lane_nonce_gap_rebroadcast_total=1 chain=%d address=%s nonce=%d tx=%s err=%v",
		l.chainID, l.logAddress(), p, rec.hash, err)
}

// sentWithin reports whether n lies inside the range of nonces this lane has
// records for.
func (l *KeyLane) sentWithin(n uint64) bool {
	for k := range l.sent {
		if k <= n {
			return true
		}
	}
	return false
}

// checkHoldLocked refuses while the lane is held, clearing the hold once the
// chain's mined nonce has passed the gap. Caller holds mu.
func (l *KeyLane) checkHoldLocked(ctx context.Context) error {
	if l.held == nil {
		return nil
	}
	if l.nonces != nil {
		rctx, cancel := context.WithTimeout(ctx, keyLaneReadTimeout)
		mined, err := l.nonces.LatestNonce(rctx, l.logAddress())
		cancel()
		if err == nil && mined > l.held.nonce {
			log.Printf("[signer] key lane hold cleared chain=%d address=%s nonce=%d (mined nonce %d)", l.chainID, l.logAddress(), l.held.nonce, mined)
			l.dropMinedLocked(mined)
			l.held = nil
			return nil
		}
	}
	return notBroadcast(fmt.Errorf("%w (nonce %d, since %s)", ErrKeyLaneHeld, l.held.nonce, l.held.since.Format(time.RFC3339)))
}

// ClearHold is the operator's exit from a held lane, after the gap was filled
// by hand (or proven filled): the floor and records are left as they are.
func (l *KeyLane) ClearHold() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held != nil {
		log.Printf("[signer] AUDIT key_lane_hold_cleared_by_operator=1 chain=%d address=%s nonce=%d", l.chainID, l.logAddress(), l.held.nonce)
		// The operator vouches the gap is filled: its record is no longer
		// evidence of a hole (records above it are kept).
		delete(l.sent, l.held.nonce)
	}
	l.held = nil
}

// dropMinedLocked forgets records the chain's mined nonce has passed.
func (l *KeyLane) dropMinedLocked(mined uint64) {
	for n := range l.sent {
		if n < mined {
			delete(l.sent, n)
		}
	}
}

// pruneLocked keeps the record map bounded, using the mined nonce only.
func (l *KeyLane) pruneLocked() {
	if len(l.sent) <= keyLanePruneAt || l.nonces == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyLaneReadTimeout)
	defer cancel()
	if mined, err := l.nonces.LatestNonce(ctx, l.logAddress()); err == nil {
		l.dropMinedLocked(mined)
	}
}

// stepBackLocked hands back the one nonce a send provably never used — the
// send that just took it — and only while the floor still sits just past it
// (re-review N3). Caller holds mu.
func (l *KeyLane) stepBackLocked(n uint64) {
	delete(l.sent, n)
	if l.nonceFloor == n+1 {
		l.nonceFloor = n
	}
}

// logAddress is the lane's address for a log line ("" on a solo lane that
// signs with its caller's signer).
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
		r.lanes[id] = lane
	}
	if role != "" && !containsString(r.roles[id], role) {
		r.roles[id] = append(r.roles[id], role)
	}
	return lane
}

// newBroadcasterLane is key's lane over bc, with bc's balance read and
// transaction lookup when it has them.
func newBroadcasterLane(key *ecdsa.PrivateKey, bc ReleaseBroadcaster, chainID int64) *KeyLane {
	l := &KeyLane{signer: NewLocalKeyReleaseSigner(key, bc), chainID: chainID}
	l.balances, _ = bc.(laneBalanceReader)
	l.lookup, _ = bc.(laneTxLookup)
	l.nonces, _ = bc.(laneNonceReader)
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
