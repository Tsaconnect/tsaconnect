package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// ── doubles ──────────────────────────────────────────────────────────────

type fakeRelaySigner struct {
	addr           string
	broadcastCalls int
	txHash         string
	err            error
}

func (f *fakeRelaySigner) Address() string { return f.addr }
func (f *fakeRelaySigner) SignAndBroadcast(_ context.Context, _ []byte) (string, error) {
	f.broadcastCalls++
	if f.err != nil {
		return "", f.err
	}
	return f.txHash, nil
}

type fakeRelayChain struct {
	callErr    error
	balance    *big.Int
	balanceErr error
	callCount  int
}

func (f *fakeRelayChain) CallContract(_ context.Context, _ ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.callCount++
	return nil, f.callErr
}
func (f *fakeRelayChain) BalanceAt(_ context.Context, _ common.Address, _ *big.Int) (*big.Int, error) {
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
	return []byte(`{"unsigned":true}`), nil
}

type attempt struct{ outcome, reason string }

type fakeRelayMetrics struct {
	attempts []attempt
	gas      *big.Int
}

func (m *fakeRelayMetrics) RelayAttempt(outcome, reason string) {
	m.attempts = append(m.attempts, attempt{outcome, reason})
}
func (m *fakeRelayMetrics) RelayGasSpent(w *big.Int) {
	if m.gas == nil {
		m.gas = new(big.Int)
	}
	m.gas.Add(m.gas, w)
}
func (m *fakeRelayMetrics) last() attempt {
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
		MaxDeadline:      5 * time.Minute,
		GasCeiling:       500_000,
		PerUserPerWindow: 3,
		Window:           time.Minute,
		DailyGasBudget:   big.NewInt(10_000_000),
		BalanceFloor:     big.NewInt(1e17),
		MinSponsored:     big.NewInt(1_000),
	}
	for _, m := range mutate {
		m(&limits)
	}
	signer := &fakeRelaySigner{addr: relayerAddr, txHash: "0xdeadbeef"}
	chain := &fakeRelayChain{}
	metrics := &fakeRelayMetrics{}
	r := &rig{signer: signer, chain: chain, metrics: metrics, clock: &now}

	svc, err := NewRelayerService(
		signer, chain, &fakeRelayBuilder{}, forwarderHex,
		[]AllowedCall{{Target: swapAddr, Selector: spendSel, AmountWordIndex: 1}},
		limits, metrics,
		func() time.Time { return *r.clock },
	)
	if err != nil {
		t.Fatalf("NewRelayerService: %v", err)
	}
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
		Signature: []byte{0x01, 0x02, 0x03},
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

func TestRelayer_RefusesWhenBalanceBelowFloor(t *testing.T) {
	r := newRig(t)
	r.chain.balance = big.NewInt(1e16) // floor is 1e17
	_, err := r.svc.Relay(context.Background(), userAddr, r.req())
	if !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("expected ErrRelayerUnderfunded, got %v", err)
	}
	if r.signer.broadcastCalls != 0 {
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
