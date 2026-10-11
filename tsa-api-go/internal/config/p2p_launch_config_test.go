package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_P2PLaunchDefaults(t *testing.T) {
	for _, k := range []string{"P2P_PAID_UNRELEASED_ALERT_AFTER", "P2P_FIXED_RATE_BAND_BPS", "P2P_GAS_TOPUPS_PER_DAY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if cfg := Load(); cfg.P2PPaidUnreleasedAlertAfter != 2*time.Hour {
		t.Fatalf("got %v", cfg.P2PPaidUnreleasedAlertAfter)
	}
	if cfg := Load(); cfg.P2PFixedRateBandBps != 2000 {
		t.Fatalf("band = %d", cfg.P2PFixedRateBandBps)
	}
	if cfg := Load(); cfg.P2PGasTopUpsPerDay != 6 {
		t.Fatalf("gas top-ups/day = %d", cfg.P2PGasTopUpsPerDay)
	}
}

// A non-positive override is not "off": the knob falls back to its default.
func TestLoad_P2PLaunchKnobsNormaliseNonPositive(t *testing.T) {
	t.Setenv("P2P_FIXED_RATE_BAND_BPS", "0")
	t.Setenv("P2P_GAS_TOPUPS_PER_DAY", "-1")
	t.Setenv("P2P_PAID_UNRELEASED_ALERT_AFTER", "0s")
	cfg := Load()
	if cfg.P2PFixedRateBandBps != DefaultP2PFixedRateBandBps || cfg.P2PGasTopUpsPerDay != DefaultP2PGasTopUpsPerDay ||
		cfg.P2PPaidUnreleasedAlertAfter != DefaultP2PPaidUnreleasedAlertAfter {
		t.Fatalf("band=%d topups=%d after=%v", cfg.P2PFixedRateBandBps, cfg.P2PGasTopUpsPerDay, cfg.P2PPaidUnreleasedAlertAfter)
	}
}

// The shared Sonic lane reads SONIC_GAS_*; the old MARKETPLACE_SONIC_GAS_*
// names still work as fallbacks, and the new name wins when both are set.
func TestLoad_SonicGasLaneRenamedWithFallback(t *testing.T) {
	for _, k := range []string{"SONIC_GAS_FLOOR_WEI", "SONIC_GAS_TOPUP_MAX_WEI",
		"MARKETPLACE_SONIC_GAS_FLOOR_WEI", "MARKETPLACE_SONIC_GAS_TOPUP_MAX_WEI"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("MARKETPLACE_SONIC_GAS_FLOOR_WEI", "111")
	t.Setenv("MARKETPLACE_SONIC_GAS_TOPUP_MAX_WEI", "222")
	if cfg := Load(); cfg.SonicGasFloorWei != "111" || cfg.SonicGasTopUpMaxWei != "222" {
		t.Fatalf("old names: floor=%s max=%s", cfg.SonicGasFloorWei, cfg.SonicGasTopUpMaxWei)
	}
	t.Setenv("SONIC_GAS_FLOOR_WEI", "333")
	t.Setenv("SONIC_GAS_TOPUP_MAX_WEI", "444")
	if cfg := Load(); cfg.SonicGasFloorWei != "333" || cfg.SonicGasTopUpMaxWei != "444" {
		t.Fatalf("new names: floor=%s max=%s", cfg.SonicGasFloorWei, cfg.SonicGasTopUpMaxWei)
	}
}
