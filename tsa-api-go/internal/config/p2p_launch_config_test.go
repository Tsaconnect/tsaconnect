package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_P2PLaunchDefaults(t *testing.T) {
	for _, k := range []string{"P2P_PAID_UNRELEASED_ALERT_AFTER", "P2P_FIXED_RATE_BAND_BPS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if cfg := Load(); cfg.P2PPaidUnreleasedAlertAfter != 2*time.Hour {
		t.Fatalf("got %v", cfg.P2PPaidUnreleasedAlertAfter)
	}
	if cfg := Load(); cfg.P2PFixedRateBandBps != 2000 {
		t.Fatalf("band = %d", cfg.P2PFixedRateBandBps)
	}
}
