package config

import (
	"reflect"
	"strings"
	"testing"
)

const (
	poolEscrowAddr = "0x00000000000000000000000000000000000A11CE"
	poolFMAddr     = "0x00000000000000000000000000000000000FEE00"
	bscUSDT        = "0x55d398326f99059fF775485246999027B3197955"
)

func netsWith(chains map[string]ChainConfig, tokens map[string]string) map[string]NetworkConfig {
	return map[string]NetworkConfig{"mainnet": {Chains: chains, TokenAddresses: tokens}, "testnet": {Chains: map[string]ChainConfig{}, TokenAddresses: map[string]string{}}}
}

func poolRow(escrow string) chainPreset {
	return chainPreset{p2pEscrow: escrow, eip712Version: "2", purpose: P2PPurposePool,
		pool: &poolPreset{feeManager: poolFMAddr, tokens: []string{"USDT"}, releaseGasFloorWei: "10000000000000000", logChunkBlocks: 2000}}
}

// Spec §5 / S36: a new chain is one preset row — no code.
func TestP2PPresets_NewChainIsOneRow(t *testing.T) {
	t.Setenv("BASE_NETWORK", "")
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"base": {ChainID: 8453}}, map[string]string{"base:USDT": "0x00000000000000000000000000000000000B05E0"})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"base": {"mainnet": poolRow(poolEscrowAddr)}}); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	cc, ok := cfg.P2PChains[8453]
	if !ok || cc.ChainKey != "base" || cc.Purpose != P2PPurposePool || cc.Network != "mainnet" || cc.Pool == nil ||
		cc.Pool.FeeManager != poolFMAddr || !reflect.DeepEqual(cc.Assets, []string{"USDT"}) || cc.Pool.LogChunkBlocks != 2000 {
		t.Fatalf("base row = %+v pool = %+v", cc, cc.Pool)
	}
	if got, ok := cfg.PoolChain(); !ok || got.ChainID != 8453 {
		t.Fatalf("PoolChain = %+v %v", got, ok)
	}
}

func TestP2PPresets_EmptyPoolRowIsDormant(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": poolRow("")}}); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if _, ok := cfg.P2PChains[56]; ok {
		t.Fatal("an empty pool row must leave the chain out")
	}
	if _, ok := cfg.PoolChain(); ok {
		t.Fatal("no pool chain when the row is empty")
	}
}

func TestConfig_PoolAndOrdinaryP2PSameChainFatal(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}, "bnb": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	err := assembleP2PChains(cfg, map[string]map[string]chainPreset{
		"bsc": {"mainnet": poolRow(poolEscrowAddr)},
		"bnb": {"mainnet": {p2pEscrow: "0x00000000000000000000000000000000000B0B00", eip712Version: "2", assets: []string{"USDT"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "S5") {
		t.Fatalf("err = %v, want the one-escrow-per-chain refusal", err)
	}
}

func TestConfig_MoreThanOnePoolRowFatal(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}, "base": {ChainID: 8453}},
		map[string]string{"bsc:USDT": bscUSDT, "base:USDT": "0x00000000000000000000000000000000000B05E0"})}
	err := assembleP2PChains(cfg, map[string]map[string]chainPreset{
		"bsc": {"mainnet": poolRow(poolEscrowAddr)}, "base": {"mainnet": poolRow("0x00000000000000000000000000000000000B05E1")},
	})
	if err == nil || !strings.Contains(err.Error(), "one pool chain") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfig_PoolTokenWithoutAddressFatal(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": poolRow(poolEscrowAddr)}}); err == nil {
		t.Fatal("a pool token with no address must refuse to boot")
	}
}

func TestConfig_MalformedPoolAddressFatal(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": poolRow("0x123")}}); err == nil {
		t.Fatal("a malformed escrow must refuse to boot")
	}
}

// S4: the pool FeeManager is never in the admin fee flow's registry.
func TestConfig_PoolFeeManagerNotInFeeManagerChains(t *testing.T) {
	cfg := &Config{P2PChains: map[int64]P2PChainConfig{56: {ChainID: 56, Purpose: P2PPurposePool, Pool: &PoolChainConfig{FeeManager: poolFMAddr}}},
		FeeManagerChains: map[int64]FeeManagerChainConfig{56: {ChainID: 56, ChainKey: "bsc", Address: strings.ToLower(poolFMAddr)}}}
	if err := checkPoolFeeManagerExcluded(cfg); err == nil || !strings.Contains(err.Error(), "S4") {
		t.Fatalf("err = %v", err)
	}
	cfg.FeeManagerChains[56] = FeeManagerChainConfig{ChainID: 56, Address: "0x00000000000000000000000000000000000FEE01"}
	if err := checkPoolFeeManagerExcluded(cfg); err != nil {
		t.Fatalf("a different FeeManager: %v", err)
	}
}

func TestLoad_RetiredBSCEscrowEnvRefused(t *testing.T) {
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")
	if err := retiredP2PEnv(); err == nil || !strings.Contains(err.Error(), "P2P_ESCROW_ADDRESS_BSC") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "")
	t.Setenv("P2P_ESCROW_EIP712_VERSION_BSC", "2")
	if err := retiredP2PEnv(); err == nil {
		t.Fatal("the BSC version alias is retired too")
	}
}
