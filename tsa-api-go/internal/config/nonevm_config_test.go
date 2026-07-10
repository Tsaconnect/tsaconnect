package config

import (
	"os"
	"testing"
)

func TestTronDormantWhenUnset(t *testing.T) {
	os.Unsetenv("TRON_RPC_URL")
	os.Unsetenv("TRON_MAINNET_RPC_URL")
	os.Unsetenv("TRON_TESTNET_RPC_URL")
	m := buildNonEVMChains()
	if len(m) != 0 {
		t.Fatalf("tron must be absent when no RPC is set, got %+v", m)
	}
}

// Backward-compat: a single TRON_RPC_URL maps to the network named by TRON_NETWORK.
func TestTronLegacySingleVarMapsToNetwork(t *testing.T) {
	os.Setenv("TRON_RPC_URL", "https://api.trongrid.io")
	os.Setenv("TRON_NETWORK", "mainnet")
	os.Setenv("TRON_USDT_ADDRESS", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	defer os.Unsetenv("TRON_RPC_URL")
	defer os.Unsetenv("TRON_NETWORK")
	defer os.Unsetenv("TRON_USDT_ADDRESS")

	m := buildNonEVMChains()
	tron, ok := m["mainnet:tron"]
	if !ok {
		t.Fatalf("expected mainnet:tron config, got %+v", m)
	}
	if tron.Family != "tron" || tron.NativeSymbol != "TRX" || tron.Network != "mainnet" {
		t.Fatalf("unexpected tron config: %+v", tron)
	}
	if tron.Tokens["USDT"] == "" {
		t.Fatal("expected USDT token address")
	}
}

// Both networks can be configured at once via the per-network vars.
func TestTronBothNetworks(t *testing.T) {
	os.Unsetenv("TRON_RPC_URL")
	os.Setenv("TRON_MAINNET_RPC_URL", "https://api.trongrid.io")
	os.Setenv("TRON_TESTNET_RPC_URL", "https://nile.trongrid.io")
	defer os.Unsetenv("TRON_MAINNET_RPC_URL")
	defer os.Unsetenv("TRON_TESTNET_RPC_URL")

	m := buildNonEVMChains()
	if m["mainnet:tron"].RPCURL != "https://api.trongrid.io" {
		t.Fatalf("mainnet tron wrong: %+v", m["mainnet:tron"])
	}
	if m["testnet:tron"].RPCURL != "https://nile.trongrid.io" {
		t.Fatalf("testnet tron wrong: %+v", m["testnet:tron"])
	}
}
