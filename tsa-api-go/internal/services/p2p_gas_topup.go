package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// P2PGasAction names the user-signed P2P transaction a top-up pays for.
type P2PGasAction string

const (
	P2PGasApprove     P2PGasAction = "approve"
	P2PGasDeposit     P2PGasAction = "deposit"
	P2PGasWithdraw    P2PGasAction = "withdraw"
	P2PGasCancelAd    P2PGasAction = "cancel_ad"
	P2PGasOpenTrade   P2PGasAction = "open_trade"
	P2PGasMarkPaid    P2PGasAction = "mark_paid"
	P2PGasRelease     P2PGasAction = "release"
	P2PGasCancelTrade P2PGasAction = "cancel_trade"
	P2PGasDispute     P2PGasAction = "dispute"
)

// p2pActionGas is a gas-unit ceiling per action on Sonic P2PEscrowV3. Every
// figure sits above the hardhat gas report's max (2026-10-09: approve 46k,
// deposit 116k, withdraw 60k, cancelVoucher 62k, openTrade 251k, markPaid 47k,
// release 106k, cancelByTimeout 59k, raiseDispute 43k) — adjust upward only:
// an action priced short leaves her a top-up that cannot pay for the tx.
var p2pActionGas = map[P2PGasAction]uint64{
	P2PGasApprove:     70_000,
	P2PGasDeposit:     150_000,
	P2PGasWithdraw:    120_000,
	P2PGasCancelAd:    90_000,
	P2PGasOpenTrade:   350_000,
	P2PGasMarkPaid:    80_000,
	P2PGasRelease:     250_000,
	P2PGasCancelTrade: 200_000,
	P2PGasDispute:     90_000,
}

// ParseP2PGasAction reads an action name from a request, ignoring case and
// surrounding space.
func ParseP2PGasAction(s string) (P2PGasAction, bool) {
	a := P2PGasAction(strings.ToLower(strings.TrimSpace(s)))
	_, ok := p2pActionGas[a]
	return a, ok
}

var (
	// ErrP2PGasUnavailable: there is no live sponsor lane on the tx's chain,
	// it refused for reasons of ours (float, ceiling, budget), or a chain read
	// or the send failed. She can still add gas herself.
	ErrP2PGasUnavailable = errors.New("we can't cover the network fee right now — add a little gas (S on Sonic, BNB on BNB Chain) to your wallet, or try again shortly")
	// ErrP2PGasNotNeeded: the trade or ad named has no tx for her to sign now
	// (not her turn, wrong state, a chain with no P2P escrow).
	ErrP2PGasNotNeeded = errors.New("there's nothing for you to sign on that right now")
	// ErrP2PGasUnused: the S we sent her last has not been spent yet.
	ErrP2PGasUnused = errors.New("the network fee we sent you last hasn't been used yet — finish that step first")
)

const (
	// p2pGasUnusedWindow is how long an unspent drip blocks the next one.
	p2pGasUnusedWindow = time.Hour
)

// gasUnavailableError is ErrP2PGasUnavailable with the cause that led to it.
// The cause is in the message (logged, redacted) but deliberately not in the
// Unwrap chain: a cause that happens to wrap a caller-refusal sentinel (KYC,
// wallet, daily cap) must never be mistaken for that refusal.
type gasUnavailableError struct{ cause error }

func gasUnavailable(cause error) error { return gasUnavailableError{cause: cause} }

func (e gasUnavailableError) Error() string {
	if e.cause == nil {
		return ErrP2PGasUnavailable.Error()
	}
	return ErrP2PGasUnavailable.Error() + " (" + e.cause.Error() + ")"
}

func (e gasUnavailableError) Unwrap() error { return ErrP2PGasUnavailable }

// P2PGasRequest is one top-up request: the action, the trade or ad it is for
// (uuid.Nil when the action takes none), and the chain the app says the tx is
// on (0 from older apps: the default chain, Sonic).
type P2PGasRequest struct {
	Action  P2PGasAction
	TradeID uuid.UUID
	AdID    uuid.UUID
	ChainID int64
}

// P2PGasNeed answers whether the caller really has req's tx to sign now, and
// on which chain. *P2PTradeService implements it (RequireP2PGasNeed).
type P2PGasNeed interface {
	RequireP2PGasNeed(ctx context.Context, user *models.User, wallet string, req P2PGasRequest) (int64, error)
}

// P2PGasLane is one chain's sponsor lane for P2P drips: the sponsorship that
// holds it and the chain key it is held under.
type P2PGasLane struct {
	ChainKey string
	Sponsor  *RampSponsorship
}

// P2PGasSponsor drips native gas (S on Sonic, BNB on BSC) to a P2P user's own
// wallet before a tx she signs there. Each chain sends through its own sponsor
// lane on the gas_sponsor key (Sonic's is the one the marketplace funding
// shares); every lane takes its KeyLane from the signer registry, so one key
// never runs two nonce sequences on one chain. Sends use GasTopUp's send: only
// the shortfall, skipped when she already holds it, the lane's ceiling, float
// floor and daily gas budget. Its own controls, on top of the KYC the P2P
// routes require:
//   - every drip is backed by real trade/ad state on that chain (P2PGasNeed);
//   - a per-user daily count of sends across every chain (skips do not count);
//   - no new drip on a chain while her last one there sits unspent;
//   - one request in flight per user.
//
// The counters are in memory, like every SponsorGuard: a restart resets them.
type P2PGasSponsor struct {
	lanes   map[int64]P2PGasLane
	need    P2PGasNeed
	perUser *SponsorGuard
	now     func() time.Time

	mu       sync.Mutex
	inFlight map[uuid.UUID]struct{}
	// lastDrip is each user's most recent send per chain: when, and the
	// balance it brought her wallet to.
	lastDrip map[p2pDripKey]p2pDrip
}

// p2pDripKey is one user's drips on one chain: balances are per chain, so a
// BNB drip says nothing about her S.
type p2pDripKey struct {
	user    uuid.UUID
	chainID int64
}

type p2pDrip struct {
	at          time.Time
	postBalance *big.Int
	txHash      string
	op          string
}

// NewP2PGasSponsor keeps each chain's lane that is live (chain id → lane),
// and is nil when none is or there is no need checker, so a dormant sponsor is
// a nil pointer whose TopUp refuses with ErrP2PGasUnavailable.
func NewP2PGasSponsor(lanes map[int64]P2PGasLane, need P2PGasNeed, perDay int, now func() time.Time) *P2PGasSponsor {
	live := map[int64]P2PGasLane{}
	for id, l := range lanes {
		if l.Sponsor.Enabled() && l.Sponsor.topUp != nil {
			live[id] = l
		}
	}
	if len(live) == 0 || need == nil {
		return nil
	}
	if perDay <= 0 {
		perDay = config.DefaultP2PGasTopUpsPerDay
	}
	if now == nil {
		now = time.Now
	}
	return &P2PGasSponsor{
		lanes:    live,
		need:     need,
		perUser:  NewSponsorGuard("p2p_gas_user", SponsorLimits{PerUserPerWindow: perDay, Window: 24 * time.Hour}, now),
		now:      now,
		inFlight: map[uuid.UUID]struct{}{},
		lastDrip: map[p2pDripKey]p2pDrip{},
	}
}

// ChainIDs lists the chains this sponsor can drip on.
func (p *P2PGasSponsor) ChainIDs() []int64 {
	if p == nil {
		return nil
	}
	out := make([]int64, 0, len(p.lanes))
	for id := range p.lanes {
		out = append(out, id)
	}
	return out
}

// TopUp sends `wallet` the native gas req.Action needs on its chain at today's
// gas price, times topUpGasBuffer, minus what it already holds. wallet must be the user's own
// linked wallet (the handler passes the proven one).
//
// Our own failures (no lane, float, ceiling, budget, a chain read, a send that
// failed or is unconfirmed) all come back as a gasUnavailableError (matches
// ErrP2PGasUnavailable and nothing else); an unconfirmed send also returns
// its txHash in the result.
func (p *P2PGasSponsor) TopUp(ctx context.Context, user *models.User, wallet string, req P2PGasRequest) (GasTopUpResult, error) {
	// NewP2PGasSponsor sets every field or returns nil, so nil is the only
	// dormant state.
	if p == nil {
		return GasTopUpResult{}, ErrP2PGasUnavailable
	}
	if err := requireTopUpUser(user); err != nil {
		return GasTopUpResult{}, err
	}
	units, ok := p2pActionGas[req.Action]
	if !ok {
		return GasTopUpResult{}, fmt.Errorf("%w: unknown gas action %q", ErrInvalidParams, req.Action)
	}
	if !common.IsHexAddress(strings.TrimSpace(wallet)) || !sameAddress(wallet, user.WalletAddress) {
		return GasTopUpResult{}, ErrSponsorWallet
	}
	chainID, err := p.need.RequireP2PGasNeed(ctx, user, wallet, req)
	if err != nil {
		if errors.Is(err, ErrInvalidParams) || errors.Is(err, ErrP2PGasNotNeeded) || KYCRefusalCode(err) != "" {
			return GasTopUpResult{}, err
		}
		return GasTopUpResult{}, gasUnavailable(fmt.Errorf("need check: %w", err))
	}
	chainLane, ok := p.lanes[chainID]
	if !ok {
		// A P2P chain with no sponsor lane here (no client, a testnet escrow):
		// she has a tx to sign but we cannot pay for it.
		return GasTopUpResult{}, gasUnavailable(fmt.Errorf("no sponsor lane on chain %d", chainID))
	}
	if !p.enter(user.ID) {
		return GasTopUpResult{}, fmt.Errorf("%w: a gas top-up is already being sent", ErrSponsorshipInProgress)
	}
	defer p.leave(user.ID)

	g := chainLane.Sponsor.topUp
	lane := g.liveLane(chainLane.ChainKey)
	if lane == nil {
		return GasTopUpResult{}, ErrP2PGasUnavailable
	}
	dripKey := p2pDripKey{user: user.ID, chainID: chainID}
	need, err := lane.gasCost(units)
	if err != nil {
		return GasTopUpResult{}, gasUnavailable(err)
	}
	need.Mul(need, big.NewInt(topUpGasBuffer))
	if err := p.refuseUnusedDrip(ctx, lane, dripKey, wallet, need); err != nil {
		return GasTopUpResult{}, err
	}

	var slot SponsorReservation
	dripOp := "p2p_drip:" + uuid.NewString() // one drip, one operation
	res, err := g.sendTopUp(ctx, topUpSend{
		lane: lane, wallet: wallet, need: need, op: dripOp,
		ref: fmt.Sprintf("p2p=%s chain=%d user=%s", req.Action, chainID, user.ID),
		admit: func() error {
			s, err := p.perUser.ReserveUser("p2p:" + user.ID.String())
			if errors.Is(err, ErrRateLimited) {
				return ErrTopUpDailyCap
			}
			if err != nil {
				return err
			}
			slot = s
			return nil
		},
		release: func() { p.perUser.ReleaseUser(slot) },
		// In-memory only (no DB row), so never inside the lane's transaction:
		// noted once the drip is committed (sent or in flight).
		sent: func(txHash string) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.lastDrip[dripKey] = p2pDrip{at: p.now(), postBalance: new(big.Int).Set(need), txHash: txHash, op: dripOp}
		},
	})
	// The chain the drip is for, so the app waits only for a drip on its own
	// chain (additive: older apps ignore it).
	res.ChainID = chainID
	switch {
	case err == nil, errors.Is(err, ErrTopUpDailyCap), errors.Is(err, ErrSponsorshipInProgress):
		return res, err
	}
	return res, gasUnavailable(err)
}

// refuseUnusedDrip refuses a new send while her last drip is under an hour old
// and her balance has not dropped below what it brought her to: no tx has
// been paid from the wallet since (every tx burns gas). Only asked when a
// send would happen; a wallet that already holds `need` is a skip either way.
func (p *P2PGasSponsor) refuseUnusedDrip(ctx context.Context, lane *SponsorLane, key p2pDripKey, wallet string, need *big.Int) error {
	p.mu.Lock()
	last, ok := p.lastDrip[key]
	if ok && p.now().Sub(last.at) >= p2pGasUnusedWindow {
		delete(p.lastDrip, key)
		ok = false
	}
	p.mu.Unlock()
	if !ok {
		return nil
	}
	if lane.callCancelled(ctx, last.txHash) && lane.KeyLane.GiveUp(ctx, lane.Signer.Address(), last.op, last.txHash, RetirePermanent) {
		// The last drip was cancelled at its nonce: it never arrived, so it
		// is not an unused drip — a new one may go.
		p.mu.Lock()
		delete(p.lastDrip, key)
		p.mu.Unlock()
		return nil
	}
	have, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(wallet), nil)
	if err != nil {
		return gasUnavailable(fmt.Errorf("read user balance: %w", err))
	}
	if have.Cmp(need) < 0 && have.Cmp(last.postBalance) >= 0 {
		return ErrP2PGasUnused
	}
	return nil
}

// enter claims the user's one in-flight slot; false when a request holds it.
func (p *P2PGasSponsor) enter(userID uuid.UUID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, busy := p.inFlight[userID]; busy {
		return false
	}
	p.inFlight[userID] = struct{}{}
	// Drop stale drips here so the map holds only the last hour's senders.
	now := p.now()
	for id, d := range p.lastDrip {
		if now.Sub(d.at) >= p2pGasUnusedWindow {
			delete(p.lastDrip, id)
		}
	}
	return true
}

func (p *P2PGasSponsor) leave(userID uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inFlight, userID)
}
