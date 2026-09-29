package services

import (
	"context"
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
	"github.com/ethereum/go-ethereum/common/hexutil"
)

var (
	// ErrTreasuryKilled maps the lane guard's own Killed() to a caller-facing
	// sentinel. The guard stays the single source of truth for the kill
	// switch; this is only how that state is reported.
	ErrTreasuryKilled = errors.New("conversions are paused")
	// ErrTreasuryCapExceeded is returned when an order or a chain's in-flight
	// exposure would cross the value caps configured in TreasuryCaps. A lane
	// that cannot pay for a transaction's gas reuses the guard's own
	// ErrRelayerUnderfunded — no third sentinel.
	ErrTreasuryCapExceeded = errors.New("this order is above the conversion limit right now")
	// ErrTreasuryInvalidRoute is returned when a LiFi route fails a safety
	// check before anything is signed: a `to` or `approvalAddress` outside
	// the lane's router allowlist, an unexpected approval token, a
	// non-zero/malformed value, unparsable calldata, or an expired quote.
	// LiFi's response is untrusted input, so every one of these fails closed.
	ErrTreasuryInvalidRoute = errors.New("this conversion route cannot be executed safely")
	// ErrTreasuryInvalidRecipient guards TransferToken: common.HexToAddress
	// silently coerces a malformed string to a zero/truncated address, so the
	// recipient is validated explicitly before it is ever used.
	ErrTreasuryInvalidRecipient = errors.New("recipient address is invalid")
)

// TreasuryLane is a SponsorLane plus what a token custodian needs: the
// numeric chain id LiFi quotes want, and the value caps the guard cannot
// express (the guard's DailyGasBudget is denominated in gas units, not USD).
type TreasuryLane struct {
	*SponsorLane // ChainKey, Chain, Builder, Native, Signer, Guard, Tokens, send()
	ChainID      int64

	// Routers is the allowlist of contract addresses a route on this lane may
	// call or be approved to spend, parsed and validated once at boot. Empty
	// means no route may execute: a lane without an allowlist is a lane that
	// would sign anything.
	Routers []common.Address

	// convertCh is a capacity-1 channel used as a context-aware lock that
	// serialises the whole allowance-read -> approve -> route sequence for
	// THIS lane. It is deliberately separate from SponsorLane.sendMu, which
	// only serialises one nonce read and its broadcast: an approve here is
	// sized to the EXACT amount of one specific route, so "read the current
	// allowance, then decide whether to top it up" is racy by construction.
	// Without this lock, two concurrent conversions on the same lane could
	// both read the same stale allowance, both decide to approve, and leave
	// the second route call spending against an allowance the first route
	// call already consumed (or vice versa) — a revert at best, a stuck
	// approve at worst. Do NOT remove this thinking it duplicates sendMu; it
	// protects a different critical section (read-then-decide, not
	// nonce-then-broadcast).
	//
	// It is a channel rather than a sync.Mutex because sync.Mutex.Lock is not
	// context-aware: a caller queued behind a slow approve (the wait for its
	// receipt can run up to 60s) must be able to abandon the wait the moment
	// ITS OWN context is cancelled, not just block until the lane is free.
	convertOnce sync.Once
	convertCh   chan struct{}
}

// convertLock lazily creates the lane's lock channel the first time it is
// used, so a TreasuryLane's zero value is safe to use directly (e.g. in
// tests that construct one without going through NewTreasury).
func (l *TreasuryLane) convertLock() chan struct{} {
	l.convertOnce.Do(func() { l.convertCh = make(chan struct{}, 1) })
	return l.convertCh
}

// TreasuryCaps bound the treasury's value exposure. A treasury caps VALUE,
// not gas units, so this lives outside SponsorLimits rather than overloading
// DailyGasBudget for a unit it was never meant to hold.
type TreasuryCaps struct {
	MaxInFlightUSDPerOrder float64
	MaxInFlightUSDPerChain float64
}

// Treasury holds and moves user funds in transit for a converted buy: one
// lane per chain, each a SponsorLane with the treasury's own SponsorGuard
// (kill switch + low-balance warning floor) and this treasury's value caps.
type Treasury struct {
	lanes map[string]*TreasuryLane
	caps  TreasuryCaps
	now   func() time.Time
	// address is computed once in NewTreasury: see Address's doc comment.
	address string

	gasMu    sync.Mutex
	gasCache map[string]gasReading // chain -> last quote-time reading
}

// gasReading is one lane's native balance and gas price, as read for
// quoting. Execution never uses it: requireGas always reads fresh.
type gasReading struct {
	balance, price *big.Int
	at             time.Time
}

// treasuryGasCacheTTL bounds how stale a quote-time gas reading may be. The
// quote endpoint is public, so a fresh read per request would be two RPC calls
// anyone can trigger; a lane drained inside the window is still caught at
// execution, before anything is sent.
const treasuryGasCacheTTL = 30 * time.Second

// NewTreasury normalises lane keys to lower case (Lane looks up by lower
// case) and computes the treasury's address once, deterministically, from
// the lanes given — see Address.
func NewTreasury(lanes map[string]*TreasuryLane, caps TreasuryCaps, now func() time.Time) *Treasury {
	if now == nil {
		now = time.Now
	}
	normalized := make(map[string]*TreasuryLane, len(lanes))
	for k, lane := range lanes {
		normalized[strings.ToLower(strings.TrimSpace(k))] = lane
	}

	t := &Treasury{lanes: normalized, caps: caps, now: now, gasCache: map[string]gasReading{}}
	t.address = computeTreasuryAddress(normalized)
	return t
}

// computeTreasuryAddress picks the address every lane's signer SHOULD share
// (one EOA key signs on every chain). It is computed once, from lanes sorted
// by chain key, so it does not depend on Go's randomised map iteration order.
// If any lane's signer disagrees, that is a configuration error serious
// enough to fail closed: an empty Address() rather than a guess.
func computeTreasuryAddress(lanes map[string]*TreasuryLane) string {
	keys := make([]string, 0, len(lanes))
	for k := range lanes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var address string
	mismatch := false
	for _, k := range keys {
		addr := lanes[k].Signer.Address()
		switch {
		case address == "":
			address = addr
		case !strings.EqualFold(addr, address):
			mismatch = true
		}
	}
	if mismatch {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_address_mismatch_total=1 lanes=%v", keys)
		return ""
	}
	return address
}

// Address is the treasury's own EOA, or "" if the configured lanes disagree
// on what that address is (computeTreasuryAddress already alerted). A caller
// that gets "" must refuse the converted-buy path rather than guess.
func (t *Treasury) Address() string { return t.address }

func (t *Treasury) Lane(chainKey string) *TreasuryLane {
	return t.lanes[strings.ToLower(strings.TrimSpace(chainKey))]
}

// ChainKeys is every chain the treasury has a lane on, sorted — for a caller
// offering the treasury as somewhere to be paid.
func (t *Treasury) ChainKeys() []string {
	if t == nil {
		return nil
	}
	keys := make([]string, 0, len(t.lanes))
	for k := range t.lanes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SetKillSwitch fans out to every lane's Guard — there is no second,
// treasury-level switch to keep in sync.
func (t *Treasury) SetKillSwitch(on bool) {
	for _, lane := range t.lanes {
		lane.Guard.SetKillSwitch(on)
	}
}

// Killed reports the treasury as paused once any lane's guard is killed:
// SetKillSwitch always sets every lane together, so in normal operation this
// is "all" or "none" — checking "any" just fails safe if that ever drifts. A
// treasury with zero lanes is dormant (Enabled() is false), not "killed":
// there is nothing here for a kill switch to be paused FROM.
func (t *Treasury) Killed() bool {
	for _, lane := range t.lanes {
		if lane.Guard.Killed() {
			return true
		}
	}
	return false
}

// CheckCaps is the gate every caller runs BEFORE reserving a leg: kill switch
// first, then the per-order cap, then this chain's total in-flight exposure.
// The treasury holds user funds, so the caps are what bounds a bad day. The
// kill switch is the lane guard's, not a second one.
func (t *Treasury) CheckCaps(chainKey string, orderUSD, inFlightUSD float64) error {
	lane := t.Lane(chainKey)
	if lane == nil {
		return ErrSponsorNotAvailable
	}
	if lane.Guard.Killed() {
		return ErrTreasuryKilled
	}
	if t.caps.MaxInFlightUSDPerOrder > 0 && orderUSD > t.caps.MaxInFlightUSDPerOrder {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_order_cap_total=1 chain=%s order_usd=%.2f cap=%.2f",
			chainKey, orderUSD, t.caps.MaxInFlightUSDPerOrder)
		return ErrTreasuryCapExceeded
	}
	if t.caps.MaxInFlightUSDPerChain > 0 && inFlightUSD+orderUSD > t.caps.MaxInFlightUSDPerChain {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_chain_cap_total=1 chain=%s in_flight=%.2f order=%.2f cap=%.2f",
			chainKey, inFlightUSD, orderUSD, t.caps.MaxInFlightUSDPerChain)
		return ErrTreasuryCapExceeded
	}
	return nil
}

// treasuryERC20Gas budgets one ERC-20 approve or transfer from the treasury:
// the same allowance the gas top-up makes for a transfer it cannot estimate.
const treasuryERC20Gas = topUpFallbackTransferGas

// requireGas refuses unless the lane's native balance pays for `units` of gas
// at the current price — the question that decides whether a transaction can
// go out, rather than whether the balance is above an arbitrary floor. A lane
// below its floor that can still pay keeps working; the floor only raises the
// refill ALERT (the guard logs it). Checked before anything is sent, because a
// half-sent conversion — an approve with no route after it — is worse than
// one that never started.
func (t *Treasury) requireGas(ctx context.Context, lane *TreasuryLane, units uint64) error {
	bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(lane.Signer.Address()), nil)
	if err != nil {
		return fmt.Errorf("treasury: read native balance: %w", err)
	}
	_ = lane.Guard.CheckFloor(bal) // a refill warning, not a stop
	cost, err := lane.gasCost(units)
	if err != nil {
		return fmt.Errorf("treasury: %w", err)
	}
	if bal.Cmp(cost) < 0 {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_gas_short_total=1 chain=%s balance=%s needed=%s gas=%d",
			lane.ChainKey, bal, cost, units)
		return fmt.Errorf("%w: %s holds %s wei, needs %s for %d gas", ErrRelayerUnderfunded, lane.ChainKey, bal, cost, units)
	}
	return nil
}

// routeValueIsZero reports whether route.Value encodes exactly zero, after
// trimming an optional 0x/0X prefix case-insensitively (so "", "0", "0x0",
// "0X0", "00", ... all read as zero). Every route this treasury executes
// spends an ERC-20 it already holds, so anything that fails to parse, or
// parses to non-zero, is treated as not-zero by the caller and refused
// outright — never bounded, per the Phase 2 ruling.
func routeValueIsZero(raw string) bool {
	v := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
	if v == "" {
		return true
	}
	parsed, ok := new(big.Int).SetString(v, 16)
	return ok && parsed.Sign() == 0
}

// routeExpired reports whether route.ValidUntil (unix seconds) has passed as
// of t.now(). Zero means the quote carried no deadline.
func (t *Treasury) routeExpired(route *LiFiRoute) bool {
	if route.ValidUntil == 0 {
		return false
	}
	return !t.now().Before(time.Unix(route.ValidUntil, 0))
}

// CanPayForGas reports whether chainKey's lane can pay for `units` of gas,
// for deciding whether to OFFER a converted route at all — so a user is never
// sold a conversion the treasury cannot send. Readings are cached for
// treasuryGasCacheTTL; an unreadable balance or price reads as "cannot pay",
// since offering the route anyway is the failure this exists to prevent.
func (t *Treasury) CanPayForGas(ctx context.Context, chainKey string, units uint64) bool {
	lane := t.Lane(chainKey)
	if lane == nil || lane.SponsorLane == nil {
		return false
	}
	reading, ok := t.gasReadingFor(ctx, lane)
	if !ok {
		return false
	}
	cost := new(big.Int).Mul(new(big.Int).SetUint64(units), reading.price)
	return reading.balance.Cmp(cost) >= 0
}

func (t *Treasury) gasReadingFor(ctx context.Context, lane *TreasuryLane) (gasReading, bool) {
	t.gasMu.Lock()
	defer t.gasMu.Unlock()
	if r, ok := t.gasCache[lane.ChainKey]; ok && t.now().Sub(r.at) < treasuryGasCacheTTL {
		return r, true
	}
	bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(lane.Signer.Address()), nil)
	if err != nil {
		log.Printf("[ramp-conversion] treasury gas reading on %s: balance: %v", lane.ChainKey, err)
		return gasReading{}, false
	}
	price, err := lane.Native.SuggestGasPrice()
	if err != nil {
		log.Printf("[ramp-conversion] treasury gas reading on %s: gas price: %v", lane.ChainKey, err)
		return gasReading{}, false
	}
	r := gasReading{balance: bal, price: price, at: t.now()}
	t.gasCache[lane.ChainKey] = r
	return r, true
}

// maxRouteGasLimit bounds the gas a LiFi route may ask the treasury to pay
// for. The route's gasLimit is untrusted: a large one on a call that burns it
// drains the lane's native balance in one transaction, which the balance floor
// cannot stop (the node only checks that the balance covers limit × price).
// Live LiFi limits topped out at ~2.3M (gasZip, 2026-09-22); the ceiling is
// about twice that. A route with no limit gets the ceiling rather than an
// estimate, since estimating a gas-burning call returns the block gas limit.
const maxRouteGasLimit uint64 = 5_000_000

// ExecuteRoute approves the route's spender if needed, then signs LiFi's
// transactionRequest AS GIVEN — never rebuilt from parts, the same invariant
// the BTC/PSBT path holds.
//
// Callers MUST run CheckCaps for this leg's order/chain BEFORE calling
// ExecuteRoute: this method only re-checks the kill switch and that the lane
// can pay for the gas, not the value caps, which are a per-order/per-chain decision this
// lane-scoped method cannot make on its own.
//
// broadcastAttempted separates a build failure (nothing left this process,
// safe to free the caller's reservation) from a broadcast whose outcome is
// unknown (the caller must keep the leg on that path, exactly as Phase 1's
// lane does) — and it is STICKY: once the approve step has actually
// broadcast, every later return from this call also reports
// attempted=true, even if the route's own build then fails before
// broadcasting anything. Without that stickiness a caller could see
// attempted=false after the approve already landed on-chain, free/retry the
// leg, and leave a stray allowance (and possibly a double-spent gas fee)
// behind it.
//
// ExecuteRoute is meant to be called from a background worker, not directly
// off a request path: a lane serialises conversions (see TreasuryLane's
// convertCh), so a call here can genuinely wait — up to the ~60s approve
// receipt timeout — behind another conversion already in flight on the same
// lane. A caller that must not block that long should pass a ctx it is
// willing to cancel; ExecuteRoute abandons the wait for the lane's lock (not
// a wait already broadcast) the moment ctx is done.
func (t *Treasury) ExecuteRoute(ctx context.Context, lane *TreasuryLane, token string, amount *big.Int, route *LiFiRoute) (string, bool, error) {
	if lane == nil || lane.SponsorLane == nil || lane.Guard == nil {
		return "", false, ErrSponsorNotAvailable
	}
	if lane.Guard.Killed() {
		return "", false, ErrTreasuryKilled
	}
	tokenAddr := lane.tokenAddress(token) // SponsorLane's own lookup
	if tokenAddr == "" || route == nil || route.To == "" || route.Data == "" {
		return "", false, ErrSponsorNotAvailable
	}

	// Everything below validates the ROUTE ITSELF before touching the chain.
	// LiFi's response is untrusted input; a treasury that signs whatever it
	// says is a wallet drainer with extra steps.

	// route.To is the single most dangerous field in the response: it is the
	// contract the treasury's own EOA is about to call with attacker-chosen
	// calldata. Validating ApprovalToken/Value/Data without validating WHERE
	// the call goes leaves the drain wide open — `to` = the USDC contract
	// with transfer(them, balance) spends the whole float, not one order.
	// Both `to` and the approval spender must be on the lane's allowlist.
	if !routerAllowed(lane.Routers, route.To) {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_route_to_refused_total=1 chain=%s to=%s tool=%s",
			lane.ChainKey, route.To, route.Tool)
		return "", false, fmt.Errorf("%w: route targets an address outside the allowlist", ErrTreasuryInvalidRoute)
	}
	if route.ApprovalAddress != "" && !routerAllowed(lane.Routers, route.ApprovalAddress) {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_route_spender_refused_total=1 chain=%s spender=%s tool=%s",
			lane.ChainKey, route.ApprovalAddress, route.Tool)
		return "", false, fmt.Errorf("%w: route names a spender outside the allowlist", ErrTreasuryInvalidRoute)
	}

	// route.ApprovalToken is asserted by the quote alone. Accept it only
	// when it case-insensitively matches the token this order is actually
	// converting — never approve an address a third party's response merely
	// claims is right.
	if route.ApprovalToken != "" && !strings.EqualFold(route.ApprovalToken, tokenAddr) {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_approval_token_mismatch_total=1 chain=%s quoted=%s expected=%s",
			lane.ChainKey, route.ApprovalToken, tokenAddr)
		return "", false, fmt.Errorf("%w: route names an unexpected approval token", ErrTreasuryInvalidRoute)
	}

	// Phase 2 ruling: every route here spends an ERC-20 the treasury already
	// holds. A route asking for non-zero native value is refused outright,
	// not bounded — it is either malformed or a shape this treasury was
	// never designed to sign.
	if !routeValueIsZero(route.Value) {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_route_value_refused_total=1 chain=%s value=%q", lane.ChainKey, route.Value)
		return "", false, fmt.Errorf("%w: route requests a non-zero or malformed value %q", ErrTreasuryInvalidRoute, route.Value)
	}
	// hexutil.Decode (unlike common.FromHex) errors on malformed hex instead
	// of silently truncating it, so a corrupt payload is refused, not signed.
	callData, derr := hexutil.Decode(route.Data)
	if derr != nil {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_route_data_refused_total=1 chain=%s err=%v", lane.ChainKey, derr)
		return "", false, fmt.Errorf("%w: route data is not valid hex: %v", ErrTreasuryInvalidRoute, derr)
	}
	if route.GasLimit > maxRouteGasLimit {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_route_gas_refused_total=1 chain=%s gas_limit=%d ceiling=%d tool=%s",
			lane.ChainKey, route.GasLimit, maxRouteGasLimit, route.Tool)
		return "", false, fmt.Errorf("%w: route asks for %d gas, above the %d ceiling", ErrTreasuryInvalidRoute, route.GasLimit, maxRouteGasLimit)
	}
	gasLimit := route.GasLimit
	if gasLimit == 0 {
		gasLimit = maxRouteGasLimit
	}
	if t.routeExpired(route) {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_route_expired_total=1 chain=%s valid_until=%d now=%d",
			lane.ChainKey, route.ValidUntil, t.now().Unix())
		return "", false, fmt.Errorf("%w: route quote has expired", ErrTreasuryInvalidRoute)
	}

	// Context-aware acquisition: sync.Mutex.Lock cannot be cancelled, so a
	// caller queued behind another conversion on this lane would otherwise
	// block until the lane frees up even after its own ctx is done. Nothing
	// has been broadcast yet at this point, so a cancellation here is a
	// clean, attempted=false refusal.
	select {
	case lane.convertLock() <- struct{}{}:
		defer func() { <-lane.convertCh }()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}

	needApprove := false
	if route.ApprovalAddress != "" {
		allowance, err := lane.Native.GetTokenAllowance(tokenAddr, lane.Signer.Address(), route.ApprovalAddress)
		if err != nil {
			return "", false, fmt.Errorf("treasury: read allowance: %w", err)
		}
		needApprove = allowance.Cmp(amount) < 0
	}
	gasUnits := gasLimit
	if needApprove {
		gasUnits += treasuryERC20Gas
	}
	if err := t.requireGas(ctx, lane, gasUnits); err != nil {
		return "", false, err
	}

	var approveAttempted bool
	if needApprove {
		// Exact-amount approval, never MaxUint256: the spender is a
		// third-party router that changes weekly. lane.send carries the
		// nonce lock and the broadcastAttempted contract.
		approveHash, attempted, aerr := lane.send(ctx, func() ([]byte, error) {
			return lane.Native.PrepareERC20Approve(tokenAddr, lane.Signer.Address(), route.ApprovalAddress, amount)
		})
		approveAttempted = attempted // sticky from here on, see doc comment
		if aerr != nil {
			return "", approveAttempted, fmt.Errorf("treasury: approve: %w", aerr)
		}
		receipt, rerr := lane.Native.WaitForReceiptContext(ctx, approveHash, 60*time.Second)
		if rerr != nil {
			return "", true, fmt.Errorf("treasury: approve unconfirmed: %w", rerr)
		}
		// An approve that reverted granted no allowance. Broadcasting
		// the route anyway would spend against an allowance that was
		// never actually set. Same pattern as gas_topup.go's confirm().
		if receipt.Status != 1 {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_approve_reverted_total=1 chain=%s tx=%s", lane.ChainKey, approveHash)
			return "", true, fmt.Errorf("treasury: approve reverted on-chain (tx %s)", approveHash)
		}
	}

	// Task 4's generalized builder, reached through the lane's existing
	// Builder — the same interface the sponsor already holds. gasLimit is
	// never zero here (a missing or malformed limit became the ceiling
	// above), so the builder never estimates an unbounded one. value is
	// always zero: routeValueIsZero already refused anything else above.
	txHash, attempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Builder.BuildUnsignedCall(lane.Signer.Address(), route.To, callData, big.NewInt(0), gasLimit)
	})
	attempted = attempted || approveAttempted // sticky, see doc comment
	if err != nil {
		if attempted {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_broadcast_uncertain_total=1 chain=%s tool=%s err=%v",
				lane.ChainKey, route.Tool, err)
		}
		return "", attempted, fmt.Errorf("treasury: broadcast route: %w", err)
	}
	return txHash, true, nil
}

// TransferToken moves a token out of the treasury — a shortfall top-up or a
// refund. Same uncertain-broadcast contract as ExecuteRoute, and the same
// CheckCaps-first obligation: this method only checks the kill switch, that
// the lane can pay for the gas, and the recipient's shape, not the value caps.
// An attempted broadcast that errored returns the signed transaction's hash
// when the signer knows it, so the caller can later ask the chain about that
// very transaction; the ramp's callers ignore the hash on any error.
func (t *Treasury) TransferToken(ctx context.Context, lane *TreasuryLane, token, to string, amount *big.Int) (string, bool, error) {
	if lane == nil || lane.SponsorLane == nil || lane.Guard == nil {
		return "", false, ErrSponsorNotAvailable
	}
	if lane.Guard.Killed() {
		return "", false, ErrTreasuryKilled
	}
	// common.HexToAddress silently coerces an empty or malformed string to a
	// truncated/zero address, so a bad recipient is validated explicitly
	// before it is ever used — a refund or top-up sent to 0x0 is gone.
	if !common.IsHexAddress(to) {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_invalid_recipient_total=1 chain=%s to=%q", lane.ChainKey, to)
		return "", false, ErrTreasuryInvalidRecipient
	}
	tokenAddr := lane.tokenAddress(token)
	if tokenAddr == "" {
		return "", false, ErrSponsorNotAvailable
	}
	if err := t.requireGas(ctx, lane, treasuryERC20Gas); err != nil {
		return "", false, err
	}
	txHash, attempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Native.PrepareERC20Transfer(tokenAddr, lane.Signer.Address(), to, amount)
	})
	if err != nil {
		if attempted {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_broadcast_uncertain_total=1 chain=%s kind=transfer tx=%s err=%v",
				lane.ChainKey, uncertainTxHash(err), err)
			return uncertainTxHash(err), true, fmt.Errorf("treasury: broadcast transfer: %w", err)
		}
		return "", false, fmt.Errorf("treasury: broadcast transfer: %w", err)
	}
	return txHash, true, nil
}

// Enabled mirrors RampSponsorship.Enabled(): a treasury with no lanes is
// dormant, not broken.
func (t *Treasury) Enabled() bool { return t != nil && len(t.lanes) > 0 }

// FloatBalances returns the same []SponsorFloat shape jobs.GasFloatMonitor
// already consumes, so a later task can register that existing monitor a
// second time for the treasury instead of writing a new one. Copied from
// RampSponsorship.FloatBalances's loop: per lane, errors.Join, carry on past
// a bad RPC rather than hiding every other chain's reading behind one.
func (t *Treasury) FloatBalances(ctx context.Context) ([]SponsorFloat, error) {
	var out []SponsorFloat
	var errs []error
	for chain, lane := range t.lanes {
		addr := lane.Signer.Address()
		bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(addr), nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("treasury float %s: %w", chain, err))
			continue
		}
		out = append(out, SponsorFloat{
			Chain: chain, Address: addr, BalanceWei: bal, FloorWei: lane.Guard.Floor(),
			TokenUSD: t.laneKeptUSD(lane, addr),
		})
	}
	return out, errors.Join(errs...)
}

// laneKeptUSD sums a lane's USDC/USDT balances — the unspent round-trip
// budget a converted buy leaves sitting on the treasury as USDC (Decision:
// sweeping stays manual, this only feeds the alert). Both are treated as $1;
// a bad token read is logged and treated as 0 rather than failing the whole
// FloatBalances call, the same carry-on-past-one-bad-read stance the native
// balance loop above takes at the chain level.
func (t *Treasury) laneKeptUSD(lane *TreasuryLane, address string) float64 {
	var usd float64
	for symbol, tokenAddr := range lane.Tokens {
		if !IsStableAsset(symbol) {
			continue
		}
		bal, err := lane.Native.GetTokenBalance(tokenAddr, address)
		if err != nil {
			log.Printf("[ramp-conversion] treasury kept-USDC read %s %s: %v", lane.ChainKey, symbol, err)
			continue
		}
		decimals, err := lane.Native.GetTokenDecimals(tokenAddr)
		if err != nil {
			log.Printf("[ramp-conversion] treasury kept-USDC decimals %s %s: %v", lane.ChainKey, symbol, err)
			continue
		}
		amount, err := strconv.ParseFloat(BaseUnitsToDecimal(bal, decimals), 64)
		if err != nil {
			log.Printf("[ramp-conversion] treasury kept-USDC parse %s %s: %v", lane.ChainKey, symbol, err)
			continue
		}
		usd += amount
	}
	return usd
}

// routerAllowed reports whether addr is one of the lane's allowlisted router
// contracts. An empty allowlist allows NOTHING: a misconfigured lane must
// refuse every route rather than accept every route. addr comes from LiFi,
// so it is parsed strictly — common.HexToAddress alone would coerce garbage
// into some address — and then compared as an address, so EIP-55 casing
// cannot make one contract read as two.
func routerAllowed(allowlist []common.Address, addr string) bool {
	if !common.IsHexAddress(addr) {
		return false
	}
	want := common.HexToAddress(addr)
	for _, a := range allowlist {
		if a == want {
			return true
		}
	}
	return false
}
