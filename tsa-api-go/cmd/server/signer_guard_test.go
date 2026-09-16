package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// rawSignerKeyAllowed is the single guard every raw-env-key signer (IT
// operator, relayer, ramp sponsor, PM release local signer) must go through.
// Its truth table: allowed everywhere except production without the explicit
// opt-in, and a nil config (shouldn't happen, but must not panic) is treated
// as non-production.
func TestRawSignerKeyAllowed(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		allowed bool
	}{
		{"development, flag off", &config.Config{Env: "development", AllowRawSignerKeys: false}, true},
		{"development, flag on", &config.Config{Env: "development", AllowRawSignerKeys: true}, true},
		{"staging, flag off", &config.Config{Env: "staging", AllowRawSignerKeys: false}, true},
		{"production, flag off", &config.Config{Env: "production", AllowRawSignerKeys: false}, false},
		{"production, flag on", &config.Config{Env: "production", AllowRawSignerKeys: true}, true},
		// IsProduction() normalises Env, so the guard must too.
		{"Production (mixed case), flag off", &config.Config{Env: "Production", AllowRawSignerKeys: false}, false},
		{"prod alias, flag off", &config.Config{Env: "prod", AllowRawSignerKeys: false}, false},
		{"prod alias, flag on", &config.Config{Env: "prod", AllowRawSignerKeys: true}, true},
		{"nil config", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rawSignerKeyAllowed(tc.cfg); got != tc.allowed {
				t.Errorf("rawSignerKeyAllowed(%+v) = %v, want %v", tc.cfg, got, tc.allowed)
			}
		})
	}
}

// alertRawSignerKeyInProduction must never panic on a nil config, and must
// only be reachable (per its call sites) once rawSignerKeyAllowed has already
// permitted the raw key — this just proves the production/non-production
// split so a future call site can't skip it silently. It does not assert on
// log output; the log line's exact shape is guarded by the ALERT convention
// grepped for in ops docs, not tested here.
func TestAlertRawSignerKeyInProduction_NoPanic(t *testing.T) {
	alertRawSignerKeyInProduction(nil, "TEST_KEY")
	alertRawSignerKeyInProduction(&config.Config{Env: "development"}, "TEST_KEY")
	alertRawSignerKeyInProduction(&config.Config{Env: "production", AllowRawSignerKeys: true}, "TEST_KEY")
}
