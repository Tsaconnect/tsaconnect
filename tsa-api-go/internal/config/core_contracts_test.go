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

func TestCoreContracts_DevSetOnSonicMainnet(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	cfg := coreTestConfig("mainnet")
	if err := assembleCoreContracts(cfg, coreSets); err != nil {
		t.Fatal(err)
	}
	c, ok := cfg.CoreContracts[146]
	if !ok {
		t.Fatalf("no Sonic row: %v", cfg.CoreContracts)
	}
	if c.FeeConfig != "0x9aba9E3e3bCD5d609ef99C6dC5973386769e2572" || c.OrderEscrow != "0x91b85d83Ec8d9f8369c245297110a82882B7248E" ||
		c.P2PEscrow != "0xDB4Ff053b677e9C65BD0988f74922329103FB2f3" || c.ChainKey != "sonic" {
		t.Fatalf("dev Sonic row = %+v", c)
	}
}

// The dev set lives on Sonic mainnet; pairing it with a testnet Sonic stack would point the
// backend at addresses that do not exist there.
func TestCoreContracts_RefusesTheWrongNetwork(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	err := assembleCoreContracts(coreTestConfig("testnet"), coreSets)
	if err == nil || !strings.Contains(err.Error(), "SONIC_NETWORK selects testnet") {
		t.Fatalf("err = %v, want the network refusal", err)
	}
}

func TestCoreContracts_RefusesUnknownAndEmptySets(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "staging")
	if err := assembleCoreContracts(coreTestConfig("mainnet"), coreSets); err == nil {
		t.Fatal("an unknown set must be refused")
	}
	t.Setenv("CORE_CONTRACTS", "prod")
	if err := assembleCoreContracts(coreTestConfig("mainnet"), coreSets); err == nil || !strings.Contains(err.Error(), "no contracts yet") {
		t.Fatalf("err = %v, want the empty-set refusal", err)
	}
}

func TestCoreContracts_RefusesABadAddress(t *testing.T) {
	t.Setenv("CORE_CONTRACTS", "dev")
	sets := map[string]map[string]coreRow{CoreSetDev: {"sonic": {network: "mainnet", feeConfig: "0x1234",
		p2pEscrow: "0xDB4Ff053b677e9C65BD0988f74922329103FB2f3", admin: "0xd963C3C596f692b4fD8e53bD9B60Ca1843400Da3"}}}
	if err := assembleCoreContracts(coreTestConfig("mainnet"), sets); err == nil || !strings.Contains(err.Error(), "FeeConfig") {
		t.Fatalf("err = %v, want the FeeConfig address refusal", err)
	}
}
