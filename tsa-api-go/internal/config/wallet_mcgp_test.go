package config

import "testing"

const sonicMCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"

// Wallet MCGP is matched by chain and address, case-insensitively, on every
// network — and nothing else is.
func TestIsWalletMCGP_MatchesTheConfiguredAddressOnly(t *testing.T) {
	cfg := &Config{Networks: map[string]NetworkConfig{
		"mainnet": {
			Chains: map[string]ChainConfig{"sonic": {ChainID: 146}, "bsc": {ChainID: 56}},
			TokenAddresses: map[string]string{
				"sonic:MCGP": sonicMCGP,
				"sonic:USDT": "0x6047828dc181963ba44974801ff68e538da5eaf9",
				"bsc:USDT":   "0x55d398326f99059fF775485246999027B3197955",
			},
		},
		"testnet": {
			Chains:         map[string]ChainConfig{"sonic": {ChainID: 14601}},
			TokenAddresses: map[string]string{"sonic:MCGP": "0x00000000000000000000000000000000000000Aa"},
		},
	}}
	for _, tc := range []struct {
		name    string
		chainID int64
		token   string
		want    bool
	}{
		{"mainnet MCGP", 146, sonicMCGP, true},
		{"lower-case, padded", 146, "  0x517600323e5e2938207fa2e2e915b9d80e5b2b21 ", true},
		{"testnet MCGP", 14601, "0x00000000000000000000000000000000000000aa", true},
		{"USDT on the same chain", 146, "0x6047828dc181963ba44974801ff68e538da5eaf9", false},
		{"a look-alike MCGP elsewhere on Sonic", 146, "0x000000000000000000000000000000000000bEEF", false},
		{"the MCGP address on another chain", 56, sonicMCGP, false},
		{"unknown chain", 999, sonicMCGP, false},
		{"empty token", 146, "", false},
	} {
		if got := cfg.IsWalletMCGP(tc.chainID, tc.token); got != tc.want {
			t.Errorf("%s: IsWalletMCGP(%d, %q) = %v, want %v", tc.name, tc.chainID, tc.token, got, tc.want)
		}
	}
	var nilCfg *Config
	if nilCfg.IsWalletMCGP(146, sonicMCGP) {
		t.Error("a nil config must match nothing")
	}
}

// The loaded config really carries Sonic MCGP, so the gate is live with no
// env var set.
func TestIsWalletMCGP_LoadedConfigKnowsSonicMCGP(t *testing.T) {
	cfg := Load()
	if !cfg.IsWalletMCGP(146, sonicMCGP) {
		t.Fatal("Sonic mainnet MCGP must be recognised from the preset")
	}
	if !cfg.IsWalletMCGP(14601, sonicMCGP) {
		t.Fatal("Sonic testnet MCGP must be recognised from the preset")
	}
	if cfg.IsWalletMCGP(146, cfg.Networks["mainnet"].TokenAddresses["sonic:USDT"]) {
		t.Fatal("Sonic USDT is not MCGP")
	}
}
