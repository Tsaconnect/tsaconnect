package main

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

const (
	testPoolEscrow  = "0x00000000000000000000000000000000000A11CE"
	testnetPoolUSDT = "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7"
)

func poolTestConfig(t *testing.T, network string) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.RampTreasuryPrivateKey = testKeyB
	cfg.RampTreasuryMaxInflightUSDPerOrder = 2000
	cfg.RampTreasuryMaxInflightUSDPerChain = 10000
	cfg.RampTreasuryNativeFloorWei = map[string]string{"bsc": "10000000000000000"}
	id := int64(97)
	if network == "mainnet" {
		id = 56
	}
	cfg.Networks = map[string]config.NetworkConfig{network: {
		// An http URL dials lazily: no RPC call happens while lanes are built.
		Chains:         map[string]config.ChainConfig{"bsc": {RPCURL: "http://127.0.0.1:1", ChainID: id}},
		TokenAddresses: map[string]string{"bsc:USDT": testnetPoolUSDT},
	}}
	cfg.P2PChains = map[int64]config.P2PChainConfig{id: {ChainID: id, ChainKey: "bsc", Network: network,
		EscrowAddress: testPoolEscrow, EIP712Version: "2", Purpose: config.P2PPurposePool, Assets: []string{"USDT"},
		Pool: &config.PoolChainConfig{FeeManager: "0x00000000000000000000000000000000000FEE00", Tokens: []string{"USDT"},
			ReleaseGasFloorWei: "10000000000000000", LogChunkBlocks: 2000}}}
	return cfg
}

// Spec §5 / I8: a testnet pool gets a router-less lane with no conversion flag.
func TestRampConversion_PoolBuildsLanesWithoutConversionBuy(t *testing.T) {
	cfg := poolTestConfig(t, "testnet")
	cfg.RampConversionBuyEnabled = false
	tr := poolTreasuryFor(cfg, services.NewBlockchainService(cfg), nil)
	if tr == nil {
		t.Fatal("the pool must get a treasury lane without RAMP_CONVERSION_BUY")
	}
	lane := tr.Lane("bsc")
	if lane == nil || lane.ChainID != 97 || len(lane.Routers) != 0 ||
		len(lane.EscrowCalls) != 1 || lane.EscrowCalls[0] != common.HexToAddress(testPoolEscrow) || lane.Tokens["USDT"] != testnetPoolUSDT {
		t.Fatalf("lane = %+v", lane)
	}
	key, _ := crypto.HexToECDSA(testKeyB)
	if tr.Address() != crypto.PubkeyToAddress(key.PublicKey).Hex() {
		t.Fatalf("treasury address = %s, want the RAMP_TREASURY_PRIVATE_KEY address", tr.Address())
	}
}

// P14: a mainnet pool shares the conversion treasury (one nonce sequence, R3).
func TestPoolTreasury_MainnetPoolReusesTheConversionTreasury(t *testing.T) {
	cfg := poolTestConfig(t, "mainnet")
	key, _ := crypto.HexToECDSA(testKeyB)
	conv := services.NewTreasury(map[string]*services.TreasuryLane{"bsc": {
		SponsorLane: &services.SponsorLane{ChainKey: "bsc", Signer: services.NewLocalKeyReleaseSigner(key, nil)}, ChainID: 56,
	}}, services.TreasuryCaps{MaxInFlightUSDPerOrder: 2000}, nil)
	if got := poolTreasuryFor(cfg, services.NewBlockchainService(cfg), conv); got != conv {
		t.Fatal("a mainnet pool must use the conversion treasury itself")
	}
	if l := conv.Lane("bsc"); len(l.EscrowCalls) != 1 || l.EscrowCalls[0] != common.HexToAddress(testPoolEscrow) {
		t.Fatalf("escrow calls = %v", l.EscrowCalls)
	}
}

func TestPoolTreasury_TestnetPoolWithConversionOnIsRefused(t *testing.T) {
	cfg := poolTestConfig(t, "testnet")
	key, _ := crypto.HexToECDSA(testKeyB)
	conv := services.NewTreasury(map[string]*services.TreasuryLane{"base": {
		SponsorLane: &services.SponsorLane{ChainKey: "base", Signer: services.NewLocalKeyReleaseSigner(key, nil)}, ChainID: 8453,
	}}, services.TreasuryCaps{MaxInFlightUSDPerOrder: 2000}, nil)
	if got := poolTreasuryFor(cfg, services.NewBlockchainService(cfg), conv); got != nil {
		t.Fatal("a testnet pool beside a live mainnet conversion treasury must be refused")
	}
}

func TestPoolTreasury_NoPoolNoTreasury(t *testing.T) {
	cfg := testConfig(t)
	if poolTreasuryFor(cfg, nil, nil) != nil {
		t.Fatal("no pool row, no treasury")
	}
}

func TestBuildRampConversion_StillRefusesASharedKey(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = true
	cfg.RampTreasuryPrivateKey = testKeyA
	cfg.PMReleasePrivateKey = testKeyA
	if !refusesToStart(t, func() { rampTreasuryKey(cfg, "[liquidity]") }) {
		t.Fatal("the extracted key check must still refuse a shared key")
	}
}
