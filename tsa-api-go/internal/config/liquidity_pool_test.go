package config

import (
	"strings"
	"testing"
)

const poolVaultAddr = "0x00000000000000000000000000000000000BA017"

func poolRows(vault string, block uint64) map[string]map[string]liquidityPoolPreset {
	return map[string]map[string]liquidityPoolPreset{"bsc": {"mainnet": {vault: vault, vaultDeployBlock: block, token: "USDT",
		releaseGasFloorWei: "10000000000000000", logChunkBlocks: 2000}}}
}

func TestLiquidityPool_RowWithAVault(t *testing.T) {
	t.Setenv("BSC_NETWORK", "")
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleLiquidityPool(cfg, poolRows(poolVaultAddr, 123)); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	pool, ok := cfg.LiquidityPool()
	if !ok || pool.ChainID != 56 || pool.ChainKey != "bsc" || pool.Vault != poolVaultAddr || pool.VaultDeployBlock != 123 ||
		pool.Token != "USDT" || pool.TokenAddress != bscUSDT || pool.LogChunkBlocks != 2000 {
		t.Fatalf("pool = %+v ok=%t", pool, ok)
	}
}

func TestLiquidityPool_NoVaultIsDormant(t *testing.T) {
	cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, map[string]string{"bsc:USDT": bscUSDT})}
	if err := assembleLiquidityPool(cfg, poolRows("", 0)); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if _, ok := cfg.LiquidityPool(); ok {
		t.Fatal("a row with no vault must be dormant")
	}
}

func TestLiquidityPool_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		vault  string
		block  uint64
		tokens map[string]string
		want   string
	}{
		"bad address":     {"0x123", 1, map[string]string{"bsc:USDT": bscUSDT}, "pool vault"},
		"zero address":    {"0x0000000000000000000000000000000000000000", 1, map[string]string{"bsc:USDT": bscUSDT}, "zero address"},
		"no deploy block": {poolVaultAddr, 0, map[string]string{"bsc:USDT": bscUSDT}, "deploy block"},
		"no token":        {poolVaultAddr, 1, map[string]string{}, "no address"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Networks: netsWith(map[string]ChainConfig{"bsc": {ChainID: 56}}, tc.tokens)}
			err := assembleLiquidityPool(cfg, poolRows(tc.vault, tc.block))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
