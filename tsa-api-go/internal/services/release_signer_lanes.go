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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// KeyLane is the ONE send lane of one hot key on one chain (signer
// consolidation 2026-10-13): the lock held from nonce read to broadcast, the
// nonce floor that covers an RPC whose pending view lags our own sends, and the
// one fee gate every send on the key passes. Every role signing with that key
// on that chain (PM release, expiry cancel, IT operator, relayer, gas sponsor,
// treasury) sends through the same KeyLane, so two roles on one key can never
// read the same pending nonce or spend the same balance twice. Get one from a
// SignerRegistry; the lint test forbids building one anywhere else.
//
// Extracted from the release signer's per-chain lane, SponsorLane.sendMu and
// TreasuryLane's nonce floor, which were three copies of this.
type KeyLane struct {
	mu      sync.Mutex
	signer  ReleaseSigner
	chainID int64 // what the lane's broadcaster reports; 0 = unchecked (a solo lane)

	// nonceFloor is the next nonce this process may use on the lane (plan C1
	// review I2). Back-to-back sends go out faster than a load-balanced RPC's
	// pending view moves; without the floor the second would be built on the
	// first one's nonce and replace or bounce it. It is NOT a counter: it moves
	// only after a build succeeded (an EstimateGas revert never burns a nonce),
	// steps back on a definite refusal, and is dropped back to the chain's own
	// pending nonce when the transaction holding the gap never reached the node
	// (gapLocked). Read and written only under mu.
	nonceFloor uint64
	// sent is every nonce this lane signed at or above the chain's pending
	// view (highWater), with its hash and when it was prepared: the evidence the
	// floor rests on, aged from each send rather than refreshed by later ones.
	sent map[uint64]laneSent
	// highWater is the highest pending nonce any build has read, seenAt when a
	// build last read at least that. A view below it is a lagging RPC (the floor
	// holds), until none has reached it for keyLaneNonceFloorTTL (a dropped
	// mempool tx really did move the chain back).
	highWater uint64
	seenAt    time.Time

	balances laneBalanceReader // nil: no fee gate (a solo test lane)
	lookup   laneTxLookup      // nil: a gap is judged on age alone

	reserveMu sync.Mutex
	reserves  map[string]LaneReservation

	now func() time.Time
}

type laneSent struct {
	hash string
	at   time.Time
}

// laneBalanceReader is the lane broadcaster's native balance read
// (*blockchain.EVMClient).
type laneBalanceReader interface {
	GetBalance(address string) (*big.Int, error)
}

// laneTxLookup asks the node whether it holds a transaction, pending or mined
// (*blockchain.EVMClient).
type laneTxLookup interface {
	TransactionKnown(ctx context.Context, txHash string) (bool, error)
}

const (
	// keyLaneGapGrace is how long a sent transaction may stay unseen in the
	// chain's pending view before the lane asks the node whether it has it.
	keyLaneGapGrace = 15 * time.Second
	// keyLaneNonceFloorTTL is the gap verdict without a node lookup, and how long
	// a pending view may sit below the high-water mark before it is believed.
	keyLaneNonceFloorTTL = 30 * time.Second
)

// soloLane is a lane outside any registry, for a signer handed in directly (a
// test, or a constructor that takes a bare ReleaseSigner). balances nil = no
// fee gate. Boot never builds one: every boot signer comes from
// SignerRegistry.Lane (lint: TestKeyLanes_OnlyFromTheRegistry).
func soloLane(signer ReleaseSigner, balances laneBalanceReader) *KeyLane {
	return &KeyLane{signer: signer, balances: balances}
}

// chainBalances adapts a BalanceAt reader (a sponsor or relay chain) to the
// lane's balance read.
type chainBalances struct {
	c interface {
		BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
	}
}

func (b chainBalances) GetBalance(address string) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.c.BalanceAt(ctx, common.HexToAddress(address), nil)
}

// Signer is the lane's signer: Address() for every role, and what Send signs with.
func (l *KeyLane) Signer() ReleaseSigner { return l.signer }

// Address is the lane's key's address.
func (l *KeyLane) Address() string { return l.signer.Address() }

// ChainID is the chain the lane sends on (0 for a solo lane).
func (l *KeyLane) ChainID() int64 { return l.chainID }

// LaneReservation is what a lane must keep back, in wei at the given gas
// price, for transactions it already owes.
type LaneReservation func(ctx context.Context, price *big.Int) (*big.Int, error)

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

// reserved sums the lane's reservations at price.
func (l *KeyLane) reserved(ctx context.Context, price *big.Int) (*big.Int, error) {
	l.reserveMu.Lock()
	fns := make(map[string]LaneReservation, len(l.reserves))
	for k, v := range l.reserves {
		fns[k] = v
	}
	l.reserveMu.Unlock()
	sum := new(big.Int)
	for name, fn := range fns {
		r, err := fn(ctx, price)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if r != nil {
			sum.Add(sum, r)
		}
	}
	return sum, nil
}

type reservedSendKey struct{}

// ReservedSend marks ctx as carrying one of the transactions a lane's
// reservation is held for (a pool slice's release, cancel or resolve): its fee
// is checked, the reservation is not, since it is what the reservation pays.
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

// sendWith is Send signing with signer. Only a SponsorLane built without a
// registry lane passes a signer other than the lane's own (tests swap
// SponsorLane.Signer); every registry lane signs with its own.
func (l *KeyLane) sendWith(ctx context.Context, signer ReleaseSigner, build func() ([]byte, error), onPrepared func(uint64) error) (string, bool, error) {
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
	prepared := u.Nonce
	l.pinNonceLocked(ctx, &u)
	if u.Nonce != prepared {
		if unsigned, err = json.Marshal(u); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, notBroadcast(fmt.Errorf("key lane: re-encode the pinned tx: %w", err))
		}
	}
	if _, err := l.requireFee(ctx, signer.Address(), &u); err != nil {
		l.stepBackLocked(u.Nonce)
		return "", false, err
	}
	if onPrepared != nil {
		if err := onPrepared(u.Nonce); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, fmt.Errorf("key lane: record the nonce before broadcast: %w", err)
		}
	}
	txHash, err := signer.SignAndBroadcast(ctx, unsigned)
	if errors.Is(err, ErrTxNotBroadcast) {
		l.stepBackLocked(u.Nonce)
		return "", false, err
	}
	hash := txHash
	if err != nil {
		hash = uncertainTxHash(err)
	}
	if rec, ok := l.sent[u.Nonce]; ok {
		rec.hash = hash
		l.sent[u.Nonce] = rec
	}
	return txHash, true, err
}

// requireFee is the lane's one balance gate (it reads no lane state guarded by
// mu, so CanPay calls it without the lock) (floors only alert, owner
// 2026-10-13): the key must hold this transaction's gas limit × gas price ×
// gasFeeBuffer, plus its value, plus whatever the lane reserves for
// transactions it already owes (unless this is one of them). Fails closed when
// the transaction cannot be priced or the balance or reservation cannot be
// read. A solo lane (no balance reader) is not gated.
func (l *KeyLane) requireFee(ctx context.Context, address string, u *blockchain.UnsignedTx) (*big.Int, error) {
	if l.balances == nil {
		return nil, nil
	}
	price, ok := new(big.Int).SetString(u.GasPrice, 10)
	if !ok || price.Sign() <= 0 || u.GasLimit == 0 {
		return nil, notBroadcast(fmt.Errorf("key lane: cannot price the prepared tx (gasPrice %q, gasLimit %d)", u.GasPrice, u.GasLimit))
	}
	value := new(big.Int)
	if u.Value != "" {
		if _, ok := value.SetString(u.Value, 10); !ok {
			return nil, notBroadcast(fmt.Errorf("key lane: cannot read the prepared tx's value %q", u.Value))
		}
	}
	need := feeNeed(u.GasLimit, price, value)
	reserved := new(big.Int)
	if !isReservedSend(ctx) {
		r, err := l.reserved(ctx, price)
		if err != nil {
			return nil, notBroadcast(fmt.Errorf("key lane: read the lane's reservation: %w", err))
		}
		reserved = r
	}
	need.Add(need, reserved)
	bal, err := l.balances.GetBalance(address)
	if err != nil {
		return nil, notBroadcast(fmt.Errorf("key lane: read the balance: %w", err))
	}
	if bal.Cmp(need) < 0 {
		log.Printf("[signer] ALERT key_lane_cannot_pay_total=1 chain=%d address=%s balance=%s needed=%s reserved=%s",
			l.chainID, address, bal, need, reserved)
		return bal, notBroadcast(fmt.Errorf("%w: balance %s wei, this transaction needs %s (%s of it reserved)", ErrRelayerUnderfunded, bal, need, reserved))
	}
	return bal, nil
}

// CanPay is the lane's fee gate asked before anything is built (a quote, a
// pool open): units of gas at price plus value, plus the lane's reservation
// unless ctx is a ReservedSend. A solo lane always can.
func (l *KeyLane) CanPay(ctx context.Context, units uint64, price, value *big.Int) error {
	if l == nil || l.signer == nil {
		return nil
	}
	_, err := l.canPayAs(ctx, l.signer.Address(), units, price, value)
	return err
}

// canPayAs is CanPay for address (a solo lane signing with its caller's
// signer); it also returns the balance it read (nil on a solo lane without a
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
	return l.requireFee(ctx, address, &blockchain.UnsignedTx{GasLimit: units, GasPrice: price.String(), Value: value.String()})
}

// pinNonceLocked raises a prepared tx's nonce to the lane's floor, unless the
// floor rests on a transaction the node never got (gapLocked), and moves the
// floor past it. Caller holds mu.
func (l *KeyLane) pinNonceLocked(ctx context.Context, u *blockchain.UnsignedTx) {
	now := l.clock()
	if l.sent == nil {
		l.sent = map[uint64]laneSent{}
	}
	p := u.Nonce
	if p >= l.highWater || now.Sub(l.seenAt) >= keyLaneNonceFloorTTL {
		l.highWater, l.seenAt = p, now
		for n := range l.sent {
			if n < p {
				delete(l.sent, n)
			}
		}
	}
	if p < l.nonceFloor {
		if p == l.highWater && l.gapLocked(ctx, p, now) {
			log.Printf("[signer] ALERT key_lane_nonce_gap_recovered_total=1 chain=%d address=%s nonce=%d floor=%d — the transaction at this nonce never reached the node; reusing it",
				l.chainID, l.logAddress(), p, l.nonceFloor)
			for n := range l.sent {
				if n >= p {
					delete(l.sent, n)
				}
			}
			l.nonceFloor = p
		} else {
			u.Nonce = l.nonceFloor
		}
	}
	l.nonceFloor = u.Nonce + 1
	l.sent[u.Nonce] = laneSent{at: now}
}

// gapLocked reports whether the chain's pending nonce p is a hole: the
// transaction this lane sent at p is older than keyLaneGapGrace and the node
// says it does not have it (or, with no lookup, it is older than
// keyLaneNonceFloorTTL). No record of p means nothing of ours holds it.
func (l *KeyLane) gapLocked(ctx context.Context, p uint64, now time.Time) bool {
	rec, ok := l.sent[p]
	if !ok {
		return true
	}
	age := now.Sub(rec.at)
	if age < keyLaneGapGrace {
		return false
	}
	if l.lookup != nil && rec.hash != "" {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		known, err := l.lookup.TransactionKnown(lctx, rec.hash)
		cancel()
		if err == nil {
			return !known
		}
	}
	return age >= keyLaneNonceFloorTTL
}

// stepBackLocked hands back the one nonce a send provably never used, and only
// while the floor still sits just past it (re-review N3). Caller holds mu.
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
