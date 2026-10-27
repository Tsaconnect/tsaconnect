package config

import (
	"fmt"
	"log"
	"sort"
	"strings"
)

// core_contracts.go — the audit-ready core contract sets (FeeConfig, OrderEscrow, P2PTradeEscrow;
// smart-contracts/deploy/core.js), slice 1 of docs/plans/2026-10-27-contracts-v2-migration.md.
//
// Nothing reads a set yet: this only makes the addresses known and checked at boot. CORE_CONTRACTS
// selects one: unset or "off" (the default, and what prod runs) keeps the core set dormant; "dev"
// is the throwaway set whose admin is a test deployer and must never be selected in prod; "prod"
// is the PM-owned set, which has no rows until it is deployed. Addresses live here, not in env
// (decision 17), so a set's contracts cannot drift apart.

// Core contract set names (CORE_CONTRACTS).
const (
	CoreSetOff  = "off"
	CoreSetDev  = "dev"
	CoreSetProd = "prod"
)

// CoreP2PEIP712Name / CoreP2PEIP712Version are the EIP-712 domain every P2PTradeEscrow is
// initialized with (ad vouchers are signed against it).
const (
	CoreP2PEIP712Name    = "TSAConnectP2PEscrow"
	CoreP2PEIP712Version = "3"
)

// CoreContracts is one chain's core set.
type CoreContracts struct {
	ChainID     int64
	ChainKey    string
	Network     string
	FeeConfig   string
	OrderEscrow string // empty where the chain has no marketplace orders (BSC)
	P2PEscrow   string // P2PTradeEscrow
	// Admin is the set's DEFAULT_ADMIN (every role). A dev set's admin is a throwaway deployer.
	Admin string
}

type coreRow struct {
	network     string // the cfg.Networks entry the contracts live on
	feeConfig   string
	orderEscrow string
	p2pEscrow   string
	admin       string
}

// coreSets: set name → chain key → row.
var coreSets = map[string]map[string]coreRow{
	// Deployed 2026-10-27 with CORE_DEV=1 and proven end to end on mainnet (scripts/e2e-core.js).
	// Admin is the throwaway deployer, so a dev set is for non-prod APIs only.
	CoreSetDev: {
		"sonic": {network: "mainnet",
			feeConfig:   "0x9aba9E3e3bCD5d609ef99C6dC5973386769e2572",
			orderEscrow: "0x91b85d83Ec8d9f8369c245297110a82882B7248E",
			p2pEscrow:   "0xDB4Ff053b677e9C65BD0988f74922329103FB2f3",
			admin:       "0xd963C3C596f692b4fD8e53bD9B60Ca1843400Da3"},
	},
	CoreSetProd: {},
}

// assembleCoreContracts fills cfg.CoreSet and cfg.CoreContracts from CORE_CONTRACTS. It runs after
// assembleP2PChains, because a row must sit on the network its chain was selected on: the dev set
// lives on Sonic mainnet, so it cannot be paired with SONIC_NETWORK=testnet.
func assembleCoreContracts(cfg *Config, sets map[string]map[string]coreRow) error {
	set := strings.ToLower(strings.TrimSpace(getEnv("CORE_CONTRACTS", CoreSetOff)))
	if set == "" {
		set = CoreSetOff
	}
	cfg.CoreSet = set
	cfg.CoreContracts = map[int64]CoreContracts{}
	if set == CoreSetOff {
		log.Printf("[config] core contracts dormant (CORE_CONTRACTS unset)")
		return nil
	}
	rows, ok := sets[set]
	if !ok {
		return fmt.Errorf("CORE_CONTRACTS=%q: want %q, %q or %q", set, CoreSetOff, CoreSetDev, CoreSetProd)
	}
	if len(rows) == 0 {
		return fmt.Errorf("CORE_CONTRACTS=%s: that set has no contracts yet", set)
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, chainKey := range keys {
		r := rows[chainKey]
		if selected := cfg.p2pNetworks[chainKey]; selected != "" && selected != r.network {
			return fmt.Errorf("CORE_CONTRACTS=%s: the %s set is on %s, but %s_NETWORK selects %s",
				set, chainKey, r.network, strings.ToUpper(chainKey), selected)
		}
		chain, ok := cfg.Networks[r.network].Chains[chainKey]
		if !ok {
			return fmt.Errorf("CORE_CONTRACTS=%s: cfg.Networks has no %s chain on %s", set, chainKey, r.network)
		}
		for label, addr := range map[string]string{"FeeConfig": r.feeConfig, "P2PTradeEscrow": r.p2pEscrow, "admin": r.admin} {
			if err := validateContractAddress(fmt.Sprintf("core %s %s %s", set, chainKey, label), addr); err != nil {
				return err
			}
		}
		if r.orderEscrow != "" {
			if err := validateContractAddress(fmt.Sprintf("core %s %s OrderEscrow", set, chainKey), r.orderEscrow); err != nil {
				return err
			}
		}
		cfg.CoreContracts[chain.ChainID] = CoreContracts{ChainID: chain.ChainID, ChainKey: chainKey, Network: r.network,
			FeeConfig: r.feeConfig, OrderEscrow: r.orderEscrow, P2PEscrow: r.p2pEscrow, Admin: r.admin}
		log.Printf("[config] core contracts %s on %s(%d): FeeConfig=%s OrderEscrow=%s P2PTradeEscrow=%s admin=%s",
			set, chainKey, chain.ChainID, r.feeConfig, r.orderEscrow, r.p2pEscrow, r.admin)
	}
	if set == CoreSetDev {
		log.Printf("[config] ALERT core_contracts_dev: the DEV core set is selected — its admin is a test key; never run this in prod")
	}
	return nil
}
