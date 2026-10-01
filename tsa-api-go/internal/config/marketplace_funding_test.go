package config

import (
	"os"
	"slices"
	"testing"
)

// Marketplace funding ships flag-off-first, and its Sonic gas lane is its own:
// enabling either must never change what the ramp sponsor lanes (sells, TSA
// Pay) do, so sonic is not a default RAMP_SPONSOR_CHAINS lane.
func TestLoad_MarketplaceFundingDefaultsOff(t *testing.T) {
	for _, k := range []string{"MARKETPLACE_FUNDING", "MARKETPLACE_SONIC_GAS", "RAMP_SPONSOR_CHAINS",
		"MARKETPLACE_SONIC_GAS_FLOOR_WEI", "MARKETPLACE_SONIC_GAS_TOPUP_MAX_WEI"} {
		t.Setenv(k, "") // restored after the test
		os.Unsetenv(k)
	}
	cfg := Load()
	if cfg.MarketplaceFundingEnabled || cfg.MarketplaceSonicGasEnabled {
		t.Fatalf("funding=%v sonic gas=%v, want both off by default", cfg.MarketplaceFundingEnabled, cfg.MarketplaceSonicGasEnabled)
	}
	if slices.Contains(cfg.RampSponsorChains, "sonic") {
		t.Fatalf("RAMP_SPONSOR_CHAINS default %v must not include sonic", cfg.RampSponsorChains)
	}
	if cfg.MarketplaceSonicGasFloorWei != "5000000000000000000" || cfg.MarketplaceSonicGasTopUpMaxWei != "500000000000000000" {
		t.Fatalf("sonic lane floor/max = %s/%s, want 5 S / 0.5 S", cfg.MarketplaceSonicGasFloorWei, cfg.MarketplaceSonicGasTopUpMaxWei)
	}
	if cfg.MarketplaceEscrowGasPerOrder != 400_000 {
		t.Fatalf("gas per order = %d", cfg.MarketplaceEscrowGasPerOrder)
	}
}
