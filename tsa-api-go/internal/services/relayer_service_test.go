package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// ── doubles ──────────────────────────────────────────────────────────────

// The doubles are mutex-guarded because the concurrency regression tests below
// drive them from many goroutines at once.
type fakeRelaySigner struct {
	mu             sync.Mutex
	addr           string
	broadcastCalls int
	txHash         string
	err            error
}

func (f *fakeRelaySigner) Address() string { return f.addr }
func (f *fakeRelaySigner) SignAndBroadcast(_ context.Context, _ []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcastCalls++
	if f.err != nil {
		return "", f.err
	}
	return f.txHash, nil
}
func (f *fakeRelaySigner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.broadcastCalls
}

type fakeRelayChain struct {
	mu         sync.Mutex
	callErr    error
	callReturn []byte
	balance    *big.Int
	balanceErr error
	callCount  int
}

func (f *fakeRelayChain) ChainID() *big.Int { return big.NewInt(146) }

func (f *fakeRelayChain) CallContract(_ context.Context, _ ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	return f.callReturn, f.callErr
}
func (f *fakeRelayChain) BalanceAt(_ context.Context, _ common.Address, _ *big.Int) (*big.Int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.balanceErr != nil {
		return nil, f.balanceErr
	}
	if f.balance == nil {
		return big.NewInt(1e18), nil
	}
	return f.balance, nil
}

type fakeRelayBuilder struct{ err error }

func (f *fakeRelayBuilder) BuildUnsignedTx(_, _ string, _ []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return json.Marshal(blockchain.UnsignedTx{GasPrice: "1000000000", GasLimit: 300_000, To: forwarderHex, Value: "0"})
}

type attempt struct{ outcome, reason string }

type fakeRelayMetrics struct {
	mu       sync.Mutex
	attempts []attempt
	gas      *big.Int
}

func (m *fakeRelayMetrics) RelayAttempt(outcome, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts = append(m.attempts, attempt{outcome, reason})
}
func (m *fakeRelayMetrics) RelayGasUnits(w *big.Int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gas == nil {
		m.gas = new(big.Int)
	}
	m.gas.Add(m.gas, w)
}
func (m *fakeRelayMetrics) last() attempt {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.attempts) == 0 {
		return attempt{}
	}
	return m.attempts[len(m.attempts)-1]
}

type fakeRoles struct {
	owner    string
	operator bool
	guardian bool
	ownerErr error
}

func (f fakeRoles) Owner(context.Context) (string, error) { return f.owner, f.ownerErr }
func (f fakeRoles) IsOperator(_ context.Context, _ string) (bool, error) {
	return f.operator, nil
}
func (f fakeRoles) IsGuardian(_ context.Context, _ string) (bool, error) {
	return f.guardian, nil
}

// ── fixtures ─────────────────────────────────────────────────────────────

const (
	swapAddr     = "0x1111111111111111111111111111111111111111"
	forwarderHex = "0x2222222222222222222222222222222222222222"
	relayerAddr  = "0x3333333333333333333333333333333333333333"
	userAddr     = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"
	otherUser    = "0x09124a9E3c1B5AcF4207E33828195D00846E5EA8"
)

var spendSel = SelectorOf("spend(address,uint256,bytes32)")

type rig struct {
	seq     atomic.Int32 // each req() is a distinct request (one request = one relay operation)
	svc     *RelayerService
	signer  *fakeRelaySigner
	chain   *fakeRelayChain
	metrics *fakeRelayMetrics
	clock   *time.Time
}

func newRig(t *testing.T, mutate ...func(*RelayerLimits)) *rig {
	t.Helper()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	limits := RelayerLimits{
		SponsorLimits: SponsorLimits{
			PerUserPerWindow: 3,
			Window:           time.Minute,
			DailyGasBudget:   big.NewInt(10_000_000),
			BalanceFloor:     big.NewInt(1e17),
		},
		MaxDeadline:  5 * time.Minute,
		GasCeiling:   500_000,
		MinSponsored: big.NewInt(1_000),
	}
	for _, m := range mutate {
		m(&limits)
	}
	signer := &fakeRelaySigner{addr: relayerAddr, txHash: "0xdeadbeef"}
	chain := &fakeRelayChain{}
	metrics := &fakeRelayMetrics{}
	r := &rig{signer: signer, chain: chain, metrics: metrics, clock: &now}

	svc, err := NewRelayerService(
		soloLane(signer, chainBalances{chain}), chain, &fakeRelayBuilder{}, forwarderHex,
		[]AllowedCall{{Target: swapAddr, Selector: spendSel, AmountWordIndex: 1}},
		limits, metrics,
		func() time.Time { return *r.clock },
	)
	if err != nil {
		t.Fatalf("NewRelayerService: %v", err)
	}
	svc.rolesOK.Store(true) // as ProbeRoles does once the key holds no swap role
	r.svc = svc
	return r
}

// spend(to, amount, ref) calldata: selector + 3 words.
func spendCalldata(amount int64) []byte {
	data := make([]byte, 4+32*3)
	copy(data[:4], spendSel[:])
	copy(data[4+32-20:4+32], common.HexToAddress(otherUser).Bytes())
	big.NewInt(amount).FillBytes(data[4+32 : 4+64])
	data[4+64] = 0x01 // non-zero ref
	return data
}

func (r *rig) req(mutate ...func(*ForwardRequest)) ForwardRequest {
	req := ForwardRequest{
		From:      userAddr,
		To:        swapAddr,
		Value:     big.NewInt(0),
		Gas:       200_000,
		Deadline:  uint64(r.clock.Add(2 * time.Minute).Unix()),
		Data:      spendCalldata(5_000),
		Signature: []byte{0x01, 0x02, 0x03, byte(r.seq.Add(1))},
	}
	for _, m := range mutate {
		m(&req)
	}
	return req
}

// ── tests ────────────────────────────────────────────────────────────────

func TestRelayer_HappyPathBroadcastsAndCountsGas(t *testing.T) {
	r := newRig(t)
	hash, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if hash != "0xdeadbeef" {
		t.Fatalf("unexpected hash %q", hash)
	}
	if r.signer.broadcastCalls != 1 {
		t.Fatalf("expected exactly one broadcast, got %d", r.signer.broadcastCalls)
	}
	if got := r.svc.SpentToday(); got.Cmp(big.NewInt(200_000)) != 0 {
		t.Fatalf("daily spend not recorded: %s", got)
	}
	if r.metrics.last() != (attempt{"ok", ""}) {
		t.Fatalf("expected an ok counter, got %+v", r.metrics.attempts)
	}
}

func TestRelayer_DisabledWhenNoKeyConfigured(t *testing.T) {
	svc, err := NewRelayerService(nil, nil, nil, "", nil, RelayerLimits{}, nil, nil)
	if err != nil {
		t.Fatalf("NewRelayerService: %v", err)
	}
	if svc.IsEnabled() {
		t.Fatal("an unconfigured relayer must report disabled")
	}
	// The app keys its self-pay fallback off this exact error, so it must be a
	// distinguishable sentinel and not a generic failure.
	if _, err := svc.Relay(context.Background(), userAddr, ForwardRequest{}); !errors.Is(err, ErrRelayerDisabled) {
		t.Fatalf("expected ErrRelayerDisabled, got %v", err)
	}
}

func TestRelayer_KillSwitchForcesSelfPay(t *testing.T) {
	r := newRig(t)
	r.svc.SetKillSwitch(true)
	if r.svc.IsEnabled() {
		t.Fatal("kill switch must disable the relayer even when fully configured")
	}
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrRelayerDisabled) {
		t.Fatalf("expected ErrRelayerDisabled, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("kill switch must stop broadcasts")
	}
}

// The single most important check: without it the endpoint relays anything
// anyone signed, for free.
func TestRelayer_RejectsWhenFromMismatchesAuthenticatedWallet(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), otherUser, r.req())
	if !errors.Is(err, ErrSenderMismatch) {
		t.Fatalf("expected ErrSenderMismatch, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("must not broadcast a request for a different wallet")
	}
}

func TestRelayer_RejectsUnlistedTarget(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		q.To = "0x9999999999999999999999999999999999999999"
	}))
	if !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("expected ErrTargetNotAllowed, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("must not broadcast to an unlisted target")
	}
}

func TestRelayer_RejectsUnlistedSelector(t *testing.T) {
	r := newRig(t)
	withdraw := SelectorOf("withdraw(address,address,uint256)")
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		d := spendCalldata(5_000)
		copy(d[:4], withdraw[:])
		q.Data = d
	}))
	if !errors.Is(err, ErrSelectorNotAllowed) {
		t.Fatalf("expected ErrSelectorNotAllowed, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("must not broadcast an unlisted selector")
	}
}

// The allowlist is a (target, selector) PAIR list — the same 4 bytes on another
// contract must not pass.
func TestRelayer_RejectsAllowedSelectorOnWrongTarget(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		q.To = "0x4444444444444444444444444444444444444444"
	}))
	if !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("expected ErrTargetNotAllowed, got %v", err)
	}
}

func TestRelayer_RejectsExpiredDeadline(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		q.Deadline = uint64(r.clock.Add(-time.Second).Unix())
	}))
	if !errors.Is(err, ErrDeadlineExpired) {
		t.Fatalf("expected ErrDeadlineExpired, got %v", err)
	}
}

// A signed request is executable by anyone until it expires, so a long deadline
// is a standing replay window against much later contract state.
func TestRelayer_RejectsDeadlineTooFarInTheFuture(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		q.Deadline = uint64(r.clock.Add(time.Hour).Unix())
	}))
	if !errors.Is(err, ErrDeadlineTooFar) {
		t.Fatalf("expected ErrDeadlineTooFar, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("must not broadcast a long-lived request")
	}
}

func TestRelayer_RejectsRequestAboveGasCeiling(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		q.Gas = 500_001
	}))
	if !errors.Is(err, ErrGasCeiling) {
		t.Fatalf("expected ErrGasCeiling, got %v", err)
	}
}

func TestRelayer_RejectsBelowMinSponsoredAmount(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Relay(context.Background(), userAddr, r.req(func(q *ForwardRequest) {
		q.Data = spendCalldata(999) // MinSponsored is 1000
	}))
	if !errors.Is(err, ErrBelowMinSponsored) {
		t.Fatalf("expected ErrBelowMinSponsored, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("dust must not burn relayer gas")
	}
}

func TestRelayer_PerUserRateLimitBlocksNPlusOne(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 3; i++ {
		if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
			t.Fatalf("request %d should have succeeded: %v", i+1, err)
		}
	}
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on the 4th, got %v", err)
	}
	// Per-user, not global: a different wallet is unaffected.
	if _, err := r.svc.Relay(context.Background(), otherUser, r.req(func(q *ForwardRequest) {
		q.From = otherUser
	})); err != nil {
		t.Fatalf("a different user must not be rate-limited: %v", err)
	}
}

func TestRelayer_RateLimitWindowRolls(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 3; i++ {
		if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
			t.Fatalf("setup request %d: %v", i, err)
		}
	}
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected the window to be full, got %v", err)
	}

	*r.clock = r.clock.Add(61 * time.Second) // past the 1-minute window

	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
		t.Fatalf("the window should have rolled: %v", err)
	}
}

// pricedRelayBuilder prepares a relay costing 300k gas at 1 gwei: its own fee,
// with gasFeeBuffer, is 6e14 wei.
type pricedRelayBuilder struct{}

func (pricedRelayBuilder) BuildUnsignedTx(_, _ string, _ []byte) ([]byte, error) {
	return json.Marshal(blockchain.UnsignedTx{Nonce: 1, GasPrice: "1000000000", GasLimit: 300_000,
		To: forwarderHex, Value: "0", ChainID: "146"})
}

// Floors are alert-only (owner 2026-10-13): a relayer below its floor that can
// still pay this relay's own fee relays (and alerts).
func TestRelayer_BelowFloorButAbleToPayStillRelays(t *testing.T) {
	r := newRig(t)
	r.svc.builder = pricedRelayBuilder{}
	r.chain.balance = big.NewInt(1e16) // floor is 1e17; the relay needs 6e14
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
		t.Fatalf("below the floor but able to pay: %v", err)
	}
	if r.signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.signer.calls())
	}
}

// A relayer that cannot pay this relay's own fee refuses with the existing
// error class and broadcasts nothing.
func TestRelayer_RefusesWhenItCannotPayTheRelaysFee(t *testing.T) {
	r := newRig(t)
	r.svc.builder = pricedRelayBuilder{}
	r.chain.balance = big.NewInt(1e14) // the relay needs 6e14
	_, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("expected ErrRelayerUnderfunded, got %v", err)
	}
	if r.signer.calls() != 0 {
		t.Fatal("must not broadcast transactions that will fail for gas")
	}
}

func TestRelayer_DailyGasBudgetExhausted(t *testing.T) {
	r := newRig(t, func(l *RelayerLimits) {
		l.DailyGasBudget = big.NewInt(250_000) // one 200k request fits, the second does not
		l.PerUserPerWindow = 100
	})
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrDailyCapExceeded) {
		t.Fatalf("expected ErrDailyCapExceeded, got %v", err)
	}

	// The budget is per UTC day, so it resets tomorrow.
	*r.clock = r.clock.Add(24 * time.Hour)
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
		t.Fatalf("budget should reset on the next UTC day: %v", err)
	}
}

func TestRelayer_RefusesOnSimulationRevertAndSurfacesInnerReason(t *testing.T) {
	r := newRig(t)
	r.chain.callErr = fmt.Errorf("execution reverted: Ref already used")

	_, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if !errors.Is(err, ErrSimulationReverted) {
		t.Fatalf("expected ErrSimulationReverted, got %v", err)
	}
	if !strings.Contains(err.Error(), "Ref already used") {
		t.Fatalf("the inner revert reason must survive: %v", err)
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("a request that would revert must never be broadcast")
	}
}

// Distinguishing a revert from an RPC failure is where relayers leak gas or drop
// good requests.
func TestRelayer_DoesNotBroadcastWhenSimulationErrorsTransiently(t *testing.T) {
	r := newRig(t)
	r.chain.callErr = fmt.Errorf("dial tcp: connection refused")

	_, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if !errors.Is(err, ErrSimulationUnavailable) {
		t.Fatalf("expected ErrSimulationUnavailable (retryable), got %v", err)
	}
	if errors.Is(err, ErrSimulationReverted) {
		t.Fatal("a transport failure must not be reported as a revert")
	}
	if r.signer.broadcastCalls != 0 {
		t.Fatal("must not broadcast when the simulation could not run")
	}
}

// Owner decision 2026-10-13: the relayer keeps its own key — owner, operator
// and guardian are all refused (boot then leaves the relayer dormant).
func TestRelayer_AssertRoleSeparationRejectsOwnerOperatorGuardian(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		roles fakeRoles
		want  bool
	}{
		{"relayer is owner", fakeRoles{owner: relayerAddr}, true},
		{"relayer is operator", fakeRoles{owner: userAddr, operator: true}, true},
		{"relayer is guardian", fakeRoles{owner: userAddr, guardian: true}, true},
		{"cleanly separated", fakeRoles{owner: userAddr}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			err := r.svc.AssertRoleSeparation(ctx, tc.roles)
			if tc.want && !errors.Is(err, ErrRoleSeparation) {
				t.Fatalf("expected ErrRoleSeparation, got %v", err)
			}
			if !tc.want && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestRelayer_EmitsCountersOnSuccessAndOnRejection(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := r.svc.Relay(context.Background(), otherUser, r.req()); err == nil {
		t.Fatal("expected the mismatch to be rejected")
	}

	var ok, rejected int
	var reason string
	for _, a := range r.metrics.attempts {
		switch a.outcome {
		case "ok":
			ok++
		case "rejected":
			rejected++
			reason = a.reason
		}
	}
	if ok != 1 || rejected != 1 {
		t.Fatalf("expected one ok and one rejected, got %+v", r.metrics.attempts)
	}
	if reason != "sender_mismatch" {
		t.Fatalf("rejections must be dimensioned by reason, got %q", reason)
	}
	if r.metrics.gas == nil || r.metrics.gas.Cmp(big.NewInt(200_000)) != 0 {
		t.Fatalf("gas counter not recorded: %v", r.metrics.gas)
	}
}

func TestRelayer_RejectsMalformedRequests(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		name  string
		req   ForwardRequest
		match string
	}{
		{"missing signature", r.req(func(q *ForwardRequest) { q.Signature = nil }), "signature"},
		{"calldata shorter than a selector", r.req(func(q *ForwardRequest) { q.Data = []byte{0x01} }), "calldata"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.svc.Relay(context.Background(), userAddr, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("expected an error mentioning %q, got %v", tc.match, err)
			}
			if r.signer.broadcastCalls != 0 {
				t.Fatal("malformed requests must not be broadcast")
			}
		})
	}
}

// Regression tests for the two concurrency defects found in review. Both were
// invisible to the single-goroutine suite above — the race detector reported
// nothing until requests actually overlapped.

func TestRelayer_DailyBudgetHoldsUnderConcurrency(t *testing.T) {
	r := newRig(t, func(l *RelayerLimits) {
		l.DailyGasBudget = big.NewInt(1_000_000) // exactly 5 x 200k
		l.PerUserPerWindow = 1000                // isolate the budget from the rate limit
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.svc.Relay(context.Background(), userAddr, r.req())
		}()
	}
	wg.Wait()

	// Before the fix this broadcast 10 times against a 5-request budget: the
	// check and the record took the mutex separately, so every goroutine saw the
	// pre-spend total. The overrun scaled with concurrency.
	if got := r.signer.calls(); got > 5 {
		t.Fatalf("budget overrun: %d broadcasts against a 5-request budget", got)
	}
	if got := r.svc.SpentToday(); got.Cmp(big.NewInt(1_000_000)) > 0 {
		t.Fatalf("recorded spend %s exceeds the budget", got)
	}
}

func TestRelayer_KillSwitchIsRaceFree(t *testing.T) {
	r := newRig(t, func(l *RelayerLimits) { l.PerUserPerWindow = 1000 })

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = r.svc.Relay(context.Background(), userAddr, r.req()) }()
		wg.Add(1)
		go func() { defer wg.Done(); r.svc.SetKillSwitch(false) }()
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.svc.IsEnabled() }()
	}
	wg.Wait() // -race fails this if IsEnabled reads `killed` unsynchronised
}

// A relayer-side outage should not burn the caller's hourly quota; a request
// that would revert is the caller's own doing and should.
func TestRelayer_RateLimitSlotRefundedOnlyWhenTheFailureIsOurs(t *testing.T) {
	t.Run("simulation unavailable refunds the slot", func(t *testing.T) {
		r := newRig(t, func(l *RelayerLimits) { l.PerUserPerWindow = 1 })
		r.chain.callErr = fmt.Errorf("dial tcp: connection refused")
		if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrSimulationUnavailable) {
			t.Fatalf("setup: %v", err)
		}
		r.chain.callErr = nil
		if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err != nil {
			t.Fatalf("the slot should have been returned: %v", err)
		}
	})

	t.Run("a would-revert request consumes the slot", func(t *testing.T) {
		r := newRig(t, func(l *RelayerLimits) { l.PerUserPerWindow = 1 })
		r.chain.callErr = fmt.Errorf("execution reverted: Ref already used")
		if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrSimulationReverted) {
			t.Fatalf("setup: %v", err)
		}
		r.chain.callErr = nil
		if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("expected the slot to stay consumed, got %v", err)
		}
	})
}

// The budget must not leak on a request that never reached the chain.
func TestRelayer_BudgetReleasedWhenTheRequestNeverBroadcasts(t *testing.T) {
	r := newRig(t)
	r.chain.callErr = fmt.Errorf("execution reverted: nope")

	if _, err := r.svc.Relay(context.Background(), userAddr, r.req()); err == nil {
		t.Fatal("expected a rejection")
	}
	if got := r.svc.SpentToday(); got.Sign() != 0 {
		t.Fatalf("budget leaked %s gas on a request that never broadcast", got)
	}
}

// laggingRelayBuilder prepares every relay at the same pending nonce, as a
// load-balanced RPC does while earlier relays are still unseen.
type laggingRelayBuilder struct{ nonce uint64 }

func (b laggingRelayBuilder) BuildUnsignedTx(_, _ string, _ []byte) ([]byte, error) {
	return unsignedOn(146, b.nonce)
}

// The relayer used to build and broadcast with no lock: concurrent relays on a
// lagging RPC signed one nonce twice. On its key lane they get 9, 10, 11 — and
// a relay on the operator's key shares the operator's lane.
func TestRelayer_ConcurrentRelaysOnTheKeyLaneGetDistinctNonces(t *testing.T) {
	r := newRig(t)
	key, _ := crypto.GenerateKey()
	node := &laneNode{id: 146}
	reg := NewSignerRegistry()
	lane := reg.Lane("relayer", key, node)
	r.svc.builder = laggingRelayBuilder{nonce: 9}
	r.svc.signer, r.svc.lane = lane.Signer(), lane
	if reg.Lane("it_operator", key, node) != lane {
		t.Fatal("relayer and operator on one key must share the lane")
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Three different requests (one request is one operation: a
			// retry of the same one reuses its allocation).
			req := r.req(func(f *ForwardRequest) { f.Data = spendCalldata(int64(5_000 + i)) })
			if _, err := r.svc.Relay(context.Background(), userAddr, req); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for _, n := range node.sent() {
		seen[n] = true
	}
	if len(seen) != 3 || !seen[9] || !seen[10] || !seen[11] {
		t.Fatalf("nonces %v, want 9, 10, 11", node.sent())
	}
}

// Review item 7: a relay the node definitely refused (nothing broadcast) is
// recorded as not broadcast, not as a broadcast error.
func TestRelayer_DefiniteRefusalIsRecordedAsNotBroadcast(t *testing.T) {
	r := newRig(t)
	r.signer.err = notBroadcast(errors.New("transaction underpriced"))
	_, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want not broadcast", err)
	}
	last := r.metrics.attempts[len(r.metrics.attempts)-1]
	if last.outcome != "rejected" || last.reason != "not_broadcast" {
		t.Fatalf("metric = %+v, want rejected/not_broadcast", last)
	}
}

// Review: the relayer's role check is re-probed — off while the key holds a
// swap role or the read fails, on once it reads clean.
func TestRelayer_ProbeRolesTurnsRelayingOnAndOff(t *testing.T) {
	r := newRig(t)
	r.svc.rolesOK.Store(false)
	if r.svc.IsEnabled() {
		t.Fatal("unprobed: must be off")
	}
	if err := r.svc.ProbeRoles(context.Background(), fakeRoles{owner: userAddr}); err != nil || !r.svc.IsEnabled() {
		t.Fatalf("clean roles: err=%v enabled=%v", err, r.svc.IsEnabled())
	}
	if err := r.svc.ProbeRoles(context.Background(), fakeRoles{owner: userAddr, operator: true}); err == nil || r.svc.IsEnabled() {
		t.Fatalf("operator: err=%v enabled=%v, want off", err, r.svc.IsEnabled())
	}
}
