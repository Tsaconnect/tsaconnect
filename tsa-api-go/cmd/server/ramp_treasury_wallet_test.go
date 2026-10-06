package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Review I1: RAMP_TREASURY_BUY never makes the wallet dormant — with it off,
// the build goes on past the flag (here to the missing chain registry), so a
// wallet with open orders keeps settling them.
func TestBuildCompanyWallet_IgnoresTheProviderFlag(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	cfg := testConfig(t)
	cfg.RampTreasuryBuy = false
	cfg.RampTreasuryWalletPrivateKey = testKeyA
	cfg.RampTreasuryMaxUSD = "200"
	if buildCompanyWallet(cfg, nil) != nil {
		t.Fatal("no chain registry must be dormant")
	}
	if strings.Contains(logs.String(), "RAMP_TREASURY_BUY") || !strings.Contains(logs.String(), "no chain registry") {
		t.Fatalf("log = %q, want the build to ignore the flag", logs.String())
	}
}

func TestBuildCompanyWallet_Dormancy(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampTreasuryBuy = true
	cfg.RampTreasuryMaxUSD = "200"
	cfg.RampTreasuryWalletPrivateKey = ""
	if buildCompanyWallet(cfg, nil) != nil {
		t.Fatal("no key must be dormant")
	}
	cfg.RampTreasuryWalletPrivateKey = "not-hex"
	if buildCompanyWallet(cfg, nil) != nil {
		t.Fatal("a bad key must be dormant")
	}
	cfg.RampTreasuryWalletPrivateKey = testKeyA
	if buildCompanyWallet(cfg, nil) != nil {
		t.Fatal("no chain registry must be dormant")
	}
	prod := testConfig(t)
	prod.Env = "production"
	prod.RampTreasuryBuy = true
	prod.RampTreasuryMaxUSD = "200"
	prod.RampTreasuryWalletPrivateKey = testKeyA
	if buildCompanyWallet(prod, &services.BlockchainService{}) != nil {
		t.Fatal("a raw key refused in production must be dormant")
	}
}

// S1 / Decision D50: the wallet's key may equal no other hot key — fatal.
func TestBuildCompanyWallet_RefusesAKeySharedWithAnyOtherSigner(t *testing.T) {
	for name, set := range map[string]func(c *config.Config){
		"RELAYER_PRIVATE_KEY":       func(c *config.Config) { c.RelayerPrivateKey = testKeyA },
		"IT_OPERATOR_PRIVATE_KEY":   func(c *config.Config) { c.ITOperatorPrivateKey = testKeyA },
		"PM_RELEASE_PRIVATE_KEY":    func(c *config.Config) { c.PMReleasePrivateKey = testKeyA },
		"RAMP_SPONSOR_PRIVATE_KEY":  func(c *config.Config) { c.RampSponsorPrivateKey = testKeyA },
		"RAMP_TREASURY_PRIVATE_KEY": func(c *config.Config) { c.RampTreasuryPrivateKey = testKeyA },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.RampTreasuryBuy = true
			cfg.RampTreasuryWalletPrivateKey = testKeyA
			set(cfg)
			if !refusesToStart(t, func() { buildCompanyWallet(cfg, nil) }) {
				t.Fatalf("a wallet key equal to %s must refuse to start", name)
			}
		})
	}
	cfg := testConfig(t)
	cfg.RampTreasuryBuy = true
	cfg.RampTreasuryWalletPrivateKey = testKeyA
	cfg.RampTreasuryPrivateKey = testKeyB
	if refusesToStart(t, func() { buildCompanyWallet(cfg, nil) }) {
		t.Fatal("distinct keys must not refuse")
	}
}

// The other side of the same check: the conversion treasury and the sponsor
// refuse a key equal to the wallet's.
func TestOtherSigners_RefuseTheWalletKey(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = true
	cfg.RampTreasuryPrivateKey = testKeyA
	cfg.RampTreasuryWalletPrivateKey = testKeyA
	if !refusesToStart(t, func() { buildRampConversion(cfg, nil, nil, nil, nil) }) {
		t.Fatal("the conversion treasury must refuse the wallet's key")
	}
	sp := testConfig(t)
	sp.RampGasSponsorshipEnabled = true
	sp.RampSponsorPrivateKey = testKeyA
	sp.RampSponsorChains = []string{"base"}
	sp.RampTreasuryWalletPrivateKey = testKeyA
	if !refusesToStart(t, func() { buildRampSponsorship(sp, &services.BlockchainService{}, &services.PaycrestOrderService{}) }) {
		t.Fatal("the sponsor must refuse the wallet's key")
	}
}
