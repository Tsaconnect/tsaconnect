package config

import (
	"os"
	"testing"
)

func TestTronDormantWhenUnset(t *testing.T) {
	os.Unsetenv("TRON_RPC_URL")
	m := buildNonEVMChains()
	if _, ok := m["tron"]; ok {
		t.Fatal("tron must be absent when TRON_RPC_URL is unset")
	}
}

func TestTronPresentWhenConfigured(t *testing.T) {
	os.Setenv("TRON_RPC_URL", "https://api.trongrid.io")
	os.Setenv("TRON_USDT_ADDRESS", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	defer os.Unsetenv("TRON_RPC_URL")
	defer os.Unsetenv("TRON_USDT_ADDRESS")

	m := buildNonEVMChains()
	tron, ok := m["tron"]
	if !ok {
		t.Fatal("expected tron config")
	}
	if tron.Family != "tron" || tron.NativeSymbol != "TRX" {
		t.Fatalf("unexpected tron config: %+v", tron)
	}
	if tron.Tokens["USDT"] == "" {
		t.Fatal("expected USDT token address")
	}
}
