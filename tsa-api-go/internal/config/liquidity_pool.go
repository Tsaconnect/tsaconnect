package config

import (
	"fmt"
	"log"
	"math/big"
	"strings"
)

// The shared liquidity pool (spec 2026-10-14): one TsaLiquidityPool vault on
// one chain, selected by that chain's <CHAIN>_NETWORK. It is its own preset —
// not a P2P escrow row — so P2P on the same chain is configured independently.
// A row with no vault leaves the pool dormant (decision 17: the configuration
// is the switch; a hot-fix is a code change, never an env var).

// LiquidityPoolConfig is the selected pool row.
type LiquidityPoolConfig struct {
	ChainID          int64
	ChainKey         string // blockchainService key: "bsc"
	Network          string // the cfg.Networks entry
	Vault            string // the TsaLiquidityPool proxy
	VaultDeployBlock uint64 // where the ledger indexer starts
	Token            string // the pool token's symbol ("USDT")
	TokenAddress     string
	// ReleaseGasFloorWei is the operator's native alert floor on the chain.
	ReleaseGasFloorWei string
	LogChunkBlocks     uint64 // ranged eth_getLogs window
}

type liquidityPoolPreset struct {
	vault              string
	vaultDeployBlock   uint64
	token              string
	releaseGasFloorWei string
	logChunkBlocks     uint64
}

// liquidityPoolPresets: BSC first (decision 6). Empty vaults: dormant until
// deploy/liquidity-pool.js has run and its address is entered here.
var liquidityPoolPresets = map[string]map[string]liquidityPoolPreset{
	"bsc": {
		// Fresh no-collateral pool, deployed 2026-10-15 by its owner 0xB3D5…2834:
		// refill wallet/guardian 0x62bA…1b96, operator 0x38cE…2c9, recipient the
		// treasury 0xA412…E205. (The 2026-10-14 pool 0xA786…60f1 is retired, empty.)
		"mainnet": {vault: "0xb825098854d8001FAD54C4267a833b4D182793D8", vaultDeployBlock: 127783089,
			token: "USDT", releaseGasFloorWei: "10000000000000000", logChunkBlocks: 2000}, // 0.01 BNB
		// BSC testnet rehearsal vault (2026-10-14): owner/provider/guardian are
		// the throwaway LIQ_C2 deployer, operator the LIQ_C1 release key.
		"testnet": {vault: "0x3Aa24E530CffEb797140d03907117f9C3653403C", vaultDeployBlock: 136773203,
			token: "USDT", releaseGasFloorWei: "10000000000000000", logChunkBlocks: 2000},
	},
}

// assembleLiquidityPool sets cfg.LiquidityPoolRow from presets: at most one chain
// may name a vault.
func assembleLiquidityPool(cfg *Config, presets map[string]map[string]liquidityPoolPreset) error {
	cfg.LiquidityPoolRow = nil
	for chainKey, rows := range presets {
		network := strings.ToLower(strings.TrimSpace(getEnv(strings.ToUpper(chainKey)+"_NETWORK", "mainnet")))
		if _, ok := rows[network]; !ok {
			network = "mainnet"
		}
		p := rows[network]
		if strings.TrimSpace(p.vault) == "" {
			log.Printf("[config] liquidity pool %s dormant on %s: no vault in its preset row", chainKey, network)
			continue
		}
		if cfg.LiquidityPoolRow != nil {
			return fmt.Errorf("liquidity pool: %s and %s both name a vault — one pool chain at a time", cfg.LiquidityPoolRow.ChainKey, chainKey)
		}
		if err := validateContractAddress(chainKey+" "+network+" pool vault", p.vault); err != nil {
			return err
		}
		if p.vaultDeployBlock == 0 {
			return fmt.Errorf("liquidity pool %s %s: a vault needs its deploy block (the ledger indexer starts there)", chainKey, network)
		}
		chain, ok := cfg.Networks[network].Chains[chainKey]
		if !ok {
			return fmt.Errorf("liquidity pool %s has a %s row but cfg.Networks has no %s chain", chainKey, network, chainKey)
		}
		token := strings.ToUpper(strings.TrimSpace(p.token))
		addr := cfg.Networks[network].TokenAddresses[chainKey+":"+token]
		if addr == "" {
			return fmt.Errorf("liquidity pool token %s has no address on %s %s (cfg.Networks)", token, network, chainKey)
		}
		if f, ok := new(big.Int).SetString(p.releaseGasFloorWei, 10); !ok || f.Sign() <= 0 {
			return fmt.Errorf("liquidity pool %s %s: invalid operator gas floor %q", chainKey, network, p.releaseGasFloorWei)
		}
		cfg.LiquidityPoolRow = &LiquidityPoolConfig{ChainID: chain.ChainID, ChainKey: chainKey, Network: network,
			Vault: p.vault, VaultDeployBlock: p.vaultDeployBlock, Token: token, TokenAddress: addr,
			ReleaseGasFloorWei: p.releaseGasFloorWei, LogChunkBlocks: p.logChunkBlocks}
		log.Printf("[config] liquidity pool %s(%d) network=%s vault=%s token=%s", chainKey, chain.ChainID, network, p.vault, token)
	}
	return nil
}

// LiquidityPool is the pool row with a vault, if any.
func (c *Config) LiquidityPool() (LiquidityPoolConfig, bool) {
	if c == nil || c.LiquidityPoolRow == nil {
		return LiquidityPoolConfig{}, false
	}
	return *c.LiquidityPoolRow, true
}
