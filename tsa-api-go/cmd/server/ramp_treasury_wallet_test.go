package main

import (
	"bytes"
	"errors"
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

func captureWalletLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &logs
}

// Each dormancy case must stop at its OWN branch, so every case asserts its
// own reason rather than just a nil return.
func TestBuildCompanyWallet_Dormancy(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *config.Config)
		chains *services.BlockchainService
		want   string
	}{
		{"no key", func(c *config.Config) { c.RampTreasuryWalletPrivateKey = "" }, nil, "not set"},
		{"bad key", func(c *config.Config) { c.RampTreasuryWalletPrivateKey = "not-hex" }, nil, "invalid RAMP_TREASURY_WALLET_PRIVATE_KEY"},
		{"no chain registry", func(c *config.Config) {}, nil, "no chain registry"},
		{"raw key refused in production", func(c *config.Config) { c.Env = "production" }, &services.BlockchainService{}, "refused in production"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureWalletLog(t)
			cfg := testConfig(t)
			cfg.RampTreasuryBuy = true
			cfg.RampTreasuryMaxUSD = "200"
			cfg.RampTreasuryWalletChains = []string{"base"}
			cfg.RampTreasuryWalletPrivateKey = testKeyA
			tc.mutate(cfg)
			if buildCompanyWallet(cfg, tc.chains) != nil {
				t.Fatal("must be dormant")
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("log = %q, want reason %q", logs.String(), tc.want)
			}
		})
	}
}

func TestBuildCompanyWallet_BadKeyLogDoesNotEchoTheParserError(t *testing.T) {
	logs := captureWalletLog(t)
	cfg := testConfig(t)
	cfg.RampTreasuryWalletPrivateKey = "zz-secret-ish"
	buildCompanyWallet(cfg, nil)
	if strings.Contains(logs.String(), "zz-secret-ish") || strings.Contains(logs.String(), "invalid byte") {
		t.Fatalf("log echoes the key material: %q", logs.String())
	}
}

func TestBuildCompanyWallet_InvalidMaxUSDIsDormant(t *testing.T) {
	for _, v := range []string{"abc", "0", "-5", ""} {
		t.Run(v, func(t *testing.T) {
			logs := captureWalletLog(t)
			cfg := testConfig(t)
			cfg.RampTreasuryWalletPrivateKey = testKeyA
			cfg.RampTreasuryMaxUSD = v
			if buildCompanyWallet(cfg, &services.BlockchainService{}) != nil {
				t.Fatal("an invalid max must leave the wallet dormant")
			}
			if !strings.Contains(logs.String(), "ramp_company_wallet_cap_invalid_total=1") {
				t.Fatalf("log = %q, want the cap_invalid ALERT", logs.String())
			}
		})
	}
}

// An RPC transport error carries the full URL, API key included.
func TestAlertReceiptsUnreadable_RedactsTheRPCKey(t *testing.T) {
	logs := captureWalletLog(t)
	alertReceiptsUnreadable("base", "base-mainnet.g.alchemy.com",
		errors.New(`Post "https://base-mainnet.g.alchemy.com/v2/SECRETKEY": dial tcp: timeout`))
	if strings.Contains(logs.String(), "SECRETKEY") || !strings.Contains(logs.String(), "receipts_unreadable") {
		t.Fatalf("log = %q", logs.String())
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
	mk := testConfig(t)
	mk.RampSponsorPrivateKey = testKeyA
	mk.RampTreasuryWalletPrivateKey = testKeyA
	if !refusesToStart(t, func() { buildSonicGasSponsorship(mk, &services.BlockchainService{}) }) {
		t.Fatal("the shared sonic gas sponsor must refuse the wallet's key")
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
