package main

import (
	"log"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// poolTreasuryFor gives the shared pool's chain a conversion-treasury lane:
// it receives every fill and delivers to the buyer.
//   - mainnet pool, conversion on → the conversion treasury itself (one nonce
//     sequence per chain);
//   - conversion off              → a pool-only, router-less treasury from
//     the same key (transfers only);
//   - testnet pool, conversion on → refused: never mix a mainnet conversion
//     treasury with a testnet pool.
//
// Returns nil (the pool's treasury gate then fails, so it stays closed)
// whenever no such lane can exist.
func poolTreasuryFor(cfg *config.Config, chains *services.BlockchainService, conversion *services.Treasury) *services.Treasury {
	pool, ok := cfg.LiquidityPool()
	if !ok {
		return nil
	}
	if conversion != nil {
		if pool.Network != "mainnet" {
			log.Printf("[liquidity] ALERT liquidity_pool_network_conflict pool=%s network=%s — RAMP_CONVERSION_BUY runs a mainnet treasury; turn it off to rehearse a testnet pool",
				pool.ChainKey, pool.Network)
			return nil
		}
		if conversion.Lane(pool.ChainKey) == nil {
			log.Printf("[liquidity] ALERT liquidity_pool_treasury_lane_missing chain=%s — add it to RAMP_TREASURY_CHAINS", pool.ChainKey)
			return nil
		}
		return conversion
	}
	if chains == nil {
		return nil
	}
	key, ok := rampTreasuryKey(cfg, "[liquidity]")
	if !ok {
		return nil
	}
	caps, ok := rampTreasuryCaps(cfg)
	if !ok {
		return nil
	}
	lanes, missing := buildTreasuryLanes(cfg, chains, key, treasuryLaneSpec{
		chains: []string{pool.ChainKey}, network: pool.Network, guardPrefix: "ramp_treasury_", role: "treasury",
		logTag: "[liquidity]", metric: "liquidity_pool_treasury", needRouters: false,
	})
	lane := lanes[pool.ChainKey]
	if lane == nil {
		log.Printf("[liquidity] ALERT liquidity_pool_treasury_lane_missing chain=%s network=%s missing=%v", pool.ChainKey, pool.Network, missing)
		return nil
	}
	t := services.NewTreasury(lanes, caps, nil)
	if t.Address() == "" {
		return nil
	}
	log.Printf("[liquidity] pool treasury lane chain=%s(%d) network=%s address=%s routers=none", pool.ChainKey, lane.ChainID, pool.Network, t.Address())
	return t
}
