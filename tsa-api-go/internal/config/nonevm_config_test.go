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

// Mainnet Tron ships with LiFi's diamond pinned; testnet has no LiFi and so no
// router (no route is signed there). Either can be overridden per network.
func TestTronRouters(t *testing.T) {
	t.Setenv("TRON_RPC_URL", "")
	t.Setenv("TRON_MAINNET_RPC_URL", "https://api.trongrid.io")
	t.Setenv("TRON_TESTNET_RPC_URL", "https://nile.trongrid.io")
	os.Unsetenv("TRON_MAINNET_ROUTERS")
	os.Unsetenv("TRON_TESTNET_ROUTERS")

	m := buildNonEVMChains()
	if got := m["mainnet:tron"].Routers; len(got) != 1 || got[0] != "TU3ymitEKCWQFtASkEeHaPb8NfZcJtCHLt" {
		t.Fatalf("mainnet routers = %v, want the LiFi diamond", got)
	}
	if got := m["testnet:tron"].Routers; len(got) != 0 {
		t.Fatalf("testnet routers = %v, want none", got)
	}

	t.Setenv("TRON_MAINNET_ROUTERS", " TAAA , ,TBBB")
	if got := buildNonEVMChains()["mainnet:tron"].Routers; len(got) != 2 || got[0] != "TAAA" || got[1] != "TBBB" {
		t.Fatalf("override routers = %v, want [TAAA TBBB]", got)
	}
}
