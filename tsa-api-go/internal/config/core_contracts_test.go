package config

import (
	"strings"
	"testing"
)

func coreTestConfig(sonicNetwork string) *Config {
	return &Config{
		Networks: map[string]NetworkConfig{
			"mainnet": {Chains: map[string]ChainConfig{"sonic": {ChainID: 146}}},
			"testnet": {Chains: map[string]ChainConfig{"sonic": {ChainID: 14601}}},
		},
		p2pNetworks: map[string]string{"sonic": sonicNetwork},
	}
}

// Prod runs without CORE_CONTRACTS: the core set must stay dormant.
func TestCoreContracts_OffByDefault(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "")
	cfg := coreTestConfig("mainnet")
	if err := assembleCoreContracts(cfg, coreSets); err != nil {
		t.Fatal(err)
	}
	if cfg.CoreSet != CoreSetOff || len(cfg.CoreContracts) != 0 {
		t.Fatalf("set=%q contracts=%v, want off and none", cfg.CoreSet, cfg.CoreContracts)
	}
}

// A selector that cannot be honoured leaves the set dormant instead of failing boot, so a bad
// value in prod's env can never crash-loop the API.
func TestCoreContracts_UnusableSelectorsStayDormant(t *testing.T) {
	for _, tc := range []struct{ set, allowDev string }{
		{"off", ""}, {"false", ""}, {"0", ""}, {"none", ""},
		{"staging", ""},  // unknown
		{"prod", ""},     // not deployed yet
		{"dev", ""},      // no allow switch
		{"dev", "false"}, // allow switch off
	} {
		t.Setenv("CORE_CONTRACTS", tc.set)
		t.Setenv(coreDevAllowEnv, tc.allowDev)
		cfg := coreTestConfig("mainnet")
		if err := assembleCoreContracts(cfg, coreSets); err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if cfg.CoreSet != CoreSetOff || len(cfg.CoreContracts) != 0 {
			t.Fatalf("%+v: set=%q contracts=%v, want dormant", tc, cfg.CoreSet, cfg.CoreContracts)
		}
	}
}

func TestCoreContracts_DevSetOnSonicMainnet(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	t.Setenv(coreDevAllowEnv, "true")
	cfg := coreTestConfig("mainnet")
	if err := assembleCoreContracts(cfg, coreSets); err != nil {
		t.Fatal(err)
	}
	c, ok := cfg.CoreContracts[146]
	if !ok || cfg.CoreSet != CoreSetDev {
		t.Fatalf("set=%q, no Sonic row: %v", cfg.CoreSet, cfg.CoreContracts)
	}
	if c.FeeConfig != "0x9aba9E3e3bCD5d609ef99C6dC5973386769e2572" || c.OrderEscrow != "0x91b85d83Ec8d9f8369c245297110a82882B7248E" ||
		c.P2PEscrow != "0xDB4Ff053b677e9C65BD0988f74922329103FB2f3" || c.ChainKey != "sonic" || c.DeployBlock != 81691442 ||
		c.Forwarder != "0x569F8a58dFCCaA600d92B641F1654dCEd2dA23e4" {
		t.Fatalf("dev Sonic row = %+v", c)
	}
}

// The dev set lives on Sonic mainnet; pairing it with a testnet Sonic stack would point the
// backend at addresses that do not exist there.
func TestCoreContracts_RefusesTheWrongNetwork(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	t.Setenv(coreDevAllowEnv, "true")
	err := assembleCoreContracts(coreTestConfig("testnet"), coreSets)
	if err == nil || !strings.Contains(err.Error(), "SONIC_NETWORK selects testnet") {
		t.Fatalf("err = %v, want the network refusal", err)
	}
}

// A row on a chain with no selected network cannot be checked, so it is refused rather than trusted.
func TestCoreContracts_RefusesAChainWithNoSelectedNetwork(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	t.Setenv(coreDevAllowEnv, "true")
	row := coreSets[CoreSetDev]["sonic"]
	cfg := coreTestConfig("mainnet")
	cfg.Networks["mainnet"].Chains["polygon"] = ChainConfig{ChainID: 137}
	err := assembleCoreContracts(cfg, map[string]map[string]coreRow{CoreSetDev: {"polygon": row}})
	if err == nil || !strings.Contains(err.Error(), "no selected network") {
		t.Fatalf("err = %v, want the unchecked-network refusal", err)
	}
}

func TestCoreContracts_RefusesABadRow(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	t.Setenv(coreDevAllowEnv, "true")
	good := coreSets[CoreSetDev]["sonic"]
	badAddr, noBlock := good, good
	badAddr.feeConfig, badAddr.admin = "0x1234", "0x0000000000000000000000000000000000000000"
	noBlock.deployBlock = 0
	for want, row := range map[string]coreRow{"FeeConfig": badAddr, "deploy block": noBlock} {
		err := assembleCoreContracts(coreTestConfig("mainnet"), map[string]map[string]coreRow{CoreSetDev: {"sonic": row}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want the %s refusal", err, want)
		}
	}
}

// The hard switch: a P2P chain the set covers takes its P2PTradeEscrow and drops the legacy
// escrow's FeeManager and owner; one it does not (and every one while dormant) goes dormant.
func TestBindP2PChainsToCore(t *testing.T) {
	cfg := &Config{
		CoreSet:       CoreSetDev,
		CoreContracts: map[int64]CoreContracts{146: {ChainID: 146, P2PEscrow: "0x00000000000000000000000000000000000E5C03"}},
		P2PChains: map[int64]P2PChainConfig{
			146: {ChainID: 146, ChainKey: "sonic", EscrowAddress: "0x7dAdae104930da7B62EA185fa697fCFA7f9eC324", EIP712Version: "2", Audience: P2PAudiencePM},
			56: {ChainID: 56, ChainKey: "bsc", EscrowAddress: "0xD16c1597EA3be8D2017698CC5d6Bf53b595EEb03", EIP712Version: "2",
				Audience: P2PAudienceDirect, FeeManager: "0xe760018f5b3fE4E8EF52DC029898545ed03d74ac"},
		},
	}
	bindP2PChainsToCore(cfg)
	sonic := cfg.P2PChains[146]
	if sonic.EscrowAddress != "0x00000000000000000000000000000000000E5C03" || sonic.EIP712Version != "3" || sonic.Audience != P2PAudiencePM {
		t.Fatalf("sonic = %+v", sonic)
	}
	if _, ok := cfg.P2PChains[56]; ok || len(cfg.DormantP2PChains) != 1 || cfg.DormantP2PChains[0].ChainID != 56 ||
		cfg.DormantP2PChains[0].Audience != P2PAudienceDirect {
		t.Fatalf("BSC must go dormant: chains=%+v dormant=%+v", cfg.P2PChains, cfg.DormantP2PChains)
	}
}
