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

// The funding's Sonic gas lane is its own switch (never a RAMP_SPONSOR_CHAINS
// lane) and stays dormant, never fatal, without its pieces.
func TestBuildMarketplaceSonicGas_DormantWithoutItsPieces(t *testing.T) {
	cfg := &config.Config{
		MarketplaceSonicGasEnabled: false, RampSponsorPrivateKey: "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318",
		MarketplaceSonicGasFloorWei: "5000000000000000000", MarketplaceSonicGasTopUpMaxWei: "500000000000000000",
		RampSponsorDailyGasBudget: "500000000",
	}
	chains := services.NewBlockchainService(cfg)
	if buildMarketplaceSonicGas(cfg, chains) != nil {
		t.Fatal("MARKETPLACE_SONIC_GAS off must leave the lane off")
	}
	cfg.MarketplaceSonicGasEnabled = true
	cfg.RampSponsorPrivateKey = ""
	if buildMarketplaceSonicGas(cfg, chains) != nil {
		t.Fatal("no sponsor key must leave the lane off")
	}
	cfg.RampSponsorPrivateKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	cfg.MarketplaceSonicGasTopUpMaxWei = "5e17"
	if buildMarketplaceSonicGas(cfg, chains) != nil {
		t.Fatal("a malformed ceiling must skip the lane, never run it unbounded")
	}
	cfg.MarketplaceSonicGasTopUpMaxWei = "500000000000000000"
	if buildMarketplaceSonicGas(cfg, chains) != nil {
		t.Fatal("no sonic client must leave the lane off")
	}
}
