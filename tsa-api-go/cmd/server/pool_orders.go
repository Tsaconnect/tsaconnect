package main

import (
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// poolWiring is the shared liquidity pool as boot builds it, once.
type poolWiring struct {
	treasury *services.Treasury
	gates    services.PoolGate
	desk     *services.PoolDesk
	nonces   *services.TreasuryNonceWatch
	ledger   *services.VaultIndexer
}

// wirePoolOrders builds the pool chain's treasury lane, gates, order desk and
// ledger, and hands the desk to ITService. No pool row with a vault: an empty
// wiring. The pool_order_max ceiling is set either way: the admin console
// edits pool settings while the pool is dormant too.
func wirePoolOrders(cfg *config.Config, bs *services.BlockchainService, release services.ChainSender,
	it *services.ITService, rampTreasury *services.Treasury) *poolWiring {
	services.SetPoolOrderMaxCeilingUSD(cfg.RampTreasuryMaxInflightUSDPerOrder)
	w := &poolWiring{}
	if _, ok := cfg.LiquidityPool(); !ok {
		return w
	}
	w.treasury = poolTreasuryFor(cfg, bs, rampTreasury)
	w.gates = buildPoolChain(cfg, bs, release, w.treasury)
	w.desk, w.nonces = buildPoolDesk(cfg, bs, release, w.treasury, w.gates)
	w.ledger = buildVaultLedger(cfg, bs)
	if w.desk != nil && it != nil {
		it.SetPoolDesk(w.desk)
		it.SetVaultLedger(w.ledger)
	}
	return w
}

// buildPoolDesk is the order desk: the vault, the operator that fills from
// it, the treasury lane that delivers. Nil — pool orders dormant, the reason
// logged — unless every part exists.
func buildPoolDesk(cfg *config.Config, bs *services.BlockchainService, release services.ChainSender,
	treasury *services.Treasury, gates services.PoolGate) (*services.PoolDesk, *services.TreasuryNonceWatch) {
	pool, _ := cfg.LiquidityPool()
	if bs == nil || treasury == nil || gates == nil || release == nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant reason=a part is missing (treasury=%v gates=%v operator=%v)",
			treasury != nil, gates != nil, release != nil)
		return nil, nil
	}
	client, _ := bs.ClientForChainID(pool.ChainID)
	lane := treasury.Lane(pool.ChainKey)
	tok, okTok := poolToken(cfg, bs)
	if client == nil || lane == nil || !okTok {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant chain=%s reason=no client, treasury lane or pool token", pool.ChainKey)
		return nil, nil
	}
	vault := services.NewLiquidityVault(client, pool.Vault)
	verifier := services.NewLogTransferVerifier(client.Client)
	sale := services.NewCompanyWallet(services.CompanyWalletConfig{
		Treasury: treasury, Decimals: map[string]map[string]int{pool.ChainKey: {tok.Symbol: tok.Decimals}},
		Verifiers:          map[string]services.ERC20SourcedTransferVerifier{pool.ChainKey: verifier},
		Probes:             map[string]services.TreasuryTxProbe{pool.ChainKey: client.Probe()},
		ConversionTreasury: treasury.Address(),
	})
	if sale == nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant chain=%s reason=the treasury lane cannot sell %s", pool.ChainKey, tok.Symbol)
		return nil, nil
	}
	nonces := services.NewTreasuryNonceWatch(pool.ChainKey, client.Probe(), treasury.Address(), 30*time.Second, nil)
	desk, err := services.NewPoolDesk(services.PoolDeskConfig{
		DB: config.DB, Token: tok, Vault: vault, Treasury: treasury, Gates: gates, Probe: client.Probe(), Nonces: nonces,
		ChainName: poolChainName(cfg), Sender: release, Sale: sale, Generation: client.EndpointGeneration,
	})
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil, nil
	}
	log.Printf("[liquidity] pool orders ready chain=%s(%d) vault=%s token=%s decimals=%d treasury=%s",
		tok.ChainKey, tok.ChainID, vault.Address(), tok.Symbol, tok.Decimals, treasury.Address())
	return desk, nonces
}

// buildVaultLedger is the vault's indexer + reconciler, whenever the pool row
// names a vault — even while orders are dormant, so LP history and the books
// keep up.
func buildVaultLedger(cfg *config.Config, bs *services.BlockchainService) *services.VaultIndexer {
	pool, ok := cfg.LiquidityPool()
	if !ok || bs == nil || config.DB == nil {
		return nil
	}
	client, _ := bs.ClientForChainID(pool.ChainID)
	if client == nil || client.Client == nil {
		log.Printf("[liquidity] ALERT liquidity_vault_ledger_dormant chain=%s reason=no client", pool.ChainKey)
		return nil
	}
	x, err := services.NewVaultIndexer(services.VaultIndexerConfig{DB: config.DB, ChainID: pool.ChainID,
		Vault: services.NewLiquidityVault(client, pool.Vault), Reader: client.Client,
		DeployBlock: pool.VaultDeployBlock, Chunk: pool.LogChunkBlocks})
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_vault_ledger_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil
	}
	return x
}

// registerPoolOrderJobs: the ledger indexer whenever it exists, and the
// treasury's stuck-nonce watch while the desk exists.
func registerPoolOrderJobs(scheduler *jobs.Scheduler, w *poolWiring) {
	if w == nil {
		return
	}
	if w.ledger != nil {
		scheduler.Register(jobs.NewLiquidityVaultIndexerJob(w.ledger, 30*time.Second), jobs.AlertOnFailureStreak())
	}
	if w.desk != nil && w.nonces != nil {
		scheduler.Register(jobs.NewTreasuryNonceWatchJob(w.nonces, 15*time.Second), jobs.AlertOnFailureStreak())
	}
}

// attachPoolCard serves the pinned card only while the desk exists; dormant,
// the handler answers {enabled:false}.
func attachPoolCard(w *poolWiring, p2p *handlers.P2PHandler, it *services.ITService) {
	if w == nil || w.desk == nil || p2p == nil || it == nil {
		return
	}
	p2p.SetPoolCard(it)
}

// attachPoolSettle registers the console's pool routes while the desk exists.
func attachPoolSettle(w *poolWiring, it *services.ITService, itH *handlers.ITHandler) {
	if w == nil || w.desk == nil || it == nil || itH == nil {
		return
	}
	itH.SetPoolOrdersAdmin(it)
	itH.SetVaultPoolAdmin(it)
}

// registerPoolSettleJob runs pool settlement in its own job, alert-watched,
// and next to it the auto-retry of orders parked on a liquidity refusal.
func registerPoolSettleJob(s *jobs.Scheduler, w *poolWiring, it *services.ITService, n jobs.WorkerFailureNotifier) {
	if w == nil || w.desk == nil || it == nil {
		return
	}
	j := jobs.NewPoolSettleJob(it, 30*time.Second)
	if n != nil {
		j.SetAlerter(n)
	}
	s.Register(j, jobs.AlertOnFailureStreak())
	s.Register(jobs.NewPoolFillAutoRetryJob(it, 2*time.Minute), jobs.AlertOnFailureStreak())
}

// statementFreshness is the canceller's view of the central-account poll: the
// inbound poller, or a true nil interface when there is none (never a typed nil).
func statementFreshness(p *jobs.InboundCreditPoller) services.StatementFreshness {
	if p == nil {
		return nil
	}
	return p
}

// registerExpiryCanceller: always on whenever a release key can send — it
// cancels expired ordinary P2P trades.
func registerExpiryCanceller(scheduler *jobs.Scheduler, trades *services.P2PTradeService, release services.ChainSender,
	fresh services.StatementFreshness, notifier jobs.WorkerFailureNotifier) {
	if trades == nil || release == nil {
		log.Printf("[p2p-expiry] dormant: no P2P trade service or release key")
		return
	}
	scheduler.Register(jobs.NewP2PExpiryCanceller(services.NewExpiryCanceller(services.ExpiryCancellerConfig{
		Trades: trades, Signer: release, Fresh: fresh}), notifier), jobs.AlertOnFailureStreak())
}
