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

// Audiences of a P2P escrow row: who may start NEW activity on it
// (services/p2p_audience.go). all: anyone; pm: Payment Merchants only (direct
// P2P has moved to BSC); direct: anyone but a PM (PM automation needs a release
// key that must never be an admin on the direct escrow). Exits are never gated.
const (
	P2PAudienceAll    = "all"
	P2PAudiencePM     = "pm"
	P2PAudienceDirect = "direct"
)

// bscPresets: direct (user-to-user) P2P on BSC (decisions 2026-10-12): a
// P2PEscrowV3 with its OWN FeeManager, whose admins are dispute arbiters only.
// Empty rows: dormant. The mainnet row is filled at the switch (plan API-11),
// with expectedOwner = the owner the deploy handed both contracts to; the
// escrow is registered only if every direct gate passes at boot
// (services/p2p_direct_gates.go). The shared liquidity pool on BSC is its own
// preset (liquidity_pool.go) and is not a P2P escrow.
var bscPresets = map[string]chainPreset{
	"mainnet": {p2pEscrow: "", eip712Version: "2", feeManager: "", expectedOwner: "",
		audience: P2PAudienceDirect, assets: []string{"USDT", "USDC"}},
	// Testnet rehearsal (OPS-2) 2026-10-17: deployed by deploy/p2p-direct.js
	// with owner = deployer, arbiter 0x2a88…0A0D, 1.9% p2p_platform.
	"testnet": {p2pEscrow: "0xD16c1597EA3be8D2017698CC5d6Bf53b595EEb03", eip712Version: "2",
		feeManager: "0xB4B5E6F054b38f3D2E9193B1eD182A17135FE039", expectedOwner: "0xB3D5B867453bC6aD5534C76Ab47655685AcF2834",
		audience: P2PAudienceDirect, assets: []string{"USDT", "USDC"}},
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

// retiredP2PEnv refuses the old BSC escrow aliases: BSC's escrow addresses
// live in bscPresets, never in env (spec S36, plan P5, decision 17).
func retiredP2PEnv() error {
	for _, env := range []string{"P2P_ESCROW_ADDRESS_BSC", "P2P_ESCROW_EIP712_VERSION_BSC"} {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return fmt.Errorf("%s is retired: the BSC escrow addresses live in internal/config/p2p_presets.go — unset it", env)
		}
	}
	return nil
}

// assembleP2PChains builds cfg.P2PChains from presets, one row per chain on its
// selected network.
func assembleP2PChains(cfg *Config, presets map[string]map[string]chainPreset) error {
	cfg.P2PChains = map[int64]P2PChainConfig{}
	cfg.DormantP2PChains = nil
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
	audience := strings.TrimSpace(p.audience)
	if audience == "" {
		audience = P2PAudienceAll
	}
	switch audience {
	case P2PAudienceAll, P2PAudiencePM, P2PAudienceDirect:
	default:
		return fmt.Errorf("P2P %s %s row has an unknown audience %q", chainKey, network, audience)
	}
	if escrow == "" {
		log.Printf("[config] P2P %s dormant on %s: no escrow in its preset row", chainKey, network)
		if chain, ok := cfg.Networks[network].Chains[chainKey]; ok {
			cfg.DormantP2PChains = append(cfg.DormantP2PChains,
				P2PChainConfig{ChainID: chain.ChainID, ChainKey: chainKey, Network: network, Audience: audience})
		}
		return nil
	}
	if err := validateContractAddress(chainKey+" "+network+" escrow", escrow); err != nil {
		return err
	}
	for label, addr := range map[string]string{"FeeManager": p.feeManager, "expectedOwner": p.expectedOwner} {
		if strings.TrimSpace(addr) == "" {
			continue
		}
		if err := validateContractAddress(chainKey+" "+network+" "+label, strings.TrimSpace(addr)); err != nil {
			return err
		}
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
		EIP712Version: version, Assets: append([]string(nil), p.assets...), Purpose: purpose, Audience: audience,
		FeeManager: strings.TrimSpace(p.feeManager), ExpectedOwner: strings.TrimSpace(p.expectedOwner)}
	if prev, dup := cfg.P2PChains[chainID]; dup {
		return fmt.Errorf("chain %d has two P2P rows (%s and %s): one escrow per chain (spec S5)", chainID, prev.ChainKey, chainKey)
	}
	cfg.P2PChains[chainID] = cc
	log.Printf("[config] P2P %s(%d) network=%s audience=%s escrow=%s", chainKey, chainID, network, audience, escrow)
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
	bscID := cfg.Networks[cfg.selectedP2PNetwork("bsc")].Chains["bsc"].ChainID
	fmBSC, err := bscFeeManager(strings.TrimSpace(getEnv("FEE_MANAGER_ADDRESS_BSC", "")), cfg.P2PChains[bscID].FeeManager)
	if err != nil {
		log.Fatalf("[config] %v", err)
	}
	if fmBSC != "" {
		cfg.FeeManagerChains[bscID] = FeeManagerChainConfig{ChainID: bscID, ChainKey: "bsc", Address: fmBSC}
	}
	if len(cfg.FeeManagerChains) == 0 {
		log.Printf("[config] FeeManager dormant: no FEE_MANAGER_ADDRESS_* configured — admin set-fee flow + drift reconciler off")
	} else {
		log.Printf("[config] FeeManager chains configured: %d", len(cfg.FeeManagerChains))
	}
}

// bscFeeManager is BSC's FeeManager: the direct preset row's when it names one
// (decision 17: addresses live in code), else FEE_MANAGER_ADDRESS_BSC. Both
// set and different is fatal — the admin fee flow would push rates to one
// contract while the escrow reads another.
func bscFeeManager(env, preset string) (string, error) {
	if env != "" {
		if err := validateFeeManagerAddress("FEE_MANAGER_ADDRESS_BSC", env); err != nil {
			return "", err
		}
	}
	switch {
	case preset == "":
		return env, nil
	case env != "" && !strings.EqualFold(env, preset):
		return "", fmt.Errorf("FEE_MANAGER_ADDRESS_BSC %s differs from the BSC direct preset FeeManager %s — unset it (the preset is the source of truth)", env, preset)
	}
	return preset, nil
}

// selectedP2PNetwork is the network assembleP2PChains chose for chainKey
// (mainnet when it has not run): the one choice, never a second env read.
func (c *Config) selectedP2PNetwork(chainKey string) string {
	if n := c.p2pNetworks[chainKey]; n != "" {
		return n
	}
	return "mainnet"
}
