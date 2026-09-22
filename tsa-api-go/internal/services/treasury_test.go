package services

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// treasuryTestRecipient is a well-formed (but unrelated) hex address reused
// wherever a test needs a VALID recipient for TransferToken — sponsorTestUser
// and sponsorTestDeposit already exist for that shape in sponsor_fakes_test.go.
const treasuryTestRecipient = sponsorTestDeposit

// treasuryTestRouter is LiFi's real diamond — the one address a route on the
// test lane is allowed to call or be approved to spend. treasuryTestToken is
// a well-formed address that is NOT on the allowlist: the drain shape the
// allowlist exists to stop is `to` = a token contract with transfer() as the
// calldata, which spends the whole float rather than one order's amount.
const (
	treasuryTestRouter = "0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE"
	treasuryTestToken  = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
)

var treasuryTestRouters = []common.Address{common.HexToAddress(treasuryTestRouter)}

// newTestTreasury wires one "base" lane on top of the existing sponsor test
// rig (newSponsorRig, sponsor_fakes_test.go) with caps: 2000 USD per order,
// 10000 USD per chain. Reused rather than a parallel fixture: a treasury lane
// IS a SponsorLane with a different guard.
func newTestTreasury(t *testing.T) *Treasury {
	t.Helper()
	tr, _, _, _ := newTestTreasuryWithFakes(t)
	return tr
}

// newTestTreasuryWithFakes exposes the underlying fakes for tests that need
// to assert on what was actually signed/broadcast.
func newTestTreasuryWithFakes(t *testing.T) (*Treasury, *TreasuryLane, *fakeSponsorNative, *fakeRelaySigner) {
	t.Helper()
	rig, _ := newSponsorRig("base", "USDC")
	lane := &TreasuryLane{SponsorLane: rig.lane, ChainID: 8453, Routers: treasuryTestRouters}
	tr := NewTreasury(map[string]*TreasuryLane{"base": lane}, TreasuryCaps{
		MaxInFlightUSDPerOrder: 2000,
		MaxInFlightUSDPerChain: 10000,
	}, func() time.Time { return sponsorTestNow })
	return tr, lane, rig.native, rig.signer
}

// validRoute is a route that ExecuteRoute should accept and sign as given:
// zero value, valid hex data, no expiry, an approval address that needs a
// fresh approve (default fake allowance is zero).
func validRoute() *LiFiRoute {
	return &LiFiRoute{To: treasuryTestRouter, Data: "0xdeadbeef", Value: "0x0", GasLimit: 250_000, ApprovalAddress: treasuryTestRouter}
}

// ── caps / kill switch ──────────────────────────────────────────────────

func TestTreasury_RefusesAboveThePerOrderCap(t *testing.T) {
	tr := newTestTreasury(t)
	if err := tr.CheckCaps("base", 2_500, 0); !errors.Is(err, ErrTreasuryCapExceeded) {
		t.Fatalf("want ErrTreasuryCapExceeded, got %v", err)
	}
	if err := tr.CheckCaps("base", 1_500, 9_000); !errors.Is(err, ErrTreasuryCapExceeded) {
		t.Fatalf("per-chain exposure must also cap, got %v", err)
	}
	if err := tr.CheckCaps("base", 1_500, 1_000); err != nil {
		t.Fatalf("inside both caps: %v", err)
	}
}

func TestTreasury_KillSwitchStopsEverything(t *testing.T) {
	tr := newTestTreasury(t)
	tr.SetKillSwitch(true)
	if err := tr.CheckCaps("base", 1, 0); !errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("want ErrTreasuryKilled, got %v", err)
	}
}

func TestTreasury_CheckCapsUnknownChain(t *testing.T) {
	tr := newTestTreasury(t)
	if err := tr.CheckCaps("nowhere", 1, 0); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("want ErrSponsorNotAvailable for an unknown chain, got %v", err)
	}
}

func TestTreasury_KilledFalseWithNoLanes(t *testing.T) {
	empty := NewTreasury(map[string]*TreasuryLane{}, TreasuryCaps{}, nil)
	if empty.Killed() {
		t.Fatalf("a treasury with no lanes is dormant, not killed")
	}
}

// ── ExecuteRoute: the happy paths ───────────────────────────────────────

// A route is signed exactly as LiFi returned it: same target, same calldata.
func TestTreasury_ExecuteRouteSignsTheRouteAsGiven(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	hash, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err != nil || !attempted || hash != "0xsponsored" { // fakeRelaySigner's canned hash, see newSponsorRig
		t.Fatalf("hash=%s attempted=%v err=%v", hash, attempted, err)
	}

	builder := lane.Builder.(*fakeSponsorBuilder)
	to, data, _, gasLimit := builder.lastCall()
	if to != treasuryTestRouter || hex.EncodeToString(data) != "deadbeef" {
		t.Fatalf("route rebuilt instead of signed as given: to=%s data=%x", to, data)
	}
	if gasLimit != 250_000 {
		t.Fatalf("route's own gas limit not honoured: got %d", gasLimit)
	}
	_, _, approveSpender, approveAmount := native.approveCall()
	if approveSpender != treasuryTestRouter {
		t.Fatalf("approval went to %q, want the route's approvalAddress", approveSpender)
	}
	if approveAmount == nil || approveAmount.Cmp(big.NewInt(100_000000)) != 0 {
		t.Fatalf("approval amount = %v, want the exact leg amount (never MaxUint256)", approveAmount)
	}
	if signer.calls() != 2 { // approve + route
		t.Fatalf("broadcasts = %d, want approve then route", signer.calls())
	}
}

// A held allowance already covering the amount skips the approve step
// entirely — one broadcast, not two.
func TestTreasury_ExecuteRouteSkipsApproveWhenAllowanceCovers(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.allowance = big.NewInt(1_000_000000)
	route := validRoute()
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err != nil || !attempted {
		t.Fatalf("attempted=%v err=%v", attempted, err)
	}
	if _, _, _, approveAmount := native.approveCall(); approveAmount != nil {
		t.Fatalf("approve should have been skipped, got amount %v", approveAmount)
	}
	if signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want just the route", signer.calls())
	}
}

// A zero ValidUntil means the quote carried no deadline at all — it must not
// be treated as already-expired.
func TestTreasury_ExecuteRouteAcceptsZeroValidUntil(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.ValidUntil = 0
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err != nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want success with no deadline", attempted, err)
	}
}

// route.ApprovalToken matching the order's token case-insensitively (e.g. a
// checksummed vs. lower-case address) is accepted, not just an exact match.
func TestTreasury_ExecuteRouteAcceptsCaseInsensitiveApprovalToken(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.ApprovalToken = sponsorTestUSDC // same address as the lane's own token
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err != nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want success with a matching approval token", attempted, err)
	}
}

// ── ExecuteRoute: guard / floor / unknown lane ──────────────────────────

// The kill switch refuses ExecuteRoute before anything is built.
func TestTreasury_ExecuteRouteRefusedWhenKilled(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	tr.SetKillSwitch(true)
	route := validRoute()
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100), route)
	if !errors.Is(err, ErrTreasuryKilled) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryKilled and attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

func TestTreasury_ExecuteRouteRefusedBelowFloor(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	lane.Chain.(*fakeSponsorChain).balances[lane.Signer.Address()] = big.NewInt(1) // below the 1_000 wei floor
	route := validRoute()
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100), route)
	if !errors.Is(err, ErrRelayerUnderfunded) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrRelayerUnderfunded and attempted=false", attempted, err)
	}
}

// tr.Lane("unknown") returns nil; the natural call pattern
// tr.ExecuteRoute(ctx, tr.Lane(chain), ...) must not panic on a bad chain.
func TestTreasury_ExecuteRouteRefusesUnknownLane(t *testing.T) {
	tr := newTestTreasury(t)
	_, attempted, err := tr.ExecuteRoute(context.Background(), tr.Lane("nowhere"), "USDC", big.NewInt(100), validRoute())
	if !errors.Is(err, ErrSponsorNotAvailable) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrSponsorNotAvailable and attempted=false", attempted, err)
	}
}

// ── ExecuteRoute: untrusted route content (I4/I5/I7) ────────────────────

func TestTreasury_ExecuteRouteRefusesMismatchedApprovalToken(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.ApprovalToken = "0x000000000000000000000000000000000000dead" // not the lane's USDC
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryInvalidRoute and attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
	if _, _, _, amt := native.approveCall(); amt != nil {
		t.Fatalf("approve should never have been attempted, got amount %v", amt)
	}
}

func TestTreasury_ExecuteRouteRefusesNonZeroValue(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.Value = "0x1"
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryInvalidRoute and attempted=false", attempted, err)
	}
	builder := lane.Builder.(*fakeSponsorBuilder)
	if to, _, _, _ := builder.lastCall(); to != "" {
		t.Fatalf("builder should never have been called, got to=%q", to)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

func TestTreasury_ExecuteRouteRefusesMalformedValue(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.Value = "not-hex"
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryInvalidRoute and attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

func TestTreasury_ExecuteRouteRefusesMalformedData(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.Data = "not-hex-data"
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryInvalidRoute and attempted=false", attempted, err)
	}
	builder := lane.Builder.(*fakeSponsorBuilder)
	if to, _, _, _ := builder.lastCall(); to != "" {
		t.Fatalf("builder should never have been called, got to=%q", to)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

func TestTreasury_ExecuteRouteRefusesExpiredRoute(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.ValidUntil = sponsorTestNow.Add(-1 * time.Second).Unix() // already passed
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryInvalidRoute and attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

// ── ExecuteRoute: uncertain-broadcast surfacing (C1, I10) ───────────────

// The approve read fails before anything is broadcast — a plain, non-sticky
// build-time failure.
func TestTreasury_ExecuteRouteRefusedWhenAllowanceReadFails(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.allowanceErr = errors.New("rpc down")
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || attempted {
		t.Fatalf("attempted=%v err=%v, want a plain error with attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

// PrepareERC20Approve itself fails (e.g. gas price lookup blew up) before any
// broadcast happens — attempted=false, same as any other build failure.
func TestTreasury_ExecuteRouteRefusedWhenApprovePrepareFails(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.approveErr = errors.New("gas price unavailable")
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || attempted {
		t.Fatalf("attempted=%v err=%v, want a plain error with attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

// A broadcast whose outcome is unknown (build succeeded, SignAndBroadcast
// errored) at the APPROVE step must be surfaced as attempted=true.
func TestTreasury_ExecuteRouteSurfacesUncertainBroadcastAtApprove(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	signer.err = errors.New("rpc timeout")
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want an error with attempted=true (the approve broadcast is what timed out)", attempted, err)
	}
	if signer.calls() != 1 {
		t.Fatalf("only the approve should have been attempted, got %d calls", signer.calls())
	}
}

// Same as above, but at the ROUTE step: allowance already covers the amount
// (approve skipped), so the uncertain broadcast is the route call itself.
func TestTreasury_ExecuteRouteSurfacesUncertainBroadcastAtRoute(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.allowance = big.NewInt(1_000_000000) // skip the approve
	signer.err = errors.New("rpc timeout")
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want an error with attempted=true (the route broadcast is what timed out)", attempted, err)
	}
	builder := lane.Builder.(*fakeSponsorBuilder)
	if to, _, _, _ := builder.lastCall(); to != treasuryTestRouter {
		t.Fatalf("route build should have been reached, builder.to=%q", to)
	}
	if signer.calls() != 1 {
		t.Fatalf("only the route should have been attempted, got %d calls", signer.calls())
	}
}

// C2: an approve that reverted on-chain granted no allowance. Broadcasting
// the route anyway would spend against an allowance that was never set.
func TestTreasury_ExecuteRouteRefusedWhenApproveReverted(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.receiptStatus = 0 // reverted
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want an error with attempted=true (the approve itself landed, reverted)", attempted, err)
	}
	builder := lane.Builder.(*fakeSponsorBuilder)
	if to, _, _, _ := builder.lastCall(); to != "" {
		t.Fatalf("the route must never be broadcast against a reverted approve, builder.to=%q", to)
	}
	if signer.calls() != 1 {
		t.Fatalf("only the approve should have been broadcast, got %d calls", signer.calls())
	}
}

// The approve's receipt never confirms (RPC/timeout) — the approve may still
// be in flight, so this is uncertain, not a clean failure.
func TestTreasury_ExecuteRouteRefusedWhenApproveUnconfirmed(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.receiptErr = errors.New("timed out waiting for receipt")
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want an error with attempted=true", attempted, err)
	}
	builder := lane.Builder.(*fakeSponsorBuilder)
	if to, _, _, _ := builder.lastCall(); to != "" {
		t.Fatalf("the route must never be broadcast while the approve is unconfirmed, builder.to=%q", to)
	}
	if signer.calls() != 1 {
		t.Fatalf("only the approve should have been broadcast, got %d calls", signer.calls())
	}
}

// C1 (the core regression): the approve broadcasts successfully, but the
// ROUTE's own build then fails (e.g. gas estimation reverts) BEFORE any
// broadcast for the route. attempted must still be true: an approve is
// already live on-chain, so a caller must not read this as "nothing left
// this process" and free/retry the leg.
func TestTreasury_ExecuteRouteRouteBuildFailureAfterApproveStaysAttempted(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	builder := lane.Builder.(*fakeSponsorBuilder)
	builder.err = errors.New("gas estimation failed (would revert)")

	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if err == nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want an error with attempted=true — the approve already broadcast", attempted, err)
	}
	if signer.calls() != 1 {
		t.Fatalf("only the approve should have reached a broadcast, got %d calls", signer.calls())
	}
}

// ── I8: TreasuryLane.convertCh serialises per-lane conversions ──────────

func TestTreasury_ExecuteRouteSerializesConcurrentConversionsPerLane(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	// Widen the allowance-read-then-decide window so two goroutines racing
	// through it without convertCh would overlap almost certainly.
	native.allowanceDelay = 20 * time.Millisecond

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected err: %v", i, err)
		}
	}
	if max := native.maxAllowanceConcurrency(); max > 1 {
		t.Fatalf("TreasuryLane.convertCh did not serialise: observed %d concurrent allowance reads", max)
	}
	// Every conversion needed a fresh approve (the fake never remembers a
	// prior approve as raising its allowance), so each of the n legs should
	// have broadcast exactly twice (approve + route) if properly serialised.
	if got, want := signer.calls(), 2*n; got != want {
		t.Fatalf("broadcasts = %d, want %d (approve+route per leg)", got, want)
	}
}

// A caller waiting for the lane's lock must be able to abandon that wait the
// moment its OWN context is cancelled, instead of blocking until the lane
// frees up (sync.Mutex.Lock cannot be cancelled; the channel-based lock can).
func TestTreasury_ExecuteRouteAbandonsLockWaitOnContextCancellation(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	// Long enough that the second call would clearly be waiting on the lock
	// (not coincidentally finishing first), short enough to keep the test fast.
	native.allowanceDelay = 150 * time.Millisecond

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _, _ = tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	}()
	// Give the first call time to acquire the lane's lock and enter the delay.
	time.Sleep(30 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the second call must not wait for the lock at all

	start := time.Now()
	_, attempted, err := tr.ExecuteRoute(ctx, lane, "USDC", big.NewInt(50_000000), validRoute())
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) || attempted {
		t.Fatalf("attempted=%v err=%v, want context.Canceled and attempted=false", attempted, err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("second call took %v; should have returned promptly on ctx cancellation instead of waiting ~%v for the lock",
			elapsed, native.allowanceDelay)
	}

	<-firstDone // let the first leg finish before asserting on shared fake state
	if signer.calls() != 2 {
		t.Fatalf("only the first leg (approve+route) should have broadcast, got %d calls", signer.calls())
	}
}

// ── TransferToken ────────────────────────────────────────────────────────

func TestTreasury_TransferTokenBroadcastsAnExactTransfer(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	hash, attempted, err := tr.TransferToken(context.Background(), lane, "USDC", treasuryTestRecipient, big.NewInt(42_000000))
	if err != nil || !attempted || hash != "0xsponsored" { // fakeRelaySigner's canned hash, see newSponsorRig
		t.Fatalf("hash=%s attempted=%v err=%v", hash, attempted, err)
	}
	_, _, transferTo, transferAmt := native.transferCall()
	if transferTo != treasuryTestRecipient || transferAmt.Cmp(big.NewInt(42_000000)) != 0 {
		t.Fatalf("transfer went to %q for %v, want %s for 42000000", transferTo, transferAmt, treasuryTestRecipient)
	}
	if signer.calls() != 1 {
		t.Fatalf("broadcasts = %d, want exactly one", signer.calls())
	}
}

// C3: an empty or malformed recipient must never reach common.HexToAddress,
// which would silently coerce it to a zero/truncated address.
func TestTreasury_TransferTokenRefusesInvalidRecipient(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	for _, bad := range []string{"", "0xRecipient", "not-an-address"} {
		_, attempted, err := tr.TransferToken(context.Background(), lane, "USDC", bad, big.NewInt(1))
		if !errors.Is(err, ErrTreasuryInvalidRecipient) || attempted {
			t.Fatalf("to=%q: attempted=%v err=%v, want ErrTreasuryInvalidRecipient and attempted=false", bad, attempted, err)
		}
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
	if _, _, to, _ := native.transferCall(); to != "" {
		t.Fatalf("PrepareERC20Transfer should never have been called, got to=%q", to)
	}
}

func TestTreasury_TransferTokenRefusedWhenKilled(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	tr.SetKillSwitch(true)
	_, attempted, err := tr.TransferToken(context.Background(), lane, "USDC", treasuryTestRecipient, big.NewInt(1))
	if !errors.Is(err, ErrTreasuryKilled) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryKilled and attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

func TestTreasury_TransferTokenRefusedBelowFloor(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	lane.Chain.(*fakeSponsorChain).balances[lane.Signer.Address()] = big.NewInt(1) // below the 1_000 wei floor
	_, attempted, err := tr.TransferToken(context.Background(), lane, "USDC", treasuryTestRecipient, big.NewInt(1))
	if !errors.Is(err, ErrRelayerUnderfunded) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrRelayerUnderfunded and attempted=false", attempted, err)
	}
}

func TestTreasury_TransferTokenRefusesUnknownLane(t *testing.T) {
	tr := newTestTreasury(t)
	_, attempted, err := tr.TransferToken(context.Background(), tr.Lane("nowhere"), "USDC", treasuryTestRecipient, big.NewInt(1))
	if !errors.Is(err, ErrSponsorNotAvailable) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrSponsorNotAvailable and attempted=false", attempted, err)
	}
}

func TestTreasury_TransferTokenBuildFailure(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.transferErr = errors.New("gas price unavailable")
	_, attempted, err := tr.TransferToken(context.Background(), lane, "USDC", treasuryTestRecipient, big.NewInt(1))
	if err == nil || attempted {
		t.Fatalf("attempted=%v err=%v, want a plain error with attempted=false", attempted, err)
	}
	if signer.calls() != 0 {
		t.Fatalf("nothing should have been broadcast, got %d calls", signer.calls())
	}
}

// ── FloatBalances / Enabled / Address ────────────────────────────────────

func TestTreasury_FloatBalancesMatchesSponsorFloatShape(t *testing.T) {
	tr := newTestTreasury(t)
	floats, err := tr.FloatBalances(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(floats) != 1 || floats[0].Chain != "base" {
		t.Fatalf("floats = %+v", floats)
	}
	if floats[0].FloorWei == nil || floats[0].FloorWei.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("floor = %v, want the lane guard's own floor", floats[0].FloorWei)
	}
}

// One chain's RPC failing must not hide another chain's reading.
func TestTreasury_FloatBalancesCarriesOnPastOneBadRPC(t *testing.T) {
	rigA, _ := newSponsorRig("base", "USDC")
	rigB, _ := newSponsorRig("bsc", "USDT")
	rigB.chain.balanceErr = errors.New("rpc down")
	laneA := &TreasuryLane{SponsorLane: rigA.lane, ChainID: 8453}
	laneB := &TreasuryLane{SponsorLane: rigB.lane, ChainID: 56}
	tr := NewTreasury(map[string]*TreasuryLane{"base": laneA, "bsc": laneB}, TreasuryCaps{}, nil)

	floats, err := tr.FloatBalances(context.Background())
	if err == nil {
		t.Fatalf("want the bsc RPC failure surfaced in the joined error")
	}
	if len(floats) != 1 || floats[0].Chain != "base" {
		t.Fatalf("want the base lane's reading to survive the bsc failure, got %+v", floats)
	}
}

func TestTreasury_EnabledReflectsLaneCount(t *testing.T) {
	tr := newTestTreasury(t)
	if !tr.Enabled() {
		t.Fatalf("want enabled with one lane")
	}
	empty := NewTreasury(map[string]*TreasuryLane{}, TreasuryCaps{}, nil)
	if empty.Enabled() {
		t.Fatalf("want disabled with no lanes")
	}
}

// I9: Address() is deterministic (not dependent on map iteration order) and
// reflects every lane's signer agreeing on the same address.
func TestTreasury_AddressDeterministicAcrossLanes(t *testing.T) {
	rigA, _ := newSponsorRig("base", "USDC")
	rigB, _ := newSponsorRig("bsc", "USDT")
	laneA := &TreasuryLane{SponsorLane: rigA.lane, ChainID: 8453}
	laneB := &TreasuryLane{SponsorLane: rigB.lane, ChainID: 56}
	tr := NewTreasury(map[string]*TreasuryLane{"base": laneA, "bsc": laneB}, TreasuryCaps{}, nil)
	if tr.Address() != sponsorTestSigner {
		t.Fatalf("Address() = %q, want %q", tr.Address(), sponsorTestSigner)
	}
}

// I9: lanes whose signers disagree fail closed to an empty Address(), rather
// than guessing which one is right.
func TestTreasury_AddressEmptyOnSignerMismatch(t *testing.T) {
	rigA, _ := newSponsorRig("base", "USDC")
	rigB, _ := newSponsorRig("bsc", "USDT")
	rigB.signer.addr = "0x000000000000000000000000000000000000dEaD"
	laneA := &TreasuryLane{SponsorLane: rigA.lane, ChainID: 8453}
	laneB := &TreasuryLane{SponsorLane: rigB.lane, ChainID: 56}
	tr := NewTreasury(map[string]*TreasuryLane{"base": laneA, "bsc": laneB}, TreasuryCaps{}, nil)
	if tr.Address() != "" {
		t.Fatalf("want empty Address() on signer mismatch, got %q", tr.Address())
	}
}

// I9: NewTreasury normalises lane keys to lower case, since Lane() lowercases
// its lookup.
func TestTreasury_NewTreasuryNormalizesLaneKeyCase(t *testing.T) {
	rig, _ := newSponsorRig("base", "USDC")
	lane := &TreasuryLane{SponsorLane: rig.lane, ChainID: 8453, Routers: treasuryTestRouters}
	tr := NewTreasury(map[string]*TreasuryLane{"BASE": lane}, TreasuryCaps{}, nil)
	if tr.Lane("base") == nil {
		t.Fatalf("want NewTreasury to normalise lane keys to lower case")
	}
}

// ── router allowlist ────────────────────────────────────────────────────
//
// route.To is the contract the treasury's own EOA is about to call with
// calldata the same untrusted response chose. These four tests are the only
// thing standing between a bad /quote answer and the chain float.

func TestTreasury_ExecuteRouteRefusesToOutsideTheAllowlist(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	// The drain: call the token contract directly. Everything else about
	// this route is well-formed — zero value, valid hex, unexpired.
	route.To = treasuryTestToken
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute, got %v", err)
	}
	if attempted {
		t.Fatal("nothing may be broadcast for a refused route")
	}
	if signer.calls() != 0 {
		t.Fatalf("signer was used %d times for a refused route", signer.calls())
	}
	if _, _, spender, _ := native.approveCall(); spender != "" {
		t.Fatalf("an allowance was granted to %q before the route was validated", spender)
	}
}

func TestTreasury_ExecuteRouteRefusesSpenderOutsideTheAllowlist(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	route := validRoute()
	// To is fine; the approval spender is not. Approving an attacker for the
	// leg amount is a smaller loss than a full drain, but it is still a loss.
	route.ApprovalAddress = treasuryTestToken
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute, got %v", err)
	}
	if attempted || signer.calls() != 0 {
		t.Fatalf("attempted=%v calls=%d, want nothing signed", attempted, signer.calls())
	}
	if _, _, spender, _ := native.approveCall(); spender != "" {
		t.Fatalf("allowance granted to %q despite the refusal", spender)
	}
}

// A lane whose allowlist was never configured must refuse EVERY route, not
// accept every route: the fail-closed direction is the whole point.
func TestTreasury_ExecuteRouteRefusesWhenAllowlistIsEmpty(t *testing.T) {
	rig, _ := newSponsorRig("base", "USDC")
	lane := &TreasuryLane{SponsorLane: rig.lane, ChainID: 8453} // no Routers
	tr := NewTreasury(map[string]*TreasuryLane{"base": lane},
		TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000},
		func() time.Time { return sponsorTestNow })
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), validRoute())
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute for an unconfigured allowlist, got %v", err)
	}
	if attempted {
		t.Fatal("nothing may be broadcast on a lane with no allowlist")
	}
}

// EIP-55 checksum casing is cosmetic. Comparing as strings would refuse the
// very address we allowlisted, turning every conversion into a false alarm.
func TestTreasury_ExecuteRouteAcceptsDifferentlyCasedAllowlistedRouter(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	route := validRoute()
	route.To = strings.ToLower(treasuryTestRouter)
	route.ApprovalAddress = strings.ToUpper(strings.TrimPrefix(treasuryTestRouter, "0x"))
	route.ApprovalAddress = "0x" + route.ApprovalAddress
	if _, _, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route); err != nil {
		t.Fatalf("case-different form of an allowlisted router refused: %v", err)
	}
}
