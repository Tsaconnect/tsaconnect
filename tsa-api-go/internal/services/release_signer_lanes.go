package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// KeyLane is the ONE send lane of one hot key on one chain (signer
// consolidation 2026-10-13): the lock held from nonce read to broadcast, and
// the nonce floor that covers an RPC whose pending view lags our own sends.
// Every role signing with that key on that chain (PM release, IT operator,
// relayer, gas sponsor, treasury) sends through the same KeyLane, so two roles
// on one key can never read the same pending nonce. Get one from a
// SignerRegistry; a lane built any other way is private to its holder.
//
// Extracted from the release signer's per-chain lane, SponsorLane.sendMu and
// TreasuryLane's nonce floor, which were three copies of this.
type KeyLane struct {
	mu      sync.Mutex
	signer  ReleaseSigner
	chainID int64 // what the lane's broadcaster reports; 0 = unchecked (a test lane)

	// nonceFloor is the next nonce this process may use on the lane, set by
	// every send it prepares (plan C1 review I2). Back-to-back sends go out
	// faster than a load-balanced RPC's pending view moves; without the floor
	// the second would be built on the first one's nonce and replace or bounce
	// it. Read and written only under mu. It expires after keyLaneNonceFloorTTL
	// so a dropped tx never leaves the lane permanently ahead of the chain.
	//
	// This is not the in-process counter MCGPSwapService once warned against:
	// the floor moves only after a build succeeded (an EstimateGas revert never
	// burns a nonce), steps back on a definite rejection, and expires.
	nonceFloor   uint64
	nonceFloorAt time.Time
	now          func() time.Time
}

const keyLaneNonceFloorTTL = 30 * time.Second

// NewKeyLane is a lane private to its caller. Shared lanes come from
// SignerRegistry.Lane.
func NewKeyLane(signer ReleaseSigner, chainID int64) *KeyLane {
	return &KeyLane{signer: signer, chainID: chainID}
}

// Signer is the lane's signer: Address() for every role, and what Send signs with.
func (l *KeyLane) Signer() ReleaseSigner { return l.signer }

// Address is the lane's key's address.
func (l *KeyLane) Address() string { return l.signer.Address() }

// ChainID is the chain the lane sends on (0 for a lane with no chain check).
func (l *KeyLane) ChainID() int64 { return l.chainID }

// Send builds (prepare + nonce read), pins the nonce to the lane's floor,
// signs and broadcasts, all under the lane's lock. attempted separates a
// build failure or a definite refusal (ErrTxNotBroadcast: nothing left this
// process) from a broadcast whose outcome is unknown, which callers must treat
// as possibly sent. A build error is returned as build returned it.
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
	if l.chainID != 0 && u.ChainID != "" && u.ChainID != strconv.FormatInt(l.chainID, 10) {
		return "", false, notBroadcast(fmt.Errorf("key lane: a tx built for chain %s was sent on chain %d", u.ChainID, l.chainID))
	}
	prepared := u.Nonce
	l.pinNonceLocked(&u)
	if u.Nonce != prepared {
		if unsigned, err = json.Marshal(u); err != nil {
			l.stepBackLocked(u.Nonce)
			return "", false, notBroadcast(fmt.Errorf("key lane: re-encode the pinned tx: %w", err))
		}
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
	return txHash, true, err
}

// pinNonceLocked raises a prepared tx's nonce to the lane's floor and moves
// the floor past it. Caller holds mu.
func (l *KeyLane) pinNonceLocked(u *blockchain.UnsignedTx) {
	now := l.clock()
	if !l.nonceFloorAt.IsZero() && now.Sub(l.nonceFloorAt) < keyLaneNonceFloorTTL && u.Nonce < l.nonceFloor {
		u.Nonce = l.nonceFloor
	}
	l.nonceFloor, l.nonceFloorAt = u.Nonce+1, now
}

// stepBackLocked hands back the one nonce a send provably never used, and only
// while the floor still sits just past it (re-review N3). Caller holds mu.
func (l *KeyLane) stepBackLocked(n uint64) {
	if l.nonceFloor == n+1 {
		l.nonceFloor = n
	}
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
		return NewKeyLane(NewLocalKeyReleaseSigner(key, bc), chainID)
	}
	id := keyLaneID{address: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), chainID: chainID}
	r.mu.Lock()
	defer r.mu.Unlock()
	lane := r.lanes[id]
	if lane == nil {
		lane = NewKeyLane(NewLocalKeyReleaseSigner(key, bc), chainID)
		r.lanes[id] = lane
	}
	if role != "" && !containsString(r.roles[id], role) {
		r.roles[id] = append(r.roles[id], role)
	}
	return lane
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
