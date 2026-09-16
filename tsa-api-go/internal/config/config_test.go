package config

import (
	"reflect"
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
	if !reflect.DeepEqual(sonic.Assets, []string{"USDT", "USDC", "MCGP"}) {
		t.Errorf("sonic assets = %v, want [USDT USDC MCGP]", sonic.Assets)
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
