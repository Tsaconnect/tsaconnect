package main

import (
	"context"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestBuildPoolChain_DormantWithoutAPoolRow(t *testing.T) {
	if g := buildPoolChain(testConfig(t), nil, nil, nil, nil); g != nil {
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

// Review I8 / S8 / S39: the per-chain lock covers every use of the release key
// only if no other signer (with its own lock) holds the same key. The
// controller confirmed prod's PM release, IT operator and relayer keys differ.
func TestBuildReleaseSigner_RefusesAKeySharedWithAnotherSigner(t *testing.T) {
	for _, other := range []string{"IT_OPERATOR_PRIVATE_KEY", "RELAYER_PRIVATE_KEY", "RAMP_SPONSOR_PRIVATE_KEY",
		"RAMP_TREASURY_WALLET_PRIVATE_KEY", "RAMP_TREASURY_PRIVATE_KEY"} {
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
		if !refusesToStart(t, func() { buildReleaseSigner(cfg, nil) }) {
			t.Fatalf("a release key shared with %s must refuse to start", other)
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

func TestCompanyPMCheck_RefusesUnsetID(t *testing.T) {
	cfg := testConfig(t)
	if err := companyPMCheck(cfg)(context.Background()); err == nil {
		t.Fatal("IT_PM_ID unset must fail gate 7")
	}
}
