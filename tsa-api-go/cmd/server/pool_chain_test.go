package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestBuildPoolChain_DormantWithoutAPoolRow(t *testing.T) {
	if g := buildPoolChain(testConfig(t), nil, nil, nil); g != nil {
		t.Fatal("no pool row, no gates")
	}
}

func TestRegisterPoolJobs_NilRegistersNothing(t *testing.T) {
	s := jobs.NewScheduler()
	registerPoolJobs(s, nil)
	if s.Count() != 0 {
		t.Fatalf("registered %d", s.Count())
	}
}

func TestRegisterPoolJobs_ProbeAndReleaseFloat(t *testing.T) {
	s := jobs.NewScheduler()
	registerPoolJobs(s, services.NewPoolChainGates(services.PoolChainGatesConfig{ChainKey: "bsc"}))
	if s.Count() != 2 {
		t.Fatalf("registered %d, want the gate re-check and the release-gas monitor", s.Count())
	}
}

// Signer consolidation (2026-10-13): the release key is the OPERATOR, which
// the IT operator and gas sponsor may share (one KeyLane per key and chain
// makes that nonce-safe). Sharing it with the relayer (its own key, owner
// decision), the TREASURY or the company wallet refuses to start.
func TestBuildReleaseSigner_RefusesAKeySharedAcrossSignerClasses(t *testing.T) {
	for other, refuse := range map[string]bool{
		"IT_OPERATOR_PRIVATE_KEY": false, "RELAYER_PRIVATE_KEY": true, "RAMP_SPONSOR_PRIVATE_KEY": false,
		"RAMP_TREASURY_WALLET_PRIVATE_KEY": true, "RAMP_TREASURY_PRIVATE_KEY": true,
	} {
		cfg := testConfig(t)
		cfg.PMReleasePrivateKey = testKeyA
		switch other {
		case "IT_OPERATOR_PRIVATE_KEY":
			cfg.ITOperatorPrivateKey = testKeyA
		case "RELAYER_PRIVATE_KEY":
			cfg.RelayerPrivateKey = testKeyA
		case "RAMP_SPONSOR_PRIVATE_KEY":
			cfg.RampSponsorPrivateKey = testKeyA
		case "RAMP_TREASURY_WALLET_PRIVATE_KEY":
			cfg.RampTreasuryWalletPrivateKey = testKeyA
		case "RAMP_TREASURY_PRIVATE_KEY":
			cfg.RampTreasuryPrivateKey = testKeyA
		}
		if got := refusesToStart(t, func() { buildReleaseSigner(cfg, nil) }); got != refuse {
			t.Fatalf("release key shared with %s: refused=%v, want %v", other, got, refuse)
		}
	}
}

func TestBuildReleaseSigner_DistinctKeysDoNotRefuse(t *testing.T) {
	cfg := testConfig(t)
	cfg.PMReleasePrivateKey = testKeyA
	cfg.ITOperatorPrivateKey = testKeyB
	if refusesToStart(t, func() { buildReleaseSigner(cfg, nil) }) {
		t.Fatal("distinct keys must not refuse")
	}
}

// Review I2: no pool row hands out a nil interface, never a typed nil.
func TestBuildPoolChain_DormantIsANilInterface(t *testing.T) {
	var g services.PoolGate = buildPoolChain(testConfig(t), nil, nil, nil)
	if g != nil {
		t.Fatalf("dormant gates = %#v, want a nil interface", g)
	}
}

// Review I2: a pool row with no RPC client at boot gets closed gates whose
// re-check job still runs (and re-alerts), rather than none at all.
func TestBuildPoolChain_NoClientIsClosedAndStillReChecked(t *testing.T) {
	g := buildPoolChain(poolTestConfig(t, "testnet"), nil, nil, nil)
	if g == nil {
		t.Fatal("a pool row must get gates even without a client")
	}
	if st := g.Status(); g.Open() || st.Open || len(st.Failures) == 0 || !strings.Contains(st.Failures[0], "no chain client") {
		t.Fatalf("status = %+v, want closed on no chain client", st)
	}
	if g.ReleaseGasOK(context.Background(), 1, false) {
		t.Fatal("no client: release gas must not read as OK")
	}
	s := jobs.NewScheduler()
	registerPoolJobs(s, g)
	if s.Count() != 2 {
		t.Fatalf("registered %d, want the gate re-check and the release-gas monitor", s.Count())
	}
}

// The release key's lane on the pool chain is the registry's: a sponsor lane
// on the same key and chain shares its lock and nonce floor.
func TestBuildReleaseSigner_TakesItsLanesFromTheRegistry(t *testing.T) {
	cfg := poolTestConfig(t, "mainnet")
	cfg.PMReleasePrivateKey = testKeyA
	bs := services.NewBlockchainService(cfg)
	crs, ok := buildReleaseSigner(cfg, bs).(*services.ChainReleaseSigner)
	if !ok || crs.Lane(56) == nil {
		t.Fatalf("release signer = %#v, want a lane on chain 56", crs)
	}
	key, _ := crypto.HexToECDSA(testKeyA)
	if l := buildSponsorLaneOn(bs, "mainnet", "bsc", "gas_sponsor", key, "ramp_sponsor_bsc", services.SponsorLimits{}, nil); l.KeyLane != crs.Lane(56) {
		t.Fatal("the release key and a sponsor lane on the same key and chain must share one KeyLane")
	}
}
