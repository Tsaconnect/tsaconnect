package main

import (
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

func TestRampSponsorLaneLimits(t *testing.T) {
	baseCfg := func() *config.Config {
		return &config.Config{
			RampSponsorDailyGasBudget: "50000000",
			RampSponsorBalanceFloorWei: map[string]string{
				"base":     "2000000000000000",
				"bsc":      "10000000000000000",
				"polygon":  "5000000000000000000",
				"arbitrum": "2000000000000000",
			},
			RampSponsorTopUpMaxWei: map[string]string{
				"base":     "50000000000000",
				"bsc":      "500000000000000",
				"polygon":  "500000000000000000",
				"arbitrum": "50000000000000",
			},
		}
	}

	t.Run("base with valid floor/max/budget", func(t *testing.T) {
		floor, topUpMax, dailyBudget, reason := rampSponsorLaneLimits(baseCfg(), "base")
		if reason != "" {
			t.Fatalf("expected no skip reason, got %q", reason)
		}
		if floor == nil || topUpMax == nil || dailyBudget == nil {
			t.Fatalf("expected all limits non-nil: floor=%v topUpMax=%v dailyBudget=%v", floor, topUpMax, dailyBudget)
		}
	})

	t.Run("ethereum is not a phase 1 chain", func(t *testing.T) {
		cfg := baseCfg()
		cfg.RampSponsorBalanceFloorWei["ethereum"] = "2000000000000000"
		cfg.RampSponsorTopUpMaxWei["ethereum"] = "50000000000000"
		_, _, _, reason := rampSponsorLaneLimits(cfg, "ethereum")
		if !strings.Contains(reason, "phase 1") {
			t.Fatalf("expected reason to mention phase 1, got %q", reason)
		}
	})

	t.Run("bsc with malformed top-up max", func(t *testing.T) {
		cfg := baseCfg()
		cfg.RampSponsorTopUpMaxWei["bsc"] = "5e14"
		_, _, _, reason := rampSponsorLaneLimits(cfg, "bsc")
		if !strings.Contains(reason, "RAMP_SPONSOR_TOPUP_MAX_WEI_BSC") {
			t.Fatalf("expected reason to mention RAMP_SPONSOR_TOPUP_MAX_WEI_BSC, got %q", reason)
		}
	})

	t.Run("polygon with empty floor", func(t *testing.T) {
		cfg := baseCfg()
		cfg.RampSponsorBalanceFloorWei["polygon"] = ""
		_, _, _, reason := rampSponsorLaneLimits(cfg, "polygon")
		if !strings.Contains(reason, "RAMP_SPONSOR_BALANCE_FLOOR_WEI_POLYGON") {
			t.Fatalf("expected reason to mention RAMP_SPONSOR_BALANCE_FLOOR_WEI_POLYGON, got %q", reason)
		}
	})

	t.Run("empty daily gas budget", func(t *testing.T) {
		cfg := baseCfg()
		cfg.RampSponsorDailyGasBudget = ""
		_, _, _, reason := rampSponsorLaneLimits(cfg, "base")
		if !strings.Contains(reason, "RAMP_SPONSOR_DAILY_GAS_BUDGET") {
			t.Fatalf("expected reason to mention RAMP_SPONSOR_DAILY_GAS_BUDGET, got %q", reason)
		}
	})
}
