package services

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
	"time"
)

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
	lane := &TreasuryLane{SponsorLane: rig.lane, ChainID: 8453}
	tr := NewTreasury(map[string]*TreasuryLane{"base": lane}, TreasuryCaps{
		MaxInFlightUSDPerOrder: 2000,
		MaxInFlightUSDPerChain: 10000,
	}, func() time.Time { return sponsorTestNow })
	return tr, lane, rig.native, rig.signer
}

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

// A route is signed exactly as LiFi returned it: same target, same calldata.
func TestTreasury_ExecuteRouteSignsTheRouteAsGiven(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	route := &LiFiRoute{To: "0xRouter", Data: "0xdeadbeef", Value: "0x0", GasLimit: 250_000, ApprovalAddress: "0xAppr"}
	hash, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err != nil || !attempted || hash != "0xsponsored" { // fakeRelaySigner's canned hash, see newSponsorRig
		t.Fatalf("hash=%s attempted=%v err=%v", hash, attempted, err)
	}

	builder := lane.Builder.(*fakeSponsorBuilder)
	if builder.to != "0xRouter" || hex.EncodeToString(builder.data) != "deadbeef" {
		t.Fatalf("route rebuilt instead of signed as given: to=%s data=%x", builder.to, builder.data)
	}
	if builder.gasLimit != 250_000 {
		t.Fatalf("route's own gas limit not honoured: got %d", builder.gasLimit)
	}
	if native.approveSpender != "0xAppr" {
		t.Fatalf("approval went to %q, want the route's approvalAddress", native.approveSpender)
	}
	if native.approveAmount == nil || native.approveAmount.Cmp(big.NewInt(100_000000)) != 0 {
		t.Fatalf("approval amount = %v, want the exact leg amount (never MaxUint256)", native.approveAmount)
	}
	if signer.broadcastCalls != 2 { // approve + route
		t.Fatalf("broadcasts = %d, want approve then route", signer.broadcastCalls)
	}
}

// A held allowance already covering the amount skips the approve step
// entirely — one broadcast, not two.
func TestTreasury_ExecuteRouteSkipsApproveWhenAllowanceCovers(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	native.allowance = big.NewInt(1_000_000000)
	route := &LiFiRoute{To: "0xRouter", Data: "0xdeadbeef", Value: "0x0", GasLimit: 250_000, ApprovalAddress: "0xAppr"}
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err != nil || !attempted {
		t.Fatalf("attempted=%v err=%v", attempted, err)
	}
	if native.approveAmount != nil {
		t.Fatalf("approve should have been skipped, got amount %v", native.approveAmount)
	}
	if signer.broadcastCalls != 1 {
		t.Fatalf("broadcasts = %d, want just the route", signer.broadcastCalls)
	}
}

// The kill switch refuses ExecuteRoute/TransferToken before anything is
// built, and the floor check runs before either — a half-sent conversion is
// worse than one that never started.
func TestTreasury_ExecuteRouteRefusedWhenKilled(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	tr.SetKillSwitch(true)
	route := &LiFiRoute{To: "0xRouter", Data: "0xdeadbeef", GasLimit: 250_000}
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100), route)
	if !errors.Is(err, ErrTreasuryKilled) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrTreasuryKilled and attempted=false", attempted, err)
	}
}

func TestTreasury_ExecuteRouteRefusedBelowFloor(t *testing.T) {
	tr, lane, _, _ := newTestTreasuryWithFakes(t)
	lane.Chain.(*fakeSponsorChain).balances[lane.Signer.Address()] = big.NewInt(1) // below the 1_000 wei floor
	route := &LiFiRoute{To: "0xRouter", Data: "0xdeadbeef", GasLimit: 250_000}
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100), route)
	if !errors.Is(err, ErrRelayerUnderfunded) || attempted {
		t.Fatalf("attempted=%v err=%v, want ErrRelayerUnderfunded and attempted=false", attempted, err)
	}
}

// A broadcast whose outcome is unknown (build succeeded, SignAndBroadcast
// errored) must be surfaced as attempted=true so the caller keeps the leg on
// the uncertain path instead of assuming it never left this process.
func TestTreasury_ExecuteRouteSurfacesUncertainBroadcast(t *testing.T) {
	tr, lane, _, signer := newTestTreasuryWithFakes(t)
	signer.err = errors.New("rpc timeout")
	route := &LiFiRoute{To: "0xRouter", Data: "0xdeadbeef", GasLimit: 250_000, ApprovalAddress: "0xAppr"}
	_, attempted, err := tr.ExecuteRoute(context.Background(), lane, "USDC", big.NewInt(100_000000), route)
	if err == nil || !attempted {
		t.Fatalf("attempted=%v err=%v, want an error with attempted=true (approve itself is what times out)", attempted, err)
	}
}

func TestTreasury_TransferTokenBroadcastsAnExactTransfer(t *testing.T) {
	tr, lane, native, signer := newTestTreasuryWithFakes(t)
	hash, attempted, err := tr.TransferToken(context.Background(), lane, "USDC", "0xRecipient", big.NewInt(42_000000))
	if err != nil || !attempted || hash != "0xsponsored" { // fakeRelaySigner's canned hash, see newSponsorRig
		t.Fatalf("hash=%s attempted=%v err=%v", hash, attempted, err)
	}
	if native.transferTo != "0xRecipient" || native.transferAmt.Cmp(big.NewInt(42_000000)) != 0 {
		t.Fatalf("transfer went to %q for %v, want 0xRecipient for 42000000", native.transferTo, native.transferAmt)
	}
	if signer.broadcastCalls != 1 {
		t.Fatalf("broadcasts = %d, want exactly one", signer.broadcastCalls)
	}
}

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
