package config

import (
	"bytes"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A malformed FEE_MANAGER_ADDRESS_* must be rejected at load (design §6) — the
// validation helper is the testable seam (Load itself log.Fatals on a bad value,
// which can't be asserted in-process).
func TestValidateFeeManagerAddress(t *testing.T) {
	valid := []string{
		"0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d",
		"0x0000000000000000000000000000000000000001",
		// 40 hex chars without the 0x prefix is a legitimately valid address —
		// HexToAddress parses it exactly, no silent pad/truncate.
		"6c96B6EB227D1254247cD5015Bfc3e8Ade94415d",
	}
	for _, a := range valid {
		if err := validateFeeManagerAddress("FEE_MANAGER_ADDRESS_SONIC", a); err != nil {
			t.Errorf("validateFeeManagerAddress(%q) unexpected error: %v", a, err)
		}
	}

	invalid := map[string]string{
		"too short":     "0x123",
		"not hex":       "0xZZZZ000000000000000000000000000000000000",
		"garbage":       "not-an-address",
		"too long":      "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415dAB",
		"zero address":  "0x0000000000000000000000000000000000000000",
		"empty padding": "0x0",
	}
	for name, a := range invalid {
		if err := validateFeeManagerAddress("FEE_MANAGER_ADDRESS_BSC", a); err == nil {
			t.Errorf("%s: validateFeeManagerAddress(%q) = nil, want error", name, a)
		}
	}
}

// Sonic is always in the registry; BSC is dormant when no escrow is configured.
func TestLoad_SonicAlways_BSCDormantByDefault(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "") // BSC dormant

	cfg := Load()

	sonic, ok := cfg.P2PChains[14601]
	if !ok {
		t.Fatalf("expected Sonic testnet (14601) in P2PChains, got %+v", cfg.P2PChains)
	}
	if sonic.ChainKey != "sonic" || sonic.EIP712Version != "2" {
		t.Errorf("sonic entry = %+v, want chainKey=sonic version=2", sonic)
	}
	if sonic.EscrowAddress == "" {
		t.Errorf("sonic escrow should be the testnet preset, got empty")
	}
	// MCGP is no longer a P2P asset (owner decision 2026-09-28).
	if !reflect.DeepEqual(sonic.Assets, []string{"USDT", "USDC"}) {
		t.Errorf("sonic assets = %v, want [USDT USDC]", sonic.Assets)
	}
	if _, bsc := cfg.P2PChains[97]; bsc {
		t.Errorf("BSC must be dormant when no escrow configured, but 97 present")
	}
}

// BSC joins the registry once its escrow address is configured.
func TestLoad_BSCEnabledWhenEscrowConfigured(t *testing.T) {
	t.Setenv("BSC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")

	cfg := Load()

	bsc, ok := cfg.P2PChains[97]
	if !ok {
		t.Fatalf("expected BSC testnet (97) in P2PChains, got %+v", cfg.P2PChains)
	}
	if bsc.ChainKey != "bsc" || bsc.EIP712Version != "2" {
		t.Errorf("bsc entry = %+v, want chainKey=bsc version=2", bsc)
	}
	if bsc.EscrowAddress != "0x00000000000000000000000000000000000bsc01" {
		t.Errorf("bsc escrow = %q, want the env override", bsc.EscrowAddress)
	}
	if !reflect.DeepEqual(bsc.Assets, []string{"USDT", "USDC"}) {
		t.Errorf("bsc assets = %v, want [USDT USDC]", bsc.Assets)
	}
}

func TestLoad_BSCUnknownNetworkFallsBackToTestnet(t *testing.T) {
	t.Setenv("BSC_NETWORK", "garbage")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")
	cfg := Load()
	if _, ok := cfg.P2PChains[97]; !ok {
		t.Fatalf("expected BSC testnet (97) after unknown-network fallback, got %+v", cfg.P2PChains)
	}
	if _, mainnet := cfg.P2PChains[56]; mainnet {
		t.Errorf("unknown BSC_NETWORK must not select mainnet (56)")
	}
}

func TestLoad_BSCVersionEnvOverride(t *testing.T) {
	t.Setenv("BSC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")
	t.Setenv("P2P_ESCROW_EIP712_VERSION_BSC", "7")
	cfg := Load()
	bsc, ok := cfg.P2PChains[97]
	if !ok {
		t.Fatalf("expected BSC testnet (97), got %+v", cfg.P2PChains)
	}
	if bsc.EIP712Version != "7" {
		t.Errorf("bsc EIP712Version = %q, want 7 (env override)", bsc.EIP712Version)
	}
}

// The legacy scalar fields still mirror the Sonic entry (back-compat for
// consumers not yet migrated to P2PChains).
func TestLoad_LegacyScalarsMirrorSonic(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "mainnet")
	cfg := Load()
	sonic := cfg.P2PChains[146]
	if cfg.P2PEscrowAddress != sonic.EscrowAddress {
		t.Errorf("legacy P2PEscrowAddress %q != sonic entry %q", cfg.P2PEscrowAddress, sonic.EscrowAddress)
	}
	if cfg.P2PEscrowEIP712Version != sonic.EIP712Version {
		t.Errorf("legacy version %q != sonic entry %q", cfg.P2PEscrowEIP712Version, sonic.EIP712Version)
	}
}

// Robinhood Chain is a wallet-only chain (native ETH + canonical USDG stablecoin)
// present on both networks. It must NOT appear in the P2P registry — no escrow is
// deployed there. USDG has a mainnet address by default; testnet stays env-gated.
func TestLoad_RobinhoodWalletChain(t *testing.T) {
	const usdgMainnet = "0x5fc5360D0400a0Fd4f2af552ADD042D716F1d168"

	cfg := Load()

	main, ok := cfg.Networks["mainnet"].Chains["robinhood"]
	if !ok {
		t.Fatalf("expected robinhood in mainnet chains")
	}
	if main.ChainID != 4663 || main.NativeCurrency != "ETH" {
		t.Errorf("robinhood mainnet = %+v, want chainID=4663 native=ETH", main)
	}
	if got := cfg.Networks["mainnet"].TokenAddresses["robinhood:USDG"]; got != usdgMainnet {
		t.Errorf("robinhood:USDG mainnet = %q, want %q", got, usdgMainnet)
	}

	test, ok := cfg.Networks["testnet"].Chains["robinhood"]
	if !ok {
		t.Fatalf("expected robinhood in testnet chains")
	}
	if test.ChainID != 46630 || test.NativeCurrency != "ETH" {
		t.Errorf("robinhood testnet = %+v, want chainID=46630 native=ETH", test)
	}
	// No verified testnet USDG address ships by default (env-gated).
	if got, present := cfg.Networks["testnet"].TokenAddresses["robinhood:USDG"]; present {
		t.Errorf("robinhood:USDG testnet should be unset by default, got %q", got)
	}

	// Wallet-only: must never be registered as a P2P chain.
	if _, isP2P := cfg.P2PChains[4663]; isP2P {
		t.Errorf("robinhood (4663) must not be in P2PChains")
	}
}

// INBOUND_MODE decides whether every naira in the platform is attributed by
// webhook or by statement poll, so a typo must land on today's behaviour
// rather than silently retiring every virtual account.
func TestNormalizeInboundMode(t *testing.T) {
	tests := map[string]string{
		"central":    InboundModeCentral,
		"CENTRAL":    InboundModeCentral,
		"  central ": InboundModeCentral,
		"va":         InboundModeVA,
		"VA":         InboundModeVA,
		"":           InboundModeVA,
		"centrl":     InboundModeVA,
		"true":       InboundModeVA,
	}
	for in, want := range tests {
		if got := normalizeInboundMode(in); got != want {
			t.Errorf("normalizeInboundMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInboundCentralEnabled(t *testing.T) {
	if (&Config{InboundMode: InboundModeCentral}).InboundCentralEnabled() != true {
		t.Error("central mode must report enabled")
	}
	if (&Config{InboundMode: InboundModeVA}).InboundCentralEnabled() != false {
		t.Error("va mode must report disabled")
	}
	var nilCfg *Config
	if nilCfg.InboundCentralEnabled() {
		t.Error("nil config must not report central mode")
	}
}

// The poll interval is the user's wait for a credit to be noticed, so an unset
// or nonsense value must fall back to the 30s of decision 8, never to zero
// (which the scheduler panics on at Register).
func TestInboundPollIntervalOrDefault(t *testing.T) {
	if got := (&Config{}).InboundPollIntervalOrDefault(); got != 30*time.Second {
		t.Errorf("unset = %s, want 30s", got)
	}
	if got := (&Config{InboundPollInterval: -time.Second}).InboundPollIntervalOrDefault(); got != 30*time.Second {
		t.Errorf("negative = %s, want 30s", got)
	}
	if got := (&Config{InboundPollInterval: 15 * time.Second}).InboundPollIntervalOrDefault(); got != 15*time.Second {
		t.Errorf("configured = %s, want 15s", got)
	}
}

// A hold is unattributed customer money whose only exit is an operator, so the
// alert list has to survive the ways an env var actually gets written: stray
// spaces, a trailing comma, the same mailbox pasted twice in different case.
// A duplicate mails the same person twice on every hold.
func TestLoad_InboundAlertEmails(t *testing.T) {
	t.Setenv("INBOUND_ALERT_EMAILS", " ops@tsaconnectworld.com , finance@tsaconnectworld.com,,OPS@tsaconnectworld.com , ")
	cfg := Load()
	want := []string{"ops@tsaconnectworld.com", "finance@tsaconnectworld.com"}
	if !reflect.DeepEqual(cfg.InboundAlertEmails, want) {
		t.Errorf("InboundAlertEmails = %v, want %v", cfg.InboundAlertEmails, want)
	}
}

// Unset means nobody is alerted. That must be an empty list rather than one
// element containing "" — a blank recipient is a send that fails on every hold
// and buries the log line that matters.
func TestLoad_InboundAlertEmailsUnset(t *testing.T) {
	t.Setenv("INBOUND_ALERT_EMAILS", "")
	if got := Load().InboundAlertEmails; len(got) != 0 {
		t.Errorf("InboundAlertEmails = %v, want empty", got)
	}
}

// LIFI_BASE_URL exists so a local rehearsal of the ramp-conversion feature can
// point every LiFi client at a stand-in server instead of the real API — the
// real API can never satisfy a status check for a transaction that only
// exists on a local chain fork. Left unset, behaviour must be byte-identical
// to the previously-hardcoded "https://li.quest/v1" everywhere that literal
// used to live (cmd/server/ramp_conversion.go, cmd/server/main.go,
// internal/handlers/swap_lifi_handler.go).
func TestResolveLiFiBaseURL_DefaultsWhenUnset(t *testing.T) {
	if got := ResolveLiFiBaseURL(""); got != "https://li.quest/v1" {
		t.Errorf("ResolveLiFiBaseURL(\"\") = %q, want %q", got, "https://li.quest/v1")
	}
}

// A configured value wins over the default.
func TestResolveLiFiBaseURL_ConfiguredValueWins(t *testing.T) {
	if got := ResolveLiFiBaseURL("http://localhost:9191/v1"); got != "http://localhost:9191/v1" {
		t.Errorf("ResolveLiFiBaseURL = %q, want %q", got, "http://localhost:9191/v1")
	}
}

// Defensive normalisation: surrounding whitespace and a trailing slash must
// not leak into the value every LiFi client builds request paths from, and a
// whitespace-only value is the same as unset — it must not silently produce
// requests to a nonsense host (e.g. "https://li.quest/v1/" + "/quote" would
// double-slash the path if the trailing slash weren't stripped).
func TestResolveLiFiBaseURL_NormalisesAndFallsBack(t *testing.T) {
	cases := map[string]string{
		"  https://stand-in.local/v1  ": "https://stand-in.local/v1",
		"https://stand-in.local/v1/":    "https://stand-in.local/v1",
		"   ":                           "https://li.quest/v1",
	}
	for in, want := range cases {
		if got := ResolveLiFiBaseURL(in); got != want {
			t.Errorf("ResolveLiFiBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Load() wires LIFI_BASE_URL through the same resolver, so the field on
// Config is never a raw, unnormalised env value.
func TestLoad_LiFiBaseURLDefaultsWhenUnset(t *testing.T) {
	t.Setenv("LIFI_BASE_URL", "")
	if got := Load().LiFiBaseURL; got != "https://li.quest/v1" {
		t.Errorf("LiFiBaseURL = %q, want default %q", got, "https://li.quest/v1")
	}
}

func TestLoad_LiFiBaseURLOverride(t *testing.T) {
	t.Setenv("LIFI_BASE_URL", " http://localhost:9191/v1/ ")
	if got := Load().LiFiBaseURL; got != "http://localhost:9191/v1" {
		t.Errorf("LiFiBaseURL = %q, want %q", got, "http://localhost:9191/v1")
	}
}

// ADMIN_URL is a comma-separated CORS list ordered dev-first. The hold alert
// needs ONE origin an on-call operator can click, so the https entry has to
// win over the localhost one that happens to be listed first.
func TestAdminConsoleBaseURL(t *testing.T) {
	tests := map[string]string{
		"http://localhost:5173,https://admin.tsaconnectworld.com": "https://admin.tsaconnectworld.com",
		"https://admin.tsaconnectworld.com/":                      "https://admin.tsaconnectworld.com",
		" http://localhost:5173 ":                                 "http://localhost:5173",
		"":                                                        "",
	}
	for in, want := range tests {
		if got := (&Config{AdminURL: in}).AdminConsoleBaseURL(); got != want {
			t.Errorf("AdminConsoleBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	var nilCfg *Config
	if got := nilCfg.AdminConsoleBaseURL(); got != "" {
		t.Errorf("nil config = %q, want empty", got)
	}
}

// IsProduction is the single comparison every raw-signer-key guard and the gin
// release-mode switch must use instead of a direct string compare against
// Env — it has to tolerate the values an operator actually types.
func TestIsProduction(t *testing.T) {
	cases := map[string]bool{
		"production":  true,
		"Production":  true,
		" production": true,
		"production ": true,
		"PRODUCTION":  true,
		"prod":        true,
		"Prod":        true,
		" prod ":      true,
		"development": false,
		"staging":     false,
		"":            false,
		"productions": false,
	}
	for env, want := range cases {
		cfg := &Config{Env: env}
		if got := cfg.IsProduction(); got != want {
			t.Errorf("IsProduction() with Env=%q = %v, want %v", env, got, want)
		}
	}

	var nilCfg *Config
	if nilCfg.IsProduction() {
		t.Errorf("nil config IsProduction() = true, want false")
	}
}

// Load() must normalise ENV the same way IsProduction() does, so a stray
// space or "Prod"/"PROD" typed into the deploy env is detected identically to
// the exact string "production".
func TestLoad_NormalisesEnv(t *testing.T) {
	// Note: t.Setenv always SETS the var (even to ""), which getEnv treats as
	// present via os.LookupEnv — so an explicit "" here is "ENV is set to
	// empty", a different case from ENV being unset, and normalises to "" not
	// the "development" default (same LookupEnv gotcha .env.example documents
	// for FLUTTERWAVE_BASE_URL). Only non-empty values are exercised here.
	cases := map[string]struct {
		wantEnv    string
		wantIsProd bool
	}{
		" Production ": {"production", true},
		"PROD":         {"production", true},
		"development":  {"development", false},
	}
	for raw, want := range cases {
		t.Setenv("ENV", raw)
		cfg := Load()
		if cfg.Env != want.wantEnv {
			t.Errorf("ENV=%q: cfg.Env = %q, want %q", raw, cfg.Env, want.wantEnv)
		}
		if cfg.IsProduction() != want.wantIsProd {
			t.Errorf("ENV=%q: cfg.IsProduction() = %v, want %v", raw, cfg.IsProduction(), want.wantIsProd)
		}
	}
}

// ALLOW_RAW_SIGNER_KEYS must default to false — the raw-key guard's fail-safe
// direction is refusing in production, not permitting it.
func TestLoad_AllowRawSignerKeysDefaultsFalse(t *testing.T) {
	t.Setenv("ALLOW_RAW_SIGNER_KEYS", "")
	if Load().AllowRawSignerKeys {
		t.Errorf("AllowRawSignerKeys = true with unset env, want false")
	}
}

func TestLoad_AllowRawSignerKeysTrue(t *testing.T) {
	t.Setenv("ALLOW_RAW_SIGNER_KEYS", "true")
	if !Load().AllowRawSignerKeys {
		t.Errorf("AllowRawSignerKeys = false with ALLOW_RAW_SIGNER_KEYS=true, want true")
	}
}

// A 0x-prefixed IT_OPERATOR_PRIVATE_KEY must not silently leave IT (and
// utility bills, which share the operator signer) dormant: crypto.HexToECDSA
// rejects the 0x prefix outright, and RELAYER/PM/RAMP keys already tolerate
// it at their point of use.
func TestLoad_StripsITOperatorKeyPrefix(t *testing.T) {
	const raw = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	t.Setenv("IT_OPERATOR_PRIVATE_KEY", "0x"+raw)
	if got := Load().ITOperatorPrivateKey; got != raw {
		t.Errorf("ITOperatorPrivateKey = %q, want %q (0x stripped)", got, raw)
	}

	t.Setenv("IT_OPERATOR_PRIVATE_KEY", raw)
	if got := Load().ITOperatorPrivateKey; got != raw {
		t.Errorf("ITOperatorPrivateKey without 0x = %q, want unchanged %q", got, raw)
	}
}

// Acting on "Flutterwave has no transfer for this reference" can pay a user
// twice if the listing is misread, so it must stay off unless asked for.
func TestLoad_ITSellFWAbsenceTrustedDefaultsOff(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "testnet")
	t.Setenv("IT_SELL_FW_ABSENCE_TRUSTED", "")
	if Load().ITSellFWAbsenceTrusted {
		t.Fatal("IT_SELL_FW_ABSENCE_TRUSTED must default to false")
	}
	t.Setenv("IT_SELL_FW_ABSENCE_TRUSTED", "true")
	if !Load().ITSellFWAbsenceTrusted {
		t.Fatal("IT_SELL_FW_ABSENCE_TRUSTED=true must enable it")
	}
}

// Every default treasury chain must ship with a pinned router: a chain that
// loads with an empty allowlist boots with no conversion lane at all.
func TestLoad_RampTreasuryRoutersDefaultToLiFiDiamond(t *testing.T) {
	// Unset, not set-to-empty: an empty value loads no chains at all, which
	// would make this loop vacuous. The real default list is what a new
	// chain gets added to, so it is the list that must be checked.
	t.Setenv("RAMP_TREASURY_CHAINS", "")
	os.Unsetenv("RAMP_TREASURY_CHAINS")
	cfg := Load()
	if len(cfg.RampTreasuryChains) == 0 {
		t.Fatal("RampTreasuryChains is empty; the default list did not load")
	}
	for _, chain := range cfg.RampTreasuryChains {
		routers := cfg.RampTreasuryRouters[chain]
		if len(routers) != 1 {
			t.Errorf("%s: routers = %v, want exactly one pinned diamond", chain, routers)
		}
	}
	if got := cfg.RampTreasuryRouters["linea"]; len(got) != 1 || got[0] != "0xde1e598b81620773454588b85d6b5d4eec32573e" {
		t.Errorf("linea routers = %v, want its own diamond", got)
	}
}

// Swapped sells check her route against the same allowlist, so narrowing
// RAMP_TREASURY_CHAINS for buys must not strip a sell's source chain of its
// pinned router — that would refuse every sell from it.
func TestLoad_RampTreasuryRoutersCoverPinnedChainsOutsideTheTreasuryList(t *testing.T) {
	t.Setenv("RAMP_TREASURY_CHAINS", "bsc")
	cfg := Load()
	if got := cfg.RampTreasuryRouters["arbitrum"]; len(got) != 1 || got[0] != "0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE" {
		t.Fatalf("arbitrum routers = %v, want the pinned diamond although arbitrum has no treasury lane", got)
	}
	if len(cfg.RampTreasuryChains) != 1 || cfg.RampTreasuryChains[0] != "bsc" {
		t.Fatalf("treasury chains = %v; the lane list itself must stay as configured", cfg.RampTreasuryChains)
	}
}

func TestLoad_RampTreasuryRoutersOverride(t *testing.T) {
	t.Setenv("RAMP_TREASURY_CHAINS", "bsc")
	t.Setenv("RAMP_TREASURY_ROUTERS_BSC", " 0x1111111111111111111111111111111111111111 , ,0x2222222222222222222222222222222222222222")
	got := Load().RampTreasuryRouters["bsc"]
	if len(got) != 2 || got[0] != "0x1111111111111111111111111111111111111111" || got[1] != "0x2222222222222222222222222222222222222222" {
		t.Errorf("bsc routers = %v, want the two overrides, trimmed, blanks dropped", got)
	}
}

// TSA Pay keeps the split it priced on before the ramp moved to the company
// take: F = 1%, B = 50 bps unless set.
func TestLoad_TSAPayFeeSplit(t *testing.T) {
	t.Setenv("TSA_PAY_FEE_PERCENT", "")
	t.Setenv("TSA_PAY_ROUND_TRIP_BUDGET_BPS", "")
	if c := Load(); c.TSAPayFeePercent != "1" || c.TSAPayRoundTripBudgetBps != 50 {
		t.Fatalf("defaults = %q / %d, want 1 / 50", c.TSAPayFeePercent, c.TSAPayRoundTripBudgetBps)
	}
	t.Setenv("TSA_PAY_FEE_PERCENT", " 0.75 ")
	t.Setenv("TSA_PAY_ROUND_TRIP_BUDGET_BPS", "35")
	if c := Load(); c.TSAPayFeePercent != "0.75" || c.TSAPayRoundTripBudgetBps != 35 {
		t.Fatalf("set = %q / %d, want 0.75 / 35", c.TSAPayFeePercent, c.TSAPayRoundTripBudgetBps)
	}
}

func TestLoad_TSAPayDefaultsOff(t *testing.T) {
	t.Setenv("TSA_PAY_ENABLED", "")
	t.Setenv("TSA_PAY_LIVE_ENABLED", "")
	t.Setenv("TSA_PAY_CHECKOUT_BASE_URL", "")
	t.Setenv("TSA_PAY_REFUNDS", "")
	cfg := Load()
	if cfg.TSAPayEnabled || cfg.TSAPayLiveEnabled || cfg.TSAPayRefundsEnabled {
		t.Fatal("TSA Pay must default off, live mode and refunds doubly so")
	}
	t.Setenv("TSA_PAY_REFUNDS", "true")
	if !Load().TSAPayRefundsEnabled {
		t.Fatal("TSA_PAY_REFUNDS=true must turn refunds on")
	}
	if cfg.TSAPayCheckoutBaseURL != "https://tsaconnectworld.com/pay" {
		t.Fatalf("checkout base = %q", cfg.TSAPayCheckoutBaseURL)
	}
}

// An unknown RAMP_PRICE_GUARD still means log, but never silently.
func TestLoad_UnknownRampPriceGuardWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	t.Setenv("RAMP_PRICE_GUARD", "enforced")
	Load()
	if !strings.Contains(buf.String(), `RAMP_PRICE_GUARD="enforced" is unknown`) {
		t.Fatalf("log = %q", buf.String())
	}
	buf.Reset()
	t.Setenv("RAMP_PRICE_GUARD", "enforce")
	Load()
	if strings.Contains(buf.String(), "RAMP_PRICE_GUARD") {
		t.Fatalf("a known mode must not warn: %q", buf.String())
	}
}

func TestLoad_CompanyWalletDefaults(t *testing.T) {
	for _, k := range []string{"RAMP_TREASURY_BUY", "RAMP_TREASURY_WALLET_PRIVATE_KEY", "RAMP_TREASURY_WALLET_CHAINS",
		"RAMP_TREASURY_MAX_USD", "RAMP_TREASURY_CHAINS", "RAMP_TREASURY_NATIVE_FLOOR_WEI_BASE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("RAMP_TREASURY_CHAINS", "polygon") // the conversion treasury elsewhere
	cfg := Load()
	if cfg.RampTreasuryBuy || cfg.RampTreasuryWalletPrivateKey != "" || cfg.RampTreasuryMaxUSD != "200" ||
		len(cfg.RampTreasuryWalletChains) != 1 || cfg.RampTreasuryWalletChains[0] != "base" {
		t.Fatalf("defaults = %v %q %v", cfg.RampTreasuryBuy, cfg.RampTreasuryMaxUSD, cfg.RampTreasuryWalletChains)
	}
	// Spec §9: the wallet's gas-floor alert reuses RAMP_TREASURY_NATIVE_FLOOR_WEI_<CHAIN>.
	if cfg.RampTreasuryNativeFloorWei["base"] != "5000000000000000" {
		t.Fatalf("base floor = %q, want the default for a wallet chain", cfg.RampTreasuryNativeFloorWei["base"])
	}
	t.Setenv("RAMP_TREASURY_WALLET_CHAINS", " Base , sonic ")
	if got := Load().RampTreasuryWalletChains; len(got) != 2 || got[0] != "base" || got[1] != "sonic" {
		t.Fatalf("chains = %v", got)
	}
}

func TestLoad_RampProviderDefaults(t *testing.T) {
	for _, k := range []string{"RAMP_FLUTTERWAVE_BUY", "RAMP_FLUTTERWAVE_MIN_TOKENS", "RAMP_FLUTTERWAVE_MAX_USD",
		"RAMP_FLUTTERWAVE_SEND_FEE_USD", "RAMP_COMPANY_TAKE_BPS", "RAMP_FUNDING_FEE_BPS", "RAMP_FLUTTERWAVE_CONVERSION_FEE_BPS",
		"RAMP_FLUTTERWAVE_SEND_FEE_MODE", "RAMP_DAILY_ABANDON_CAP", "RAMP_HONOUR_GRACE_SECONDS",
		"RAMP_TREASURY_MIN_NGN", "RAMP_TREASURY_DAILY_USER_MAX_USD", "RAMP_TREASURY_LOW_BALANCE_USD", "RAMP_TREASURY_SEND_STALL_MINUTES",
		"RAMP_TREASURY_RATE_TOL_BPS", "RAMP_TREASURY_CONFIRMATIONS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	cfg := Load()
	if cfg.RampFlutterwaveBuy || cfg.RampFlutterwaveMinTokens != "10" || cfg.RampFlutterwaveMaxUSD != "2000" ||
		cfg.RampFlutterwaveSendFeeUSD != "1.50" || cfg.RampCompanyTakeBps != 50 || cfg.RampFundingFeeBps != 140 ||
		cfg.RampFlutterwaveConversionFeeBps != 150 ||
		cfg.RampFlutterwaveSendFeeMode != "wallet" || cfg.RampDailyAbandonCap != 5 || cfg.RampHonourGrace != 10*time.Minute ||
		cfg.RampTreasuryMinNGN != "100" || cfg.RampTreasuryDailyUserMaxUSD != "500" ||
		cfg.RampTreasuryLowBalanceUSD != 50 || cfg.RampTreasurySendStall != 30*time.Minute ||
		cfg.RampTreasuryRateTolBps != 400 || cfg.RampTreasuryConfirmations != 5 {
		t.Fatalf("defaults = %+v", cfg)
	}
	t.Setenv("RAMP_FLUTTERWAVE_BUY", "true")
	t.Setenv("RAMP_FLUTTERWAVE_SEND_FEE_MODE", " Amount ")
	t.Setenv("RAMP_HONOUR_GRACE_SECONDS", "300")
	cfg = Load()
	if !cfg.RampFlutterwaveBuy || cfg.RampFlutterwaveSendFeeMode != "amount" || cfg.RampHonourGrace != 5*time.Minute {
		t.Fatalf("overrides = %v %q %s", cfg.RampFlutterwaveBuy, cfg.RampFlutterwaveSendFeeMode, cfg.RampHonourGrace)
	}
	for _, bad := range []string{"1001", "0", "-5"} {
		t.Setenv("RAMP_TREASURY_RATE_TOL_BPS", bad)
		if got := Load().RampTreasuryRateTolBps; got != 400 {
			t.Fatalf("RAMP_TREASURY_RATE_TOL_BPS=%s → %d, want the default 400", bad, got)
		}
	}
	t.Setenv("RAMP_TREASURY_RATE_TOL_BPS", "1000")
	if got := Load().RampTreasuryRateTolBps; got != 1000 {
		t.Fatalf("1000 → %d, want 1000 (the ceiling is allowed)", got)
	}
}

func TestLoad_RampTreasuryLowBalanceRejectsBadValues(t *testing.T) {
	for _, bad := range []string{"0", "-1", "NaN"} {
		t.Setenv("RAMP_TREASURY_LOW_BALANCE_USD", bad)
		if got := Load().RampTreasuryLowBalanceUSD; got != 50 {
			t.Fatalf("RAMP_TREASURY_LOW_BALANCE_USD=%s -> %v, want the default 50", bad, got)
		}
	}
	t.Setenv("RAMP_TREASURY_LOW_BALANCE_USD", "75.5")
	if got := Load().RampTreasuryLowBalanceUSD; got != 75.5 {
		t.Fatalf("75.5 -> %v", got)
	}
}

// Spec §15: the Polygon USDC Flutterwave sends is bundled per network (native
// USDC on mainnet, none on testnet), and a bad hash wait keeps the 10-minute
// default (loudly).
func TestLoad_FlutterwavePolygonUSDCAndHashWait(t *testing.T) {
	t.Setenv("RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES", "")
	os.Unsetenv("RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES")
	cfg := Load()
	if got := cfg.Networks["mainnet"].FlutterwavePolygonUSDC; got != "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359" {
		t.Fatalf("mainnet Flutterwave Polygon USDC = %q", got)
	}
	if got := cfg.Networks["testnet"].FlutterwavePolygonUSDC; got != "" {
		t.Fatalf("testnet Flutterwave Polygon USDC = %q, want none", got)
	}
	if cfg.RampFlutterwaveHashWait != 10*time.Minute {
		t.Fatalf("default hash wait = %s", cfg.RampFlutterwaveHashWait)
	}
	t.Setenv("RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES", "25")
	if got := Load().RampFlutterwaveHashWait; got != 25*time.Minute {
		t.Fatalf("override hash wait = %s", got)
	}
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	for _, bad := range []string{"0", "-3"} {
		t.Setenv("RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES", bad)
		if got := Load().RampFlutterwaveHashWait; got != 10*time.Minute {
			t.Fatalf("%s → %s, want the 10-minute default", bad, got)
		}
	}
	if !strings.Contains(buf.String(), "ramp_fw_hash_wait_invalid") {
		t.Fatalf("log = %q", buf.String())
	}
}

// RAMP_COMPANY_TAKE_BPS has one rule, resolved here for every provider: 1..500
// bps, anything else is the 50 bps default with a boot ALERT.
func TestLoad_CompanyTakeHasOneRule(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for raw, want := range map[string]int64{"": 50, "1": 1, "120": 120, "500": 500} {
		t.Setenv("RAMP_COMPANY_TAKE_BPS", raw)
		if got := Load().RampCompanyTakeBps; got != want {
			t.Fatalf("%q: take = %d, want %d", raw, got, want)
		}
	}
	if strings.Contains(logs.String(), "ramp_company_take_invalid") {
		t.Fatalf("a valid take alerted: %s", logs.String())
	}
	for _, raw := range []string{"0", "-5", "501", "9999", "0.5", "abc"} {
		logs.Reset()
		t.Setenv("RAMP_COMPANY_TAKE_BPS", raw)
		if got := Load().RampCompanyTakeBps; got != DefaultCompanyTakeBps {
			t.Fatalf("%q: take = %d, want the default %d", raw, got, DefaultCompanyTakeBps)
		}
		if !strings.Contains(logs.String(), "ALERT ramp_company_take_invalid=1") {
			t.Fatalf("%q: no ALERT, logs %q", raw, logs.String())
		}
	}
}

// A retired pricing key left in the environment is named at boot, with what
// reads it now.
func TestLoad_WarnsOnRemovedPricingKeys(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("PAYCREST_SENDER_FEE_PERCENT", "1")
	t.Setenv("RAMP_ROUND_TRIP_BUDGET_BPS", "50")
	Load()
	for _, want := range []string{
		"WARNING PAYCREST_SENDER_FEE_PERCENT is set but no longer read and is ignored", "TSA_PAY_FEE_PERCENT",
		"WARNING RAMP_ROUND_TRIP_BUDGET_BPS is set but no longer read and is ignored", "TSA_PAY_ROUND_TRIP_BUDGET_BPS",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs %q lack %q", logs.String(), want)
		}
	}
}

// A cap of 0 turns the chain's converted routes off, and the boot line says so.
func TestConvertedFeeCapFromEnv_ZeroSaysTheRoutesAreOff(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS_BSC", "0")
	if got := convertedFeeCapFromEnv("RAMP_CONVERTED_FEE_MAX_BPS_BSC", 200); got != 0 {
		t.Fatalf("cap = %d, want 0", got)
	}
	if !strings.Contains(logs.String(), "converted buys and swapped sells on bsc are off") {
		t.Fatalf("logs %q do not say bsc's routes are off", logs.String())
	}
	t.Setenv("RAMP_CONVERTED_FEE_MAX_BPS", "0")
	logs.Reset()
	convertedFeeCapFromEnv("RAMP_CONVERTED_FEE_MAX_BPS", 200)
	if !strings.Contains(logs.String(), "converted buys and swapped sells on chains without their own cap are off") {
		t.Fatalf("logs %q do not say the default chains' routes are off", logs.String())
	}
}
