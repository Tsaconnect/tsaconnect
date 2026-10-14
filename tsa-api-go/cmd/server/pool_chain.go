package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// releaseBroadcasters is one broadcaster per configured P2P chain with a client:
// the release key sends on each under its own lock (spec S8).
func releaseBroadcasters(bs *services.BlockchainService, cfg *config.Config) map[int64]services.ReleaseBroadcaster {
	out := map[int64]services.ReleaseBroadcaster{}
	if bs == nil || cfg == nil {
		return out
	}
	for id := range cfg.P2PChains {
		if c, _ := bs.ClientForChainID(id); c != nil {
			out[id] = c
		}
	}
	// The shared pool's chain: the same key fills from the vault there.
	if pool, ok := cfg.LiquidityPool(); ok {
		if c, _ := bs.ClientForChainID(pool.ChainID); c != nil {
			out[pool.ChainID] = c
		}
	}
	return out
}

// buildReleaseSigner is the PM release key with one broadcaster and one lane
// (lock + nonce floor) per P2P chain (spec S8), taken from the process's
// SignerRegistry so any other role on the same key and chain shares it, or nil
// with a log line saying why. A key shared with
// any other signer is fatal (review I8): that signer has its own lock, so the
// per-chain mutex would no longer cover every use of the key (S39).
func buildReleaseSigner(cfg *config.Config, bs *services.BlockchainService) services.ChainSender {
	if cfg.PMReleaseSigner == "kms" {
		log.Printf("[pm] KMS signer not yet implemented; PM release disabled")
		return nil
	}
	if cfg.PMReleasePrivateKey == "" {
		log.Printf("[pm] PM release disabled: PM_RELEASE_PRIVATE_KEY unset")
		return nil
	}
	if !rawSignerKeyAllowed(cfg) {
		// Same rule as the IT operator, relayer and ramp sponsor keys: a raw
		// key out of the environment is refused in production unless
		// ALLOW_RAW_SIGNER_KEYS opts back in.
		log.Printf("[pm] PM release disabled: raw PM_RELEASE_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)")
		return nil
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.PMReleasePrivateKey, "0x"))
	if err != nil {
		log.Printf("[pm] invalid PM_RELEASE_PRIVATE_KEY; PM release disabled: %v", err)
		return nil
	}
	if err := assertDistinctKey("PM_RELEASE_PRIVATE_KEY", key, signerKeysExcept(cfg, "PM_RELEASE_PRIVATE_KEY")); err != nil {
		fatalf("[config] REFUSING TO START: %v", err)
		return nil
	}
	broadcasters := releaseBroadcasters(bs, cfg)
	if len(broadcasters) == 0 {
		log.Printf("[pm] PM release disabled: no P2P chain has an RPC client")
		return nil
	}
	alertRawSignerKeyInProduction(cfg, "PM_RELEASE_PRIVATE_KEY")
	crs := services.NewChainReleaseSigner(bs.Signers(), key, broadcasters)
	log.Printf("[pm] release signer ready (admin EOA %s) chains=%v — one shared key lane per chain", crs.Address(), crs.Chains())
	// Catch a signer/admin-EOA misconfig at boot rather than at the first
	// release: adminRelease reverts on-chain when the key isn't a registered admin.
	if cfg.PMAdminEOAAddress != "" && !strings.EqualFold(crs.Address(), cfg.PMAdminEOAAddress) {
		log.Printf("[pm] WARNING: release signer address %s does not match configured PM_ADMIN_EOA_ADDRESS %s — adminRelease will revert on-chain until the key matches a registered admin", crs.Address(), cfg.PMAdminEOAAddress)
	}
	return crs
}

// buildPoolChain runs the shared pool's gates once and returns them, or a
// nil PoolGate (never a typed nil) when no pool row names a vault. A failed
// gate leaves the pool closed (ALERT logged), never the process down; a row
// with no RPC client still gets its gates: closed, and re-checked by
// registerPoolJobs until a restart brings a client.
func buildPoolChain(cfg *config.Config, bs *services.BlockchainService, release services.ChainSender, treasury *services.Treasury) services.PoolGate {
	pool, ok := cfg.LiquidityPool()
	if !ok {
		log.Printf("[liquidity] dormant: no pool row names a vault")
		return nil
	}
	var client *blockchain.EVMClient
	if bs != nil {
		client, _ = bs.ClientForChainID(pool.ChainID)
	}
	// Assigned only when non-nil: a nil *ethclient.Client in the reader
	// interface would read as a client and panic on the first gate.
	var chain services.PoolChainReader
	var receipts services.RawRPCCaller
	decimals := map[string]int{}
	if client != nil && client.Client != nil {
		chain, receipts = client.Client, client.Client.Client()
		if d, err := client.GetTokenDecimals(pool.TokenAddress); err == nil {
			decimals[pool.Token] = d
		}
	}
	admin := ""
	if release != nil {
		admin = release.Address()
	}
	var lane *services.TreasuryLane
	if treasury != nil {
		lane = treasury.Lane(pool.ChainKey)
	}
	g := services.NewPoolChainGates(services.PoolChainGatesConfig{
		ChainID: pool.ChainID, ChainKey: pool.ChainKey, Network: pool.Network, Vault: pool.Vault,
		Tokens: map[string]string{pool.Token: pool.TokenAddress}, Decimals: decimals, ReleaseAdmin: admin,
		ReleaseGasFloorWei: bigFromDecimal(pool.ReleaseGasFloorWei), Chain: chain, Receipts: receipts, TreasuryLane: lane,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := g.Check(ctx)
	log.Printf("[liquidity] pool chain=%s(%d) network=%s vault=%s decimals=%v operator=%s treasury_lane=%t open=%t failures=%d",
		pool.ChainKey, pool.ChainID, pool.Network, pool.Vault, decimals, admin, lane != nil, st.Open, len(st.Failures))
	return g
}

// registerPoolJobs re-checks every gate each 5 minutes and watches the
// operator's gas floor on the pool chain.
func registerPoolJobs(scheduler *jobs.Scheduler, g services.PoolGate) {
	if g == nil {
		return
	}
	scheduler.Register(jobs.NewReceiptProbe("liquidity-pool-gates", g, 5*time.Minute))
	scheduler.Register(jobs.NewGasFloatMonitorNamed("liquidity_pool_release", g, 10*time.Minute))
}
