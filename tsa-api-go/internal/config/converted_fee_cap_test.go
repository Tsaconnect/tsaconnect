package config

import "testing"

// A converted-fee cap that is not a sane whole number turns raising off
// rather than falling back to something more permissive than was asked.
func TestLoad_ConvertedFeeCapFailsClosed(t *testing.T) {
	for raw, want := range map[string]int{
		"150": 150, "500": 500, "0": 0, "-5": 0, "2000": 0, "1.5": 0, "abc": 0,
	} {
		t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS", raw)
		t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS_BSC", "")
		cfg := Load()
		if cfg.RampConvertedFeeMaxBps != want {
			t.Errorf("RAMP_CONVERTED_FEE_MAX_BPS=%q loaded as %d, want %d", raw, cfg.RampConvertedFeeMaxBps, want)
		}
		if got := cfg.RampConvertedFeeMaxBpsByChain["bsc"]; got != want {
			t.Errorf("bsc inherits %d, want %d", got, want)
		}
	}
	t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS", "")
	t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS_BSC", "9999")
	cfg := Load()
	if cfg.RampConvertedFeeMaxBps != 200 || cfg.RampConvertedFeeMaxBpsByChain["bsc"] != 0 {
		t.Fatalf("unset global = %d (want 200), bad bsc override = %d (want 0)",
			cfg.RampConvertedFeeMaxBps, cfg.RampConvertedFeeMaxBpsByChain["bsc"])
	}
	// A sell's source chain outside RAMP_TREASURY_CHAINS still gets its override.
	t.Setenv("RAMP_TREASURY_CHAINS", "base")
	t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS_ETHEREUM", "120")
	if got := Load().RampConvertedFeeMaxBpsByChain["ethereum"]; got != 120 {
		t.Fatalf("ethereum cap = %d, want its own 120", got)
	}
}
