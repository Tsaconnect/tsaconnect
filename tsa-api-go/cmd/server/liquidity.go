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
	"gorm.io/gorm"
)

// liquidityVoucherRenewalInterval is how often LP stock-ad vouchers are swept
// for renewal reminders and expired-ad closes.
const liquidityVoucherRenewalInterval = 6 * time.Hour

// wireLiquidity turns on the LP flows (TSA Liquidity plan B) when the registry
// holds a pool chain: the trade service's LP route, LP onboarding, and the read
// side. With no pool chain (production until the mainnet row is filled) it
// only hands the trade service its (closed) gate and returns nil.
func wireLiquidity(db *gorm.DB, pm *services.PMService, reg *services.P2PEscrowRegistry, trade *services.P2PTradeService,
	gate services.PoolGate, tokenAddr func(int64, string) string, chainName string,
	onboarding *services.PMOnboardingService, accounts services.AccountNameResolver) *services.LiquidityService {
	if trade == nil {
		return nil
	}
	var opener services.PoolOpener
	if gate != nil { // never a typed nil (plan A review I2)
		opener = gate
	}
	trade.SetPoolGate(opener)
	pool, ok := reg.PoolChain()
	if !ok {
		log.Printf("[liquidity] LP onboarding dormant: no pool chain in the P2P registry")
		return nil
	}
	if onboarding != nil {
		onboarding.SetLiquidity(pool.ChainID, accounts, opener)
	} else {
		log.Printf("[liquidity] LP applications dormant: no PM onboarding service (no fiat provider)")
	}
	log.Printf("[liquidity] LP onboarding on chain=%s(%d) assets=%v", pool.ChainKey, pool.ChainID, pool.Assets)
	return services.NewLiquidityService(db, pm, trade, services.NewPoolStock(reg, tokenAddr), opener, chainName)
}

// attachLiquidity hands a wired LiquidityService to its consumers: the
// /liquidity routes (registered only once the P2P handler has it), the admin
// LP columns, LP activation (which also turns on the app's LP CTA) and the
// voucher renewal job. A nil service — dormant — attaches nothing (O-B5).
// liq is non-nil only when trade is (wireLiquidity).
func attachLiquidity(liq *services.LiquidityService, p2p *handlers.P2PHandler, pmh *handlers.PMHandler,
	onboarding *services.PMOnboardingService, trade *services.P2PTradeService, scheduler *jobs.Scheduler) {
	if liq == nil {
		return
	}
	p2p.SetLiquidity(liq)
	if pmh != nil {
		pmh.SetLiquidityView(liq)
		if onboarding != nil { // a typed-nil pointer in LPActivator would panic on use
			pmh.SetLPActivator(onboarding)
		}
	}
	// A dead sweep leaves expired stock ads open and LPs unwarned.
	scheduler.Register(jobs.NewLiquidityVoucherRenewal(trade, liquidityVoucherRenewalInterval), jobs.AlertOnFailureStreak())
}

// wireVaultLiquidity is the shared pool's LP side (spec 2026-10-14 §5, §6),
// whenever the pool row names a vault: nil (dormant, reason logged) unless
// the chain client, the operator key and the pool token all exist.
func wireVaultLiquidity(cfg *config.Config, bs *services.BlockchainService, reg *services.P2PEscrowRegistry,
	pm *services.PMService, release services.ChainSender, gate services.PoolGate) *services.VaultLPService {
	pc, ok := cfg.PoolChain()
	if !ok || pc.Pool.Vault == "" || bs == nil || config.DB == nil || pm == nil {
		return nil
	}
	client, _ := bs.ClientForChainID(pc.ChainID)
	tok, okTok := services.NewPoolStock(reg, bs.TokenAddressForChainID).Token()
	if client == nil || release == nil || !okTok {
		log.Printf("[liquidity] ALERT liquidity_vault_lp_dormant chain=%s reason=no client, operator key or pool token", pc.ChainKey)
		return nil
	}
	var opener services.PoolOpener
	if gate != nil {
		opener = gate
	}
	svc, err := services.NewVaultLPService(services.VaultLPConfig{DB: config.DB, PM: pm,
		Vault: services.NewLiquidityVault(client, pc.Pool.Vault), Tokens: client, Sender: release,
		Token: tok, ChainName: poolChainName(cfg), Gate: opener,
		CompanyUser: func(ctx context.Context) (uuid.UUID, error) {
			return services.CompanyPMUserID(ctx, config.DB, cfg.ITPMID)
		}})
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_vault_lp_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil
	}
	log.Printf("[liquidity] LP side mode=vault chain=%s(%d) vault=%s", pc.ChainKey, pc.ChainID, pc.Pool.Vault)
	return svc
}

// attachVaultLiquidity serves /liquidity from the shared pool and keeps the
// depositor allow-list in sync. Nil attaches nothing.
func attachVaultLiquidity(lp *services.VaultLPService, p2p *handlers.P2PHandler, scheduler *jobs.Scheduler) {
	if lp == nil || p2p == nil {
		return
	}
	p2p.SetVaultLiquidity(lp)
	scheduler.Register(jobs.NewLiquidityDepositorSyncJob(lp, 2*time.Minute), jobs.AlertOnFailureStreak())
}

// poolChainName is the pool chain's display name ("BNB Smart Chain").
func poolChainName(cfg *config.Config) string {
	pool, ok := cfg.PoolChain()
	if !ok {
		return ""
	}
	if c, ok := cfg.Networks[pool.Network].Chains[pool.ChainKey]; ok && c.Name != "" {
		return c.Name
	}
	return pool.ChainKey
}
