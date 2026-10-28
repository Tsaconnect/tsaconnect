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
		c.P2PEscrow != "0xDB4Ff053b677e9C65BD0988f74922329103FB2f3" || c.ChainKey != "sonic" || c.DeployBlock != 81691442 {
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
