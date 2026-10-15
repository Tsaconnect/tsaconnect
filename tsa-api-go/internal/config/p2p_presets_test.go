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

const (
	directFMAddr    = "0x00000000000000000000000000000000000FEE01"
	directOwnerAddr = "0x62bA000000000000000000000000000000001b96"
)

// The BSC rows are the direct escrow: audience direct, USDT+USDC, dormant
// until the switch fills the escrow (plan API-11). Sonic stays open to all
// until then.
func TestBSCPresets_DirectRowsDormant(t *testing.T) {
	for _, n := range []string{"mainnet", "testnet"} {
		r := bscPresets[n]
		if r.audience != P2PAudienceDirect || r.p2pEscrow != "" || !reflect.DeepEqual(r.assets, []string{"USDT", "USDC"}) {
			t.Fatalf("bsc %s row = %+v", n, r)
		}
		if sonicPresets[n].audience != P2PAudienceAll {
			t.Fatalf("sonic %s audience = %q; it flips to pm only with the BSC switch", n, sonicPresets[n].audience)
		}
	}
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": bscPresets}); err != nil {
		t.Fatal(err)
	}
	want := []P2PChainConfig{{ChainID: 56, ChainKey: "bsc", Network: "mainnet", Audience: P2PAudienceDirect}}
	if len(cfg.P2PChains) != 0 || !reflect.DeepEqual(cfg.DormantP2PChains, want) {
		t.Fatalf("chains = %v dormant = %+v", cfg.P2PChains, cfg.DormantP2PChains)
	}
}

// A filled direct row carries its FeeManager and expected owner, validated
// like every preset address; an unknown audience refuses boot.
func TestP2PPresets_DirectRowFields(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	row := chainPreset{p2pEscrow: p2pEscrowAddr, eip712Version: "2", audience: P2PAudienceDirect,
		feeManager: directFMAddr, expectedOwner: directOwnerAddr, assets: []string{"USDT"}}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": row}}); err != nil {
		t.Fatal(err)
	}
	if cc := cfg.P2PChains[56]; cc.Audience != P2PAudienceDirect || cc.FeeManager != directFMAddr || cc.ExpectedOwner != directOwnerAddr {
		t.Fatalf("row = %+v", cc)
	}
	for _, bad := range []func(*chainPreset){
		func(r *chainPreset) { r.expectedOwner = "0x123" },
		func(r *chainPreset) { r.feeManager = "0x0000000000000000000000000000000000000000" },
		func(r *chainPreset) { r.audience = "everyone" },
	} {
		r := row
		bad(&r)
		if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": r}}); err == nil {
			t.Fatalf("row %+v must refuse", r)
		}
	}
}

// Decision 17: the direct row's FeeManager is BSC's; FEE_MANAGER_ADDRESS_BSC
// may only repeat it.
func TestBSCFeeManager_PresetWins(t *testing.T) {
	for _, tc := range []struct {
		env, preset, want string
		fatal             bool
	}{
		{"", "", "", false},
		{poolFMAddr, "", poolFMAddr, false},
		{"", directFMAddr, directFMAddr, false},
		{strings.ToLower(directFMAddr), directFMAddr, directFMAddr, false},
		{poolFMAddr, directFMAddr, "", true},
		{"0x123", "", "", true},
	} {
		got, err := bscFeeManager(tc.env, tc.preset)
		if (err != nil) != tc.fatal || (!tc.fatal && got != tc.want) {
			t.Fatalf("bscFeeManager(%q, %q) = %q, %v", tc.env, tc.preset, got, err)
		}
	}
}
