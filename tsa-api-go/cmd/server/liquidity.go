package main

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// poolToken is the shared pool's token, its decimals read on-chain (never a
// table value: the vault's units are the token's own).
func poolToken(cfg *config.Config, bs *services.BlockchainService) (services.PoolToken, bool) {
	pool, ok := cfg.LiquidityPool()
	if !ok || bs == nil {
		return services.PoolToken{}, false
	}
	client, _ := bs.ClientForChainID(pool.ChainID)
	if client == nil {
		return services.PoolToken{}, false
	}
	d, err := client.GetTokenDecimals(pool.TokenAddress)
	if err != nil || d < services.StableAssetDecimals {
		log.Printf("[liquidity] ALERT liquidity_pool_token_unreadable chain=%s token=%s decimals=%d err=%v", pool.ChainKey, pool.Token, d, err)
		return services.PoolToken{}, false
	}
	return services.PoolToken{ChainID: pool.ChainID, ChainKey: pool.ChainKey, Symbol: pool.Token, Address: pool.TokenAddress, Decimals: d}, true
}

// wireVaultLiquidity is the shared pool's LP side (spec 2026-10-14 §5, §6):
// nil (dormant, reason logged) unless the chain client, the operator key and
// the pool token all exist.
func wireVaultLiquidity(cfg *config.Config, bs *services.BlockchainService, pm *services.PMService,
	release services.ChainSender, gate services.PoolGate) *services.VaultLPService {
	pool, ok := cfg.LiquidityPool()
	if !ok || bs == nil || config.DB == nil || pm == nil {
		return nil
	}
	client, _ := bs.ClientForChainID(pool.ChainID)
	tok, okTok := poolToken(cfg, bs)
	if client == nil || release == nil || !okTok {
		log.Printf("[liquidity] ALERT liquidity_vault_lp_dormant chain=%s reason=no client, operator key or pool token", pool.ChainKey)
		return nil
	}
	var opener services.PoolOpener
	if gate != nil {
		opener = gate
	}
	svc, err := services.NewVaultLPService(services.VaultLPConfig{DB: config.DB, PM: pm,
		Vault: services.NewLiquidityVault(client, pool.Vault), Tokens: client, Sender: release,
		Token: tok, ChainName: poolChainName(cfg), Gate: opener,
		CompanyUser: func(ctx context.Context) (uuid.UUID, error) {
			return services.CompanyPMUserID(ctx, config.DB, cfg.ITPMID)
		}})
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_vault_lp_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil
	}
	log.Printf("[liquidity] LP side chain=%s(%d) vault=%s", pool.ChainKey, pool.ChainID, pool.Vault)
	return svc
}

// attachVaultLiquidity serves /liquidity from the shared pool, shows the
// app's LP CTA and keeps the depositor allow-list in sync. Nil attaches nothing.
func attachVaultLiquidity(lp *services.VaultLPService, p2p *handlers.P2PHandler, pmh *handlers.PMHandler, scheduler *jobs.Scheduler) {
	if lp == nil || p2p == nil {
		return
	}
	p2p.SetVaultLiquidity(lp)
	if pmh != nil {
		pmh.SetLiquidityAvailable(true)
	}
	scheduler.Register(jobs.NewLiquidityDepositorSyncJob(lp, 2*time.Minute), jobs.AlertOnFailureStreak())
}

// poolChainName is the pool chain's display name ("BNB Smart Chain").
func poolChainName(cfg *config.Config) string {
	pool, ok := cfg.LiquidityPool()
	if !ok {
		return ""
	}
	if c, ok := cfg.Networks[pool.Network].Chains[pool.ChainKey]; ok && c.Name != "" {
		return c.Name
	}
	return pool.ChainKey
}
