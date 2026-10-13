package main

import (
	"log"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// poolTreasuryFor gives the TSA Liquidity pool chain a conversion-treasury lane
// that may call the pool escrow (spec 2026-10-08 S21, §5, plan P14):
//   - mainnet pool, conversion on  → the conversion treasury itself (one nonce
//     sequence per chain, R3), its pool-chain lane allowed to call the escrow;
//   - conversion off               → a pool-only, router-less treasury from
//     the same key (only transfers and escrow calls);
//   - testnet pool, conversion on  → refused: never mix a mainnet conversion
//     treasury with a testnet pool.
//
// Returns nil (pool gate 4 then fails, so the pool stays closed) whenever no
// such lane can exist.
func poolTreasuryFor(cfg *config.Config, chains *services.BlockchainService, conversion *services.Treasury) *services.Treasury {
	pool, ok := cfg.PoolChain()
	if !ok {
		return nil
	}
	escrow := common.HexToAddress(pool.EscrowAddress)
	if conversion != nil {
		if pool.Network != "mainnet" {
			log.Printf("[liquidity] ALERT liquidity_pool_network_conflict pool=%s network=%s — RAMP_CONVERSION_BUY runs a mainnet treasury; turn it off to rehearse a testnet pool",
				pool.ChainKey, pool.Network)
			return nil
		}
		lane := conversion.Lane(pool.ChainKey)
		if lane == nil {
			log.Printf("[liquidity] ALERT liquidity_pool_treasury_lane_missing chain=%s — add it to RAMP_TREASURY_CHAINS", pool.ChainKey)
			return nil
		}
		lane.EscrowCalls = []common.Address{escrow}
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
	lane.EscrowCalls = []common.Address{escrow}
	t := services.NewTreasury(lanes, caps, nil)
	if t.Address() == "" {
		return nil
	}
	log.Printf("[liquidity] pool treasury lane chain=%s(%d) network=%s address=%s routers=none escrow_calls=%s",
		pool.ChainKey, lane.ChainID, pool.Network, t.Address(), escrow.Hex())
	return t
}
