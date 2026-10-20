package main

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
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

// wireFiatLiquidity is the fiat LP side (plan 2026-10-20): fiat LPs send USDT
// to the pool's refill wallet, read from the vault. Nil (dormant, reason
// logged) without the LP side, the chain client or a rate source.
func wireFiatLiquidity(cfg *config.Config, bs *services.BlockchainService, lp *services.VaultLPService, it *services.ITService) *services.FiatLPService {
	pool, ok := cfg.LiquidityPool()
	rate := liquidityFiatRate(it)
	if !ok || lp == nil || bs == nil || rate == nil || config.DB == nil {
		return nil
	}
	client, _ := bs.ClientForChainID(pool.ChainID)
	tok, okTok := poolToken(cfg, bs)
	if client == nil || client.Client == nil || !okTok {
		return nil
	}
	vault := services.NewLiquidityVault(client, pool.Vault)
	svc, err := services.NewFiatLPService(services.FiatLPConfig{DB: config.DB, Token: tok, Vault: pool.Vault,
		Desk: func(ctx context.Context) (string, error) {
			r, err := vault.Roles(ctx)
			if err != nil {
				return "", err
			}
			return r.CollateralProvider, nil
		},
		Verifier: services.NewLogTransferVerifier(client.Client), Rate: rate, CanLP: lp.CanProvide,
		TxTime: func(ctx context.Context, txHash string) (time.Time, error) {
			rc, err := client.Client.TransactionReceipt(ctx, common.HexToHash(txHash))
			if err != nil {
				return time.Time{}, err
			}
			h, err := client.Client.HeaderByNumber(ctx, rc.BlockNumber)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(int64(h.Time), 0), nil
		}})
	if err != nil {
		log.Printf("[fiat-lp] ALERT fiat_lp_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil
	}
	log.Printf("[fiat-lp] ready chain=%s(%d) vault=%s", pool.ChainKey, pool.ChainID, pool.Vault)
	return svc
}

// attachFiatLiquidity serves /liquidity/fiat. Nil attaches nothing.
func attachFiatLiquidity(f *services.FiatLPService, p2p *handlers.P2PHandler) {
	if f == nil || p2p == nil {
		return
	}
	p2p.SetFiatLiquidity(f)
}

// liquidityFiatRate prices USDT in a fiat currency with the same mid the
// company wallet uses (USDT trades at par with the USDC the desk quotes).
// Nil when there is no IT service, so naira positions stay off.
func liquidityFiatRate(it *services.ITService) services.FiatRateFunc {
	if it == nil {
		return nil
	}
	return func(ctx context.Context, currency string) (*big.Rat, error) {
		return it.StableMidFiatPerUnit(ctx, currency, models.ITAssetUSDC)
	}
}
