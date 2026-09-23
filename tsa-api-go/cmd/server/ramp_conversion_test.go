package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

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
	if !refusesToStart(t, func() { buildRampConversion(cfg, nil, nil, nil, nil) }) {
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
	treasury, orch := buildRampConversion(cfg, nil, nil, nil, nil)
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
	treasury, orch := buildRampConversion(cfg, nil, nil, nil, nil)

	scheduler := jobs.NewScheduler()
	registerRampConversionWorkers(scheduler, treasury, orch, 500)
	if got := scheduler.Count(); got != 0 {
		t.Fatalf("flag off must register zero workers, got %d", got)
	}
}

func TestRegisterRampConversionWorkers_NilOrchestratorRegistersNothing(t *testing.T) {
	scheduler := jobs.NewScheduler()
	registerRampConversionWorkers(scheduler, nil, nil, 500)
	if got := scheduler.Count(); got != 0 {
		t.Fatalf("a nil orchestrator must register zero workers, got %d", got)
	}
}

// Treasury.CheckCaps treats a cap <= 0 as UNLIMITED (its own, already-tested
// convention), so rampTreasuryCaps — the boot-time gate in front of it — must
// refuse a zero per-order cap rather than let it through as "no limit".
//
// This tests rampTreasuryCaps DIRECTLY rather than through
// buildRampConversion: a version of this test that called
// buildRampConversion(cfg, nil, nil, nil, nil) and asserted a nil/nil return would
// be vacuous, because the pre-existing `if chains == nil` guard immediately
// after the caps check returns that exact same (nil, nil) regardless of
// whether the caps check ever ran. Asserting on rampTreasuryCaps' own return
// value is what actually proves the cap refusal fired.
func TestRampTreasuryCaps_RefusesAZeroPerOrderCap(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryMaxInflightUSDPerOrder = 0
	cfg.RampTreasuryMaxInflightUSDPerChain = 10000
	if caps, ok := rampTreasuryCaps(cfg); ok {
		t.Fatalf("a zero per-order cap must not validate, got caps=%+v ok=%v", caps, ok)
	}
}

// Same as above for the per-chain cap, and with a negative value rather than
// zero, so both caps and both "not a valid positive number" shapes are
// covered.
func TestRampTreasuryCaps_RefusesANegativePerChainCap(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryMaxInflightUSDPerOrder = 2000
	cfg.RampTreasuryMaxInflightUSDPerChain = -1
	if caps, ok := rampTreasuryCaps(cfg); ok {
		t.Fatalf("a negative per-chain cap must not validate, got caps=%+v ok=%v", caps, ok)
	}
}

// The positive case, so a future change to the <= 0 boundary (e.g. an
// off-by-one to < 0) is caught from both directions.
func TestRampTreasuryCaps_AcceptsBothValidCaps(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryMaxInflightUSDPerOrder = 2000
	cfg.RampTreasuryMaxInflightUSDPerChain = 10000
	caps, ok := rampTreasuryCaps(cfg)
	if !ok {
		t.Fatal("two valid positive caps must validate")
	}
	if caps.MaxInFlightUSDPerOrder != 2000 || caps.MaxInFlightUSDPerChain != 10000 {
		t.Fatalf("caps = %+v, want order=2000 chain=10000", caps)
	}
}

// The former rampQuotableConversionMinimums map-filter is gone: it never
// actually gated anything (MeetsMinimumOrder falls back to the DEFAULT
// minimum for a chain absent from the map, making an unlaned chain MORE
// quotable, not less). The real gate now lives inside
// PaycrestQuoteService.ConvertedVenues itself, wired via SetTreasuryLanes —
// see TestQuote_RefusesAVenueOnAChainWithNoTreasuryLane in
// internal/services/paycrest_quote_conversion_test.go, which asserts the
// actual observable behavior (the venue is not offered, and LiFi is never
// called), not an internal map's keys.

// A lane with no allowlist could never execute a route (ExecuteRoute refuses
// an empty one), so it must not be built — otherwise the chain is quotable
// but unexecutable.
func TestRampTreasuryLaneRouters_SkipsAnEmptyAllowlist(t *testing.T) {
	cfg := testConfig(t)
	if routers, reason := rampTreasuryLaneRouters(cfg, "bsc", nil); reason == "" {
		t.Fatalf("a missing allowlist must skip the lane, got routers=%v", routers)
	}
}

// A typo in RAMP_TREASURY_ROUTERS_<CHAIN> must fail at boot, not silently
// leave a lane that refuses every route at execution time.
func TestRampTreasuryLaneRouters_SkipsAMalformedEntry(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryRouters = map[string][]string{
		"bsc": {"0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE", "0x1231DEB6f5749EF6cE6943a275A1D3E7486F4Ea"},
	}
	if routers, reason := rampTreasuryLaneRouters(cfg, "bsc", nil); reason == "" {
		t.Fatalf("a malformed router address must skip the lane, got routers=%v", routers)
	}
}

func TestRampTreasuryLaneRouters_AcceptsValidAddresses(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryRouters = map[string][]string{"bsc": {"0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE"}}
	routers, reason := rampTreasuryLaneRouters(cfg, "bsc", nil)
	if reason != "" || len(routers) != 1 || routers[0] != common.HexToAddress("0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE") {
		t.Fatalf("routers=%v reason=%q, want the one valid router and no skip", routers, reason)
	}
}

// Allowlisting a token the lane holds would reopen the exact drain the list
// exists to close: `to` = that token, calldata = transfer(attacker, balance).
// Casing must not hide the match.
func TestRampTreasuryLaneRouters_SkipsALaneTokenInTheAllowlist(t *testing.T) {
	const bscUSDT = "0x55d398326f99059fF775485246999027B3197955"
	cfg := testConfig(t)
	cfg.RampTreasuryRouters = map[string][]string{
		"bsc": {"0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE", strings.ToLower(bscUSDT)},
	}
	tokens := map[string]string{"USDT": bscUSDT}
	if routers, reason := rampTreasuryLaneRouters(cfg, "bsc", tokens); reason == "" {
		t.Fatalf("a lane token in the allowlist must skip the lane, got routers=%v", routers)
	}
}

func TestRampTreasuryLaneRouters_SkipsTheZeroAddress(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryRouters = map[string][]string{"bsc": {"0x0000000000000000000000000000000000000000"}}
	if routers, reason := rampTreasuryLaneRouters(cfg, "bsc", nil); reason == "" {
		t.Fatalf("the zero address must skip the lane, got routers=%v", routers)
	}
}

// testChainIDs gives testBlockchainService a stable numeric chain ID per
// chain key, distinct enough that a client mixing them up would misroute.
var testChainIDs = map[string]int64{"base": 8453, "bsc": 56}

// testBlockchainService builds a *services.BlockchainService whose clients
// point at a throwaway httptest JSON-RPC server, one per requested chain
// key, on the mainnet network. Dialing an httptest server does no I/O by
// itself (ethclient.DialContext only wires the transport), so this is safe
// to use in any test that never actually calls the RPC.
func testBlockchainService(t *testing.T, keys ...string) *services.BlockchainService {
	t.Helper()
	chains := map[string]config.ChainConfig{}
	for _, key := range keys {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		id, ok := testChainIDs[key]
		if !ok {
			t.Fatalf("testBlockchainService: no chain id fixture for %q", key)
		}
		chains[key] = config.ChainConfig{RPCURL: srv.URL, ChainID: id}
	}
	cfg := &config.Config{Networks: map[string]config.NetworkConfig{
		"mainnet": {Chains: chains},
	}}
	return services.NewBlockchainService(cfg)
}

// TestRampTransferVerifiers_CoverChainsWithoutALane pins the Task 4 fix: a
// converted buy's on-chain delivery proof must be available for every
// configured EVM chain, not only chains with a treasury lane — a lane-less
// chain can still be a converted-buy DESTINATION even though the treasury
// never sources funds from it directly.
func TestRampTransferVerifiers_CoverChainsWithoutALane(t *testing.T) {
	chains := testBlockchainService(t, "base", "bsc") // clients for both; see below
	got := rampTransferVerifiers(chains, []string{"base", "bsc", "nowhere"})
	if got["bsc"] == nil || got["base"] == nil {
		t.Fatalf("verifiers = %v; every chain with a client needs one", got)
	}
	if _, ok := got["nowhere"]; ok {
		t.Fatal("a chain with no client gets no verifier")
	}
}

// The sell quote's allowlist is the treasury's own, by chain id, through the
// treasury's own validation: a chain whose list the treasury would refuse is
// refused for a sell too (no entry at all, which the quoter treats as "no
// sells from here"), and a chain the registry cannot name gets nothing.
func TestRampSellRouters_ReusesTheTreasuryAllowlistByChainID(t *testing.T) {
	const diamond = "0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE"
	cfg := testConfig(t)
	cfg.RampTreasuryRouters = map[string][]string{
		"bsc":     {diamond},
		"base":    {"0x1231DEB6f5749EF6cE6943a275A1D3E7486F4Ea"}, // a typo: skipped, not trusted
		"nowhere": {diamond},
	}
	got := rampSellRouters(cfg, testBlockchainService(t, "base", "bsc"))
	if len(got) != 1 || len(got[56]) != 1 || got[56][0] != common.HexToAddress(diamond) {
		t.Fatalf("routers = %v, want only bsc (56) with the diamond", got)
	}
}
