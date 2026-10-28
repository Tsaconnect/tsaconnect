package config

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
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

// coreDevAllowEnv must be "true" for the dev set to load. Prod runs ENV=development, so ENV cannot
// tell a dev API from prod; a second, deliberately named switch keeps a copied CORE_CONTRACTS=dev
// from pointing prod at a set whose admin is a test key.
const coreDevAllowEnv = "CORE_CONTRACTS_ALLOW_DEV"

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
	// DeployBlock is where event indexers start (the set's first contract; Sonic's RPC returns no
	// logs for whole-range queries, so they must not scan from 0).
	DeployBlock uint64
}

type coreRow struct {
	network     string // the cfg.Networks entry the contracts live on
	feeConfig   string
	orderEscrow string
	p2pEscrow   string
	admin       string
	deployBlock uint64
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
			admin:       "0xd963C3C596f692b4fD8e53bD9B60Ca1843400Da3",
			deployBlock: 81691442},
	},
	CoreSetProd: {},
}

// assembleCoreContracts fills cfg.CoreSet and cfg.CoreContracts from CORE_CONTRACTS. It runs after
// assembleP2PChains, because a row must sit on the network its chain was selected on: the dev set
// lives on Sonic mainnet, so it cannot be paired with SONIC_NETWORK=testnet.
//
// A selector that cannot be honoured (unknown value, dev without its allow switch, a set not yet
// deployed) alerts and stays dormant rather than failing boot: dormant is what prod runs, so a bad
// value can never crash-loop it. A broken row in a set is a code bug and does fail boot.
func assembleCoreContracts(cfg *Config, sets map[string]map[string]coreRow) error {
	raw := strings.TrimSpace(getEnv("CORE_CONTRACTS", ""))
	set := strings.ToLower(raw)
	cfg.CoreSet = CoreSetOff
	cfg.CoreContracts = map[int64]CoreContracts{}
	dormant := func(why string) error {
		log.Printf("[config] core contracts dormant (%s)", why)
		return nil
	}
	switch set {
	case "", CoreSetOff, "false", "0", "no", "none", "disabled":
		if raw == "" {
			return dormant("CORE_CONTRACTS unset")
		}
		return dormant(fmt.Sprintf("CORE_CONTRACTS=%s", raw))
	}
	rows, ok := sets[set]
	if !ok {
		log.Printf("[config] ALERT core_contracts_unknown: CORE_CONTRACTS=%q is not %q, %q or %q", raw, CoreSetOff, CoreSetDev, CoreSetProd)
		return dormant("unknown set")
	}
	if len(rows) == 0 {
		log.Printf("[config] ALERT core_contracts_empty: CORE_CONTRACTS=%s has no contracts yet", set)
		return dormant("empty set")
	}
	if set == CoreSetDev && !getEnvBool(coreDevAllowEnv, false) {
		log.Printf("[config] ALERT core_contracts_dev_refused: CORE_CONTRACTS=dev needs %s=true (its admin is a test key; never in prod)", coreDevAllowEnv)
		return dormant("dev set not allowed")
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	contracts := map[int64]CoreContracts{}
	for _, chainKey := range keys {
		r := rows[chainKey]
		selected := cfg.p2pNetworks[chainKey]
		if selected == "" {
			return fmt.Errorf("CORE_CONTRACTS=%s: %s has no selected network to check the set against", set, chainKey)
		}
		if selected != r.network {
			return fmt.Errorf("CORE_CONTRACTS=%s: the %s set is on %s, but %s_NETWORK selects %s",
				set, chainKey, r.network, strings.ToUpper(chainKey), selected)
		}
		chain, ok := cfg.Networks[r.network].Chains[chainKey]
		if !ok {
			return fmt.Errorf("CORE_CONTRACTS=%s: cfg.Networks has no %s chain on %s", set, chainKey, r.network)
		}
		if err := r.validate(fmt.Sprintf("core %s %s", set, chainKey)); err != nil {
			return err
		}
		contracts[chain.ChainID] = CoreContracts{ChainID: chain.ChainID, ChainKey: chainKey, Network: r.network,
			FeeConfig: r.feeConfig, OrderEscrow: r.orderEscrow, P2PEscrow: r.p2pEscrow, Admin: r.admin, DeployBlock: r.deployBlock}
		log.Printf("[config] core contracts %s on %s(%d): FeeConfig=%s OrderEscrow=%s P2PTradeEscrow=%s admin=%s deployBlock=%d",
			set, chainKey, chain.ChainID, r.feeConfig, r.orderEscrow, r.p2pEscrow, r.admin, r.deployBlock)
	}
	cfg.CoreSet, cfg.CoreContracts = set, contracts
	if set == CoreSetDev {
		log.Printf("[config] ALERT core_contracts_dev: the DEV core set is selected — its admin is a test key; never run this in prod")
	}
	return nil
}

// validate checks a row's addresses in a fixed order (OrderEscrow is optional) and its deploy block.
func (r coreRow) validate(label string) error {
	for _, f := range []struct {
		name, addr string
		optional   bool
	}{
		{"FeeConfig", r.feeConfig, false},
		{"OrderEscrow", r.orderEscrow, true},
		{"P2PTradeEscrow", r.p2pEscrow, false},
		{"admin", r.admin, false},
	} {
		if f.optional && f.addr == "" {
			continue
		}
		if !common.IsHexAddress(f.addr) || common.HexToAddress(f.addr) == (common.Address{}) {
			return fmt.Errorf("%s %s %q is not a valid non-zero address; fix the coreSets row", label, f.name, f.addr)
		}
	}
	if r.deployBlock == 0 {
		return fmt.Errorf("%s: a set needs its deploy block (event indexers start there)", label)
	}
	return nil
}
