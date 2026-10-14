package config

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

const P2PPurposeP2P = "p2p"

// bscPresets: ordinary P2P on BSC. Empty rows: dormant. (The shared
// liquidity pool on BSC is its own preset, liquidity_pool.go.)
var bscPresets = map[string]chainPreset{
	"mainnet": {},
	"testnet": {},
}

// p2pPresets is every P2P chain, one row per network. A new chain is a new
// entry here (spec S36) — nothing else.
var p2pPresets = map[string]map[string]chainPreset{
	"sonic": sonicPresets,
	"bsc":   bscPresets,
}

// p2pNetwork is <CHAIN>_NETWORK (default mainnet). An unknown value falls back
// to mainnet with an ALERT, as SONIC_NETWORK always has.
func p2pNetwork(chainKey string, rows map[string]chainPreset) string {
	env := strings.ToUpper(chainKey) + "_NETWORK"
	n := strings.ToLower(strings.TrimSpace(getEnv(env, "mainnet")))
	if n == "" {
		n = "mainnet"
	}
	if _, ok := rows[n]; !ok {
		log.Printf("[config] ALERT p2p_network_unknown %s=%q — using mainnet", env, n)
		return "mainnet"
	}
	return n
}

// retiredP2PEnv refuses the old BSC escrow aliases: the BSC row is now the
// liquidity pool, and its addresses live in bscPresets (spec S36, plan P5).
func retiredP2PEnv() error {
	for _, env := range []string{"P2P_ESCROW_ADDRESS_BSC", "P2P_ESCROW_EIP712_VERSION_BSC"} {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return fmt.Errorf("%s is retired: the BSC row is the liquidity pool and its addresses live in internal/config/p2p_presets.go — unset it", env)
		}
	}
	return nil
}

// assembleP2PChains builds cfg.P2PChains from presets, one row per chain on its
// selected network.
func assembleP2PChains(cfg *Config, presets map[string]map[string]chainPreset) error {
	cfg.P2PChains = map[int64]P2PChainConfig{}
	cfg.p2pNetworks = map[string]string{}
	keys := make([]string, 0, len(presets))
	for k := range presets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, chainKey := range keys {
		rows := presets[chainKey]
		network := p2pNetwork(chainKey, rows)
		cfg.p2pNetworks[chainKey] = network
		if err := addP2PChain(cfg, chainKey, network, rows[network]); err != nil {
			return err
		}
	}
	return nil
}

// addP2PChain adds one preset row (spec §5: "new chain = one preset row").
func addP2PChain(cfg *Config, chainKey, network string, p chainPreset) error {
	escrow := strings.TrimSpace(p.p2pEscrow)
	if p.escrowEnv != "" {
		escrow = strings.TrimSpace(getEnv(p.escrowEnv, escrow))
	}
	version := p.eip712Version
	if p.versionEnv != "" {
		version = getEnv(p.versionEnv, version)
	}
	if escrow == "" {
		log.Printf("[config] P2P %s dormant on %s: no escrow in its preset row", chainKey, network)
		return nil
	}
	if err := validateContractAddress(chainKey+" "+network+" escrow", escrow); err != nil {
		return err
	}
	chain, ok := cfg.Networks[network].Chains[chainKey]
	if !ok {
		return fmt.Errorf("P2P %s has a %s preset row but cfg.Networks has no %s chain", chainKey, network, chainKey)
	}
	chainID := chain.ChainID
	if p.chainID != 0 {
		chainID = p.chainID
	}
	if p.chainIDEnv != "" {
		chainID = getEnvInt64(p.chainIDEnv, chainID)
	}
	purpose := p.purpose
	if purpose == "" {
		purpose = P2PPurposeP2P
	}
	cc := P2PChainConfig{ChainID: chainID, ChainKey: chainKey, Network: network, EscrowAddress: escrow,
		EIP712Version: version, Assets: append([]string(nil), p.assets...), Purpose: purpose}
	if prev, dup := cfg.P2PChains[chainID]; dup {
		return fmt.Errorf("chain %d has two P2P rows (%s and %s): one escrow per chain (spec S5)", chainID, prev.ChainKey, chainKey)
	}
	cfg.P2PChains[chainID] = cc
	log.Printf("[config] P2P %s(%d) network=%s purpose=%s escrow=%s", chainKey, chainID, network, purpose, escrow)
	return nil
}

// validateContractAddress refuses a preset (or legacy-alias) contract address
// that is not a valid, non-zero 20-byte hex address. Same rule as
// validateFeeManagerAddress, with a neutral message (review m6).
func validateContractAddress(label, addr string) error {
	if !common.IsHexAddress(addr) {
		return fmt.Errorf("%s %q is not a valid hex address; fix the preset row or the env alias", label, addr)
	}
	if common.HexToAddress(addr) == (common.Address{}) {
		return fmt.Errorf("%s %q is the zero address", label, addr)
	}
	return nil
}

// assembleFeeManagerChains is the FEE_MANAGER_ADDRESS_* registry, moved
// unchanged from Load except that BSC's chain id is its selected network's.
//
// DORMANT-BY-DEFAULT: a chain is registered only when its FeeManager address
// is configured. Sonic uses the Sonic chainID active for this boot; BSC uses
// the chain id of its selected network. UNSET addresses keep the admin set-fee
// flow + drift reconciler dormant for that chain (the endpoints surface "not
// configured"). A configured FEE_MANAGER_ADDRESS_* MUST be a syntactically
// valid, non-zero hex address. common.HexToAddress silently pads/truncates a
// malformed value (e.g. "0x123" → 0x0000…0123, or extra nibbles dropped), which
// would make the admin set-fee flow build setRate txs against the WRONG
// contract and the drift reconciler read a phantom rate. Fail LOUDLY at boot
// instead (design §6).
func assembleFeeManagerChains(cfg *Config) {
	cfg.FeeManagerChains = map[int64]FeeManagerChainConfig{}
	if fmSonic := strings.TrimSpace(getEnv("FEE_MANAGER_ADDRESS_SONIC", "")); fmSonic != "" {
		if err := validateFeeManagerAddress("FEE_MANAGER_ADDRESS_SONIC", fmSonic); err != nil {
			log.Fatalf("[config] %v", err)
		}
		cfg.FeeManagerChains[cfg.SonicChainID] = FeeManagerChainConfig{ChainID: cfg.SonicChainID, ChainKey: "sonic", Address: fmSonic}
	}
	if fmBSC := strings.TrimSpace(getEnv("FEE_MANAGER_ADDRESS_BSC", "")); fmBSC != "" {
		if err := validateFeeManagerAddress("FEE_MANAGER_ADDRESS_BSC", fmBSC); err != nil {
			log.Fatalf("[config] %v", err)
		}
		bscID := cfg.Networks[cfg.selectedP2PNetwork("bsc")].Chains["bsc"].ChainID
		cfg.FeeManagerChains[bscID] = FeeManagerChainConfig{ChainID: bscID, ChainKey: "bsc", Address: fmBSC}
	}
	if len(cfg.FeeManagerChains) == 0 {
		log.Printf("[config] FeeManager dormant: no FEE_MANAGER_ADDRESS_* configured — admin set-fee flow + drift reconciler off")
	} else {
		log.Printf("[config] FeeManager chains configured: %d", len(cfg.FeeManagerChains))
	}
}

// selectedP2PNetwork is the network assembleP2PChains chose for chainKey
// (mainnet when it has not run): the one choice, never a second env read.
func (c *Config) selectedP2PNetwork(chainKey string) string {
	if n := c.p2pNetworks[chainKey]; n != "" {
		return n
	}
	return "mainnet"
}
