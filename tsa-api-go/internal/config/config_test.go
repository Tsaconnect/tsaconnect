package config

import (
	"reflect"
	"testing"
)

// Sonic is always in the registry; BSC is dormant when no escrow is configured.
func TestLoad_SonicAlways_BSCDormantByDefault(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "") // BSC dormant

	cfg := Load()

	sonic, ok := cfg.P2PChains[14601]
	if !ok {
		t.Fatalf("expected Sonic testnet (14601) in P2PChains, got %+v", cfg.P2PChains)
	}
	if sonic.ChainKey != "sonic" || sonic.EIP712Version != "2" {
		t.Errorf("sonic entry = %+v, want chainKey=sonic version=2", sonic)
	}
	if sonic.EscrowAddress == "" {
		t.Errorf("sonic escrow should be the testnet preset, got empty")
	}
	if !reflect.DeepEqual(sonic.Assets, []string{"USDT", "USDC", "MCGP"}) {
		t.Errorf("sonic assets = %v, want [USDT USDC MCGP]", sonic.Assets)
	}
	if _, bsc := cfg.P2PChains[97]; bsc {
		t.Errorf("BSC must be dormant when no escrow configured, but 97 present")
	}
}

// BSC joins the registry once its escrow address is configured.
func TestLoad_BSCEnabledWhenEscrowConfigured(t *testing.T) {
	t.Setenv("BSC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")

	cfg := Load()

	bsc, ok := cfg.P2PChains[97]
	if !ok {
		t.Fatalf("expected BSC testnet (97) in P2PChains, got %+v", cfg.P2PChains)
	}
	if bsc.ChainKey != "bsc" || bsc.EIP712Version != "2" {
		t.Errorf("bsc entry = %+v, want chainKey=bsc version=2", bsc)
	}
	if bsc.EscrowAddress != "0x00000000000000000000000000000000000bsc01" {
		t.Errorf("bsc escrow = %q, want the env override", bsc.EscrowAddress)
	}
	if !reflect.DeepEqual(bsc.Assets, []string{"USDT", "USDC"}) {
		t.Errorf("bsc assets = %v, want [USDT USDC]", bsc.Assets)
	}
}

func TestLoad_BSCUnknownNetworkFallsBackToTestnet(t *testing.T) {
	t.Setenv("BSC_NETWORK", "garbage")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")
	cfg := Load()
	if _, ok := cfg.P2PChains[97]; !ok {
		t.Fatalf("expected BSC testnet (97) after unknown-network fallback, got %+v", cfg.P2PChains)
	}
	if _, mainnet := cfg.P2PChains[56]; mainnet {
		t.Errorf("unknown BSC_NETWORK must not select mainnet (56)")
	}
}

func TestLoad_BSCVersionEnvOverride(t *testing.T) {
	t.Setenv("BSC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")
	t.Setenv("P2P_ESCROW_EIP712_VERSION_BSC", "7")
	cfg := Load()
	bsc, ok := cfg.P2PChains[97]
	if !ok {
		t.Fatalf("expected BSC testnet (97), got %+v", cfg.P2PChains)
	}
	if bsc.EIP712Version != "7" {
		t.Errorf("bsc EIP712Version = %q, want 7 (env override)", bsc.EIP712Version)
	}
}

// The legacy scalar fields still mirror the Sonic entry (back-compat for
// consumers not yet migrated to P2PChains).
func TestLoad_LegacyScalarsMirrorSonic(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "mainnet")
	cfg := Load()
	sonic := cfg.P2PChains[146]
	if cfg.P2PEscrowAddress != sonic.EscrowAddress {
		t.Errorf("legacy P2PEscrowAddress %q != sonic entry %q", cfg.P2PEscrowAddress, sonic.EscrowAddress)
	}
	if cfg.P2PEscrowEIP712Version != sonic.EIP712Version {
		t.Errorf("legacy version %q != sonic entry %q", cfg.P2PEscrowEIP712Version, sonic.EIP712Version)
	}
}
