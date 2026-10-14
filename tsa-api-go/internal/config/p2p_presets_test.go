package config

import (
	"bytes"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	p2pEscrowAddr = "0x00000000000000000000000000000000000A11CE"
	poolFMAddr    = "0x00000000000000000000000000000000000FEE00"
	bscUSDT       = "0x55d398326f99059fF775485246999027B3197955"
)

func netsWith(chains map[string]ChainConfig, tokens map[string]string) map[string]NetworkConfig {
	return map[string]NetworkConfig{"mainnet": {Chains: chains, TokenAddresses: tokens}, "testnet": {Chains: map[string]ChainConfig{}, TokenAddresses: map[string]string{}}}
}

func p2pRow(escrow string) chainPreset {
	return chainPreset{p2pEscrow: escrow, eip712Version: "2", assets: []string{"USDT"}}
}

// Spec §5 / S36: a new chain is one preset row — no code.
func TestP2PPresets_NewChainIsOneRow(t *testing.T) {
	t.Setenv("BASE_NETWORK", "")
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"base": {ChainID: 8453}}, map[string]string{"base:USDT": "0x00000000000000000000000000000000000B05E0"})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"base": {"mainnet": p2pRow(p2pEscrowAddr)}}); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	cc, ok := cfg.P2PChains[8453]
	if !ok || cc.ChainKey != "base" || cc.Purpose != P2PPurposeP2P || cc.Network != "mainnet" || !reflect.DeepEqual(cc.Assets, []string{"USDT"}) {
		t.Fatalf("base row = %+v", cc)
	}
}

func TestP2PPresets_EmptyRowIsDormant(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": p2pRow("")}}); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if _, ok := cfg.P2PChains[56]; ok {
		t.Fatal("an empty row must leave the chain out")
	}
}

func TestConfig_TwoRowsOnOneChainFatal(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}, "bnb": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	err := assembleP2PChains(cfg, map[string]map[string]chainPreset{
		"bsc": {"mainnet": p2pRow(p2pEscrowAddr)},
		"bnb": {"mainnet": p2pRow("0x00000000000000000000000000000000000B0B00")},
	})
	if err == nil || !strings.Contains(err.Error(), "S5") {
		t.Fatalf("err = %v, want the one-escrow-per-chain refusal", err)
	}
}

func TestConfig_MalformedEscrowAddressFatal(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": p2pRow("0x123")}}); err == nil {
		t.Fatal("a malformed escrow address must refuse")
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

// Review M4: BSC's network is chosen once — the FeeManager registry reuses
// it, so an unknown BSC_NETWORK alerts once, not twice.
func TestP2PPresets_BSCNetworkChosenOnce(t *testing.T) {
	t.Setenv("BSC_NETWORK", "garbage")
	t.Setenv("FEE_MANAGER_ADDRESS_SONIC", "")
	t.Setenv("FEE_MANAGER_ADDRESS_BSC", poolFMAddr)
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": bscPresets}); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	assembleFeeManagerChains(cfg)
	if n := strings.Count(buf.String(), "p2p_network_unknown"); n != 1 {
		t.Fatalf("p2p_network_unknown logged %d times, want once:\n%s", n, buf.String())
	}
	if fm, ok := cfg.FeeManagerChains[56]; !ok || fm.ChainKey != "bsc" {
		t.Fatalf("FeeManagerChains = %+v, want BSC on its selected (mainnet) id", cfg.FeeManagerChains)
	}
}
