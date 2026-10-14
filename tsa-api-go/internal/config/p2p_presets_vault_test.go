package config

import (
	"strings"
	"testing"
)

const poolVaultAddr = "0x00000000000000000000000000000000000BA017"

func vaultRow(vault string, block uint64) chainPreset {
	row := poolRow(poolEscrowAddr)
	pool := *row.pool
	pool.vault, pool.vaultDeployBlock = vault, block
	row.pool = &pool
	return row
}

func TestP2PPresets_PoolVaultCarriedThrough(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": vaultRow(poolVaultAddr, 123)}}); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	pc, ok := cfg.PoolChain()
	if !ok || pc.Pool.Vault != poolVaultAddr || pc.Pool.VaultDeployBlock != 123 {
		t.Fatalf("pool = %+v", pc.Pool)
	}
}

func TestP2PPresets_PoolVaultRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		vault string
		block uint64
		want  string
	}{
		"bad address":     {"0x123", 1, "pool vault"},
		"zero address":    {"0x0000000000000000000000000000000000000000", 1, "zero address"},
		"no deploy block": {poolVaultAddr, 0, "deploy block"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
			err := assembleP2PChains(cfg, map[string]map[string]chainPreset{"bsc": {"mainnet": vaultRow(tc.vault, tc.block)}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
