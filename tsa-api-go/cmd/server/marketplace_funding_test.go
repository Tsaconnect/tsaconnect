package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The funding is dormant, never a boot failure, whenever what it needs is
// missing: switched off, no Sonic mainnet client, or no chain registry.
func TestBuildMarketplaceFunding_DormantWithoutItsPieces(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{
			MarketplaceFundingEnabled: true, MarketplaceEscrowGasPerOrder: 400_000,
			Networks: map[string]config.NetworkConfig{"mainnet": {
				Chains:         map[string]config.ChainConfig{"sonic": {ChainID: 146}},
				TokenAddresses: map[string]string{"sonic:USDC": "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"},
			}},
		}
	}
	off := base()
	off.MarketplaceFundingEnabled = false
	if buildMarketplaceFunding(off, services.NewBlockchainService(off), nil, nil, nil, nil, nil) != nil {
		t.Fatal("MARKETPLACE_FUNDING=false must leave it off")
	}
	// No connected Sonic client (no RPC URL configured): nothing to prove or read on.
	if buildMarketplaceFunding(base(), services.NewBlockchainService(base()), nil, nil, nil, nil, nil) != nil {
		t.Fatal("no Sonic mainnet client must leave it dormant")
	}
	if buildMarketplaceFunding(base(), nil, nil, nil, nil, nil, nil) != nil {
		t.Fatal("no chain registry must leave it dormant")
	}
}

// The shared Sonic gas lane (P2P always, marketplace funding while its switch
// is on) stays dormant, never fatal, without its pieces.
func TestBuildSonicGasSponsorship_DormantWithoutItsPieces(t *testing.T) {
	cfg := &config.Config{
		RampSponsorPrivateKey:       "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318",
		MarketplaceSonicGasFloorWei: "5000000000000000000", MarketplaceSonicGasTopUpMaxWei: "500000000000000000",
		RampSponsorDailyGasBudget: "500000000",
	}
	chains := services.NewBlockchainService(cfg)
	cfg.RampSponsorPrivateKey = ""
	if buildSonicGasSponsorship(cfg, chains) != nil {
		t.Fatal("no sponsor key must leave the lane off")
	}
	cfg.RampSponsorPrivateKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	cfg.Env = "production"
	if buildSonicGasSponsorship(cfg, chains) != nil {
		t.Fatal("a raw sponsor key in production without ALLOW_RAW_SIGNER_KEYS must leave the lane off")
	}
	cfg.Env = ""
	cfg.MarketplaceSonicGasTopUpMaxWei = "5e17"
	if buildSonicGasSponsorship(cfg, chains) != nil {
		t.Fatal("a malformed ceiling must skip the lane, never run it unbounded")
	}
	cfg.MarketplaceSonicGasTopUpMaxWei = "500000000000000000"
	if buildSonicGasSponsorship(cfg, chains) != nil {
		t.Fatal("no sonic client must leave the lane off")
	}
}

// One Sonic lane on the sponsor key: the funding sees it only while
// MARKETPLACE_SONIC_GAS is on, exactly as when it built its own.
func TestMarketplaceSonicGas_GatedByItsSwitch(t *testing.T) {
	shared := &services.RampSponsorship{}
	if marketplaceSonicGas(&config.Config{MarketplaceSonicGasEnabled: false}, shared) != nil {
		t.Fatal("off must hide the shared lane from the funding")
	}
	if marketplaceSonicGas(&config.Config{MarketplaceSonicGasEnabled: true}, shared) != shared {
		t.Fatal("on must hand it the shared lane")
	}
	if marketplaceSonicGas(&config.Config{MarketplaceSonicGasEnabled: true}, nil) != nil {
		t.Fatal("on without a lane is still no lane")
	}
}
