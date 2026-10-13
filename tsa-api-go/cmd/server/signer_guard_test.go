package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
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

const testKeyC = "3333333333333333333333333333333333333333333333333333333333333333"

// captureLog returns what fn logged.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

// Role resolution: {unset, = operator, ≠ operator} × each operator role.
func TestResolveSignerRoles_FallsBackToTheOperatorOnlyWhenUnset(t *testing.T) {
	roles := map[string]func(*config.Config) *string{
		"IT_OPERATOR_PRIVATE_KEY":  func(c *config.Config) *string { return &c.ITOperatorPrivateKey },
		"RAMP_SPONSOR_PRIVATE_KEY": func(c *config.Config) *string { return &c.RampSponsorPrivateKey },
	}
	for name, field := range roles {
		for _, tc := range []struct {
			state, own, want string
		}{
			{"unset", "", testKeyA},
			{"equal to the operator", "0x" + testKeyA, "0x" + testKeyA},
			{"its own key", testKeyB, testKeyB},
		} {
			t.Run(name+"/"+tc.state, func(t *testing.T) {
				cfg := testConfig(t)
				cfg.PMReleasePrivateKey = "0x" + testKeyA
				*field(cfg) = tc.own
				out := captureLog(t, func() { resolveSignerRoles(cfg) })
				if got := *field(cfg); got != tc.want {
					t.Fatalf("resolved key = %q, want %q", got, tc.want)
				}
				if tc.state == "its own key" && !strings.Contains(out, "DEPRECATED") {
					t.Fatalf("a distinct role key must log a DEPRECATED line: %s", out)
				}
				for _, k := range []string{testKeyA, testKeyB} {
					if strings.Contains(out, k) {
						t.Fatal("the boot lines must never carry key material")
					}
				}
			})
		}
	}
}

// Owner decision 2026-10-13: the relayer keeps its own key — it never falls
// back to the operator.
func TestResolveSignerRoles_RelayerNeverFallsBack(t *testing.T) {
	cfg := testConfig(t)
	cfg.PMReleasePrivateKey = testKeyA
	out := captureLog(t, func() { resolveSignerRoles(cfg) })
	if cfg.RelayerPrivateKey != "" || !strings.Contains(out, "role=relayer dormant") {
		t.Fatalf("relayer key = %q, log = %s; want unset and a dormant line", cfg.RelayerPrivateKey, out)
	}
}

// No operator key (unset, or a KMS signer): nothing falls back.
func TestResolveSignerRoles_NoOperatorNoFallback(t *testing.T) {
	for _, cfg := range []*config.Config{
		{Env: "test"},
		{Env: "test", PMReleasePrivateKey: testKeyA, PMReleaseSigner: "kms"},
	} {
		resolveSignerRoles(cfg)
		if cfg.ITOperatorPrivateKey != "" || cfg.RelayerPrivateKey != "" || cfg.RampSponsorPrivateKey != "" {
			t.Fatalf("no usable operator key: roles must stay unset, got %+v", cfg)
		}
	}
}

// Golden: today's production shape (every role var set to its own key) is
// left exactly as it is — the deploy is behaviour-neutral until Doppler
// deletes a var.
func TestResolveSignerRoles_AllDistinctIsUnchanged(t *testing.T) {
	cfg := testConfig(t)
	cfg.PMReleasePrivateKey, cfg.ITOperatorPrivateKey, cfg.RelayerPrivateKey, cfg.RampSponsorPrivateKey =
		testKeyA, testKeyB, testKeyC, "4444444444444444444444444444444444444444444444444444444444444444"
	before := *cfg
	resolveSignerRoles(cfg)
	if cfg.ITOperatorPrivateKey != before.ITOperatorPrivateKey || cfg.RelayerPrivateKey != before.RelayerPrivateKey ||
		cfg.RampSponsorPrivateKey != before.RampSponsorPrivateKey || cfg.PMReleasePrivateKey != before.PMReleasePrivateKey {
		t.Fatal("distinct role keys must be kept as they are")
	}
}

// Class rule: operator-class keys may coincide; operator = treasury, operator
// = company wallet and company wallet = treasury still refuse to start.
func TestAssertDistinctKey_ClassRule(t *testing.T) {
	keyA, _ := crypto.HexToECDSA(testKeyA)
	cases := []struct {
		name, other string
		refuse      bool
	}{
		{"PM_RELEASE_PRIVATE_KEY", "IT_OPERATOR_PRIVATE_KEY", false},
		{"RAMP_SPONSOR_PRIVATE_KEY", "RELAYER_PRIVATE_KEY", true},
		{"RELAYER_PRIVATE_KEY", "PM_RELEASE_PRIVATE_KEY", true},
		{"RELAYER_PRIVATE_KEY", "IT_OPERATOR_PRIVATE_KEY", true},
		{"RELAYER_PRIVATE_KEY", "RAMP_TREASURY_PRIVATE_KEY", true},
		{"RAMP_SPONSOR_PRIVATE_KEY", "PM_RELEASE_PRIVATE_KEY", false},
		{"PM_RELEASE_PRIVATE_KEY", "RAMP_TREASURY_PRIVATE_KEY", true},
		{"RAMP_TREASURY_PRIVATE_KEY", "IT_OPERATOR_PRIVATE_KEY", true},
		{"RAMP_TREASURY_PRIVATE_KEY", "RELAYER_PRIVATE_KEY", true},
		{"RAMP_SPONSOR_PRIVATE_KEY", "RAMP_TREASURY_PRIVATE_KEY", true},
		{"RAMP_TREASURY_WALLET_PRIVATE_KEY", "RAMP_TREASURY_PRIVATE_KEY", true},
		{"RAMP_TREASURY_WALLET_PRIVATE_KEY", "PM_RELEASE_PRIVATE_KEY", true},
	}
	for _, tc := range cases {
		err := assertDistinctKey(tc.name, keyA, map[string]string{tc.other: testKeyA})
		if (err != nil) != tc.refuse {
			t.Errorf("%s = %s: err = %v, want refuse=%v", tc.name, tc.other, err, tc.refuse)
		}
	}
}

// After the fallback, the treasury builder still refuses an operator role
// that lands on the treasury key.
func TestRampTreasuryKey_RefusesTheOperatorKeyAfterFallback(t *testing.T) {
	cfg := testConfig(t)
	cfg.PMReleasePrivateKey, cfg.RampTreasuryPrivateKey = testKeyA, testKeyA
	resolveSignerRoles(cfg)
	if !refusesToStart(t, func() { rampTreasuryKey(cfg, "[test]") }) {
		t.Fatal("OPERATOR = TREASURY must refuse to start")
	}
}

// The company wallet must still differ from both classes in Phase 1.
func TestBuildCompanyWallet_RefusesTheOperatorOrTreasuryKey(t *testing.T) {
	for _, set := range []func(*config.Config){
		func(c *config.Config) { c.PMReleasePrivateKey = testKeyA },
		func(c *config.Config) { c.RampTreasuryPrivateKey = testKeyA },
	} {
		cfg := testConfig(t)
		set(cfg)
		cfg.RampTreasuryWalletPrivateKey = testKeyA
		resolveSignerRoles(cfg)
		if !refusesToStart(t, func() { buildCompanyWallet(cfg, nil) }) {
			t.Fatal("the company wallet on the operator or treasury key must refuse to start")
		}
	}
}

// The summary names the operator and treasury addresses with their roles and
// chain ids.
func TestLogSignerSummary_NamesClassesRolesAndLanes(t *testing.T) {
	cfg := poolTestConfig(t, "mainnet")
	cfg.PMReleasePrivateKey = testKeyA
	resolveSignerRoles(cfg)
	bs := services.NewBlockchainService(cfg)
	buildReleaseSigner(cfg, bs)
	poolTreasuryFor(cfg, bs, nil)
	out := captureLog(t, func() { logSignerSummary(cfg, bs.Signers()) })
	if !strings.Contains(out, "[signer] operator="+signerAddress(testKeyA)+" roles=pm_release lanes=[56]") ||
		!strings.Contains(out, "[signer] treasury="+signerAddress(testKeyB)+" roles=treasury lanes=[56]") {
		t.Fatalf("summary = %s", out)
	}
}
