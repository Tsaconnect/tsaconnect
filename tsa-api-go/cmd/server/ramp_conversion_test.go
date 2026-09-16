package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// testKeyA and testKeyB are two distinct, well-formed (but obviously fake)
// secp256k1 private keys — 64 hex chars (32 bytes), each well under the
// curve order — used across these boot-wiring tests.
const (
	testKeyA = "1111111111111111111111111111111111111111111111111111111111111111"
	testKeyB = "2222222222222222222222222222222222222222222222222222222222222222"
)

// testConfig returns a minimal, non-production Config safe to mutate per
// test — every field these tests touch is set explicitly by the caller.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{Env: "test"}
}

// refusesToStart runs fn and reports whether it triggered a boot-time fatal
// refusal. It hooks the same `fatalf` package-level indirection production
// code calls instead of log.Fatalf, so this proves the real refusal path
// fired rather than merely that fn returned some error.
//
// NOT safe under t.Parallel() — see fatalf's own doc comment in
// ramp_sponsorship.go.
func refusesToStart(t *testing.T, fn func()) bool {
	t.Helper()
	orig := fatalf
	defer func() { fatalf = orig }()
	triggered := false
	fatalf = func(format string, args ...any) {
		triggered = true
		panic("refusesToStart: boot refused")
	}
	func() {
		defer func() { _ = recover() }()
		fn()
	}()
	return triggered
}

// Same key as the ramp sponsor: the treasury holds user funds, so sharing
// its key with a hot signer is the one thing that must never boot.
func TestBuildRampConversion_RefusesAKeySharedWithAnotherSigner(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampGasSponsorshipEnabled = true
	cfg.RampSponsorPrivateKey = testKeyA
	cfg.RampConversionBuyEnabled = true
	cfg.RampTreasuryPrivateKey = testKeyA
	if !refusesToStart(t, func() { buildRampConversion(cfg, nil, nil, nil) }) {
		t.Fatal("a shared treasury key must refuse to start")
	}
}

// The mutual check works from the sponsor's side too: a sponsor key equal to
// the treasury key must refuse to start from buildRampSponsorship, not only
// from buildRampConversion's own side.
func TestBuildRampSponsorship_RefusesAKeySharedWithTheTreasury(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampGasSponsorshipEnabled = true
	cfg.RampSponsorPrivateKey = testKeyA
	cfg.RampSponsorChains = []string{"base"}
	cfg.RampTreasuryPrivateKey = testKeyA
	// Zero-value literals: buildRampSponsorship only needs non-nil pointers
	// to get past its own "orders == nil || chains == nil" dormancy check
	// before it ever reaches the collision check this test is aimed at.
	chains := &services.BlockchainService{}
	orders := &services.PaycrestOrderService{}
	if !refusesToStart(t, func() { buildRampSponsorship(cfg, chains, orders) }) {
		t.Fatal("a treasury key shared with the sponsor must refuse to start")
	}
}

func TestBuildRampConversion_DormantWithoutTheFlag(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = false
	cfg.RampTreasuryPrivateKey = testKeyB
	treasury, orch := buildRampConversion(cfg, nil, nil, nil)
	if treasury != nil || orch != nil {
		t.Fatal("off means dormant")
	}
}

// The dormancy guarantee that actually matters at boot lives in
// registerRampConversionWorkers' nil check (main() calls it unconditionally
// now), not in buildRampConversion's own return values — pin that directly
// rather than only inferring it from a nil *ConversionOrchestrator.
func TestBuildRampConversion_DormantWithoutTheFlagRegistersNoWorkers(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = false
	cfg.RampTreasuryPrivateKey = testKeyB
	treasury, orch := buildRampConversion(cfg, nil, nil, nil)

	scheduler := jobs.NewScheduler()
	registerRampConversionWorkers(scheduler, treasury, orch)
	if got := scheduler.Count(); got != 0 {
		t.Fatalf("flag off must register zero workers, got %d", got)
	}
}

func TestRegisterRampConversionWorkers_NilOrchestratorRegistersNothing(t *testing.T) {
	scheduler := jobs.NewScheduler()
	registerRampConversionWorkers(scheduler, nil, nil)
	if got := scheduler.Count(); got != 0 {
		t.Fatalf("a nil orchestrator must register zero workers, got %d", got)
	}
}

// Treasury.CheckCaps treats a cap <= 0 as UNLIMITED (its own, already-tested
// convention), so a boot-time value of 0 must refuse to build the treasury —
// otherwise RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER=0 would silently mean
// "no limit" instead of "no orders".
func TestBuildRampConversion_RefusesAZeroPerOrderCap(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = true
	cfg.RampTreasuryPrivateKey = testKeyB
	cfg.RampTreasuryMaxInflightUSDPerOrder = 0
	cfg.RampTreasuryMaxInflightUSDPerChain = 10000
	treasury, orch := buildRampConversion(cfg, nil, nil, nil)
	if treasury != nil || orch != nil {
		t.Fatal("a zero per-order cap must refuse to build the treasury, not mean unlimited")
	}
}

// Same as above for the per-chain cap, and with a negative value rather than
// zero, so both caps and both "not a valid positive number" shapes are
// covered.
func TestBuildRampConversion_RefusesANegativePerChainCap(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = true
	cfg.RampTreasuryPrivateKey = testKeyB
	cfg.RampTreasuryMaxInflightUSDPerOrder = 2000
	cfg.RampTreasuryMaxInflightUSDPerChain = -1
	treasury, orch := buildRampConversion(cfg, nil, nil, nil)
	if treasury != nil || orch != nil {
		t.Fatal("a negative per-chain cap must refuse to build the treasury, not mean unlimited")
	}
}

// Quotable chains and treasury lanes are independent sets:
// RampConversionMinUSDByChain is populated for every configured chain, but
// `lanes` (missing floor, no chain client, ...) can be a strict subset. The
// quote service must only ever be told about chains that actually got a
// lane.
func TestRampQuotableConversionMinimums_FiltersToChainsWithALane(t *testing.T) {
	mins := map[string]float64{"base": 1, "ethereum": 5, "linea": 25}
	// Zero-value TreasuryLane literals: this is a pure map-filtering
	// function, so the lane's own contents are irrelevant — only its
	// presence as a key is.
	lanes := map[string]*services.TreasuryLane{"base": {}}

	got := rampQuotableConversionMinimums(mins, lanes)

	if len(got) != 1 {
		t.Fatalf("expected exactly the chains with a lane, got %v", got)
	}
	if got["base"] != 1 {
		t.Fatalf("base minimum = %v, want 1", got["base"])
	}
	if _, ok := got["ethereum"]; ok {
		t.Fatal("ethereum has no lane and must not be quotable")
	}
	if _, ok := got["linea"]; ok {
		t.Fatal("linea has no lane and must not be quotable")
	}
}
