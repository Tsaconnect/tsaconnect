package main

import (
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// poolWiring is the TSA Liquidity pool as boot builds it, once.
type poolWiring struct {
	treasury *services.Treasury
	gates    services.PoolGate
	desk     *services.PoolDesk
	nonces   *services.TreasuryNonceWatch
}

// wirePoolOrders builds the pool chain's treasury lane and gates (Plan A) and
// the order desk (plan C1 P4, its settle side plan C2), and hands the desk to ITService and the slice
// hooks to the trade service. Dormant (no pool row): an empty wiring. The
// pool_order_max ceiling is set either way: the admin console edits pool
// settings while the pool is dormant too.
func wirePoolOrders(cfg *config.Config, bs *services.BlockchainService, reg *services.P2PEscrowRegistry, trades *services.P2PTradeService,
	pm *services.PMService, release services.ChainSender, it *services.ITService, rampTreasury *services.Treasury) *poolWiring {
	services.SetPoolOrderMaxCeilingUSD(cfg.RampTreasuryMaxInflightUSDPerOrder)
	w := &poolWiring{}
	if _, ok := cfg.PoolChain(); !ok {
		return w
	}
	w.treasury = poolTreasuryFor(cfg, bs, rampTreasury) // sets the lane's EscrowCalls to the pool escrow only
	w.gates = buildPoolChain(cfg, bs, reg, release, w.treasury)
	w.desk, w.nonces = buildPoolDesk(cfg, bs, reg, trades, pm, release, it, w.treasury, w.gates)
	if w.desk != nil && it != nil {
		it.SetPoolDesk(w.desk)
		if err := it.PoolHonourFits(); err != nil { // review I4: opens refuse until the env values shrink
			log.Printf("[liquidity] ALERT liquidity_pool_honour_window_unsafe %s", err)
		}
		if trades != nil {
			trades.SetPoolSlices(it, release) // release may be nil: F21 then alerts and waits
		}
	}
	return w
}

// buildPoolDesk is nil (pool orders dormant, with the reason logged) unless
// every part exists, the settle side included: the release key, the P2P
// settlement forwarder that pays LPs, and the pool chain's sale wallet. The burst generation sums the endpoint generations of the
// clients an open touches (P10): any failover in any of them aborts a burst.
func buildPoolDesk(cfg *config.Config, bs *services.BlockchainService, reg *services.P2PEscrowRegistry, trades *services.P2PTradeService,
	pm *services.PMService, release services.ChainSender, it *services.ITService, treasury *services.Treasury,
	gates services.PoolGate) (*services.PoolDesk, *services.TreasuryNonceWatch) {
	pool, ok := reg.PoolChain()
	if !ok || bs == nil || trades == nil || pm == nil || treasury == nil || gates == nil {
		log.Printf("[liquidity] pool orders dormant: a part is missing (chain=%v trades=%v pm=%v treasury=%v gates=%v)",
			ok, trades != nil, pm != nil, treasury != nil, gates != nil)
		return nil, nil
	}
	client, _ := bs.ClientForChainID(pool.ChainID)
	escrow, err := reg.For(pool.ChainID)
	lane := treasury.Lane(pool.ChainKey)
	if client == nil || err != nil || lane == nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant chain=%s reason=no client, escrow or treasury lane", pool.ChainKey)
		return nil, nil
	}
	// Re-review N7: the 60-minute slice window the honour guard, the canceller
	// and every slice's expires_at assume must be this escrow's paymentWindow().
	if err := services.CheckPoolPaymentWindow(escrow); err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_payment_window_mismatch chain=%s %s", pool.ChainKey, services.RedactAlertText(err.Error()))
		return nil, nil
	}
	stock := services.NewPoolStock(reg, bs.TokenAddressForChainID)
	tok, ok := stock.Token()
	if !ok {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant chain=%s reason=no pool token", pool.ChainKey)
		return nil, nil
	}
	buyer, err := companyPMUser(cfg)
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil, nil
	}
	gens := []interface{ EndpointGeneration() uint64 }{client}
	if g, ok := lane.Native.(interface{ EndpointGeneration() uint64 }); ok && g != interface{ EndpointGeneration() uint64 }(client) {
		gens = append(gens, g)
	}
	if release == nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant chain=%s reason=no release key — nothing could settle a paid order", pool.ChainKey)
		return nil, nil
	}
	if !it.HasPoolForwards() {
		// Review I-4: a desk that cannot pay its LPs never takes naira.
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant chain=%s reason=no P2P settlement forwarder — LPs could not be paid", pool.ChainKey)
		return nil, nil
	}
	pc, _ := cfg.PoolChain()
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
		DB: config.DB, PM: pm, Trades: trades, Stock: stock, Token: tok, Escrow: escrow, EscrowAddr: escrow.ContractAddress(),
		Treasury: treasury, Gates: gates, Probe: client.Probe(), Nonces: nonces, BuyerUserID: buyer, ChainName: poolChainName(cfg),
		Sender: release, Verifier: verifier, Sale: sale, LogChunk: pc.Pool.LogChunkBlocks,
		Generation: func() uint64 {
			var sum uint64
			for _, g := range gens {
				sum += g.EndpointGeneration()
			}
			return sum
		},
	})
	if err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_orders_dormant reason=%s", services.RedactAlertText(err.Error()))
		return nil, nil
	}
	log.Printf("[liquidity] pool orders ready chain=%s(%d) token=%s decimals=%d treasury=%s", tok.ChainKey, tok.ChainID, tok.Symbol, tok.Decimals, treasury.Address())
	return desk, nonces
}

// companyPMUser is IT_PM_ID's user: every slice's DB buyer (S10).
func companyPMUser(cfg *config.Config) (uuid.UUID, error) {
	id, err := uuid.Parse(cfg.ITPMID)
	if err != nil || config.DB == nil {
		return uuid.Nil, fmt.Errorf("IT_PM_ID is unusable for the pool's slice buyer")
	}
	var pm models.PaymentMerchant
	if err := config.DB.First(&pm, "id = ?", id).Error; err != nil || !pm.IsActive() {
		return uuid.Nil, fmt.Errorf("IT_PM_ID %s is not an active payment merchant", id)
	}
	return pm.UserID, nil
}

// registerPoolOrderJobs: the stuck-nonce watch (F24) while the desk exists.
func registerPoolOrderJobs(scheduler *jobs.Scheduler, w *poolWiring) {
	if w == nil || w.desk == nil || w.nonces == nil {
		return
	}
	scheduler.Register(jobs.NewTreasuryNonceWatchJob(w.nonces, 15*time.Second), jobs.AlertOnFailureStreak())
}

// attachPoolCard serves the pinned card (spec §15) only while the desk
// exists; dormant, the handler answers {enabled:false} (O-C1-1: hidden).
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
}

// registerPoolSettleJob runs pool settlement in its own job (review I-8), alert-watched.
func registerPoolSettleJob(s *jobs.Scheduler, w *poolWiring, it *services.ITService, n jobs.WorkerFailureNotifier) {
	if w == nil || w.desk == nil || it == nil {
		return
	}
	j := jobs.NewPoolSettleJob(it, 30*time.Second)
	if n != nil {
		j.SetAlerter(n)
	}
	s.Register(j, jobs.AlertOnFailureStreak())
}

// statementFreshness is the canceller's view of the central-account poll: the
// inbound poller, or a true nil interface when there is none (never a typed nil).
func statementFreshness(p *jobs.InboundCreditPoller) services.StatementFreshness {
	if p == nil {
		return nil
	}
	return p
}

// registerExpiryCanceller is §14: always on whenever a release key can send
// (S24, decision 17). Without a pool desk it runs the legacy scope only.
func registerExpiryCanceller(scheduler *jobs.Scheduler, trades *services.P2PTradeService, release services.ChainSender,
	it *services.ITService, fresh services.StatementFreshness, notifier jobs.WorkerFailureNotifier) {
	if trades == nil || release == nil {
		log.Printf("[p2p-expiry] dormant: no P2P trade service or release key")
		return
	}
	scheduler.Register(jobs.NewP2PExpiryCanceller(services.NewExpiryCanceller(services.ExpiryCancellerConfig{
		Trades: trades, Signer: release, Pool: it, Fresh: fresh}), notifier), jobs.AlertOnFailureStreak())
	log.Printf("[p2p-expiry] canceller on (legacy scope always; pool scope=%t)", it != nil && it.HasPool())
}

// checkPoolUnconfigure is §25 step 12 (S46): boot refuses while a
// non-terminal slice sits on a chain the configuration no longer names as
// the pool (its preset row was removed). It decides from cfg, never from the
// runtime registry (review I-6): a pool RPC that fails to dial at boot keeps
// the chain out of the registry, and refusing then would restart-loop every
// product until the RPC came back. That case is a warning, and so is a slice
// table it cannot read (an ALERT).
func checkPoolUnconfigure(db *gorm.DB, cfg *config.Config, reg *services.P2PEscrowRegistry) error {
	if db == nil || cfg == nil {
		return nil
	}
	var chains []int64
	if err := db.Model(&models.P2PTrade{}).
		Where("it_intent_id IS NOT NULL AND status NOT IN ?", []string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
		Distinct("chain_id").Pluck("chain_id", &chains).Error; err != nil {
		// Pre-merge review M-5: only a positive finding refuses boot. A read
		// error (transient DB, or a binary started before -migrate) would
		// crash-loop every product; the slices, if any, wait either way.
		log.Printf("[liquidity] ALERT liquidity_pool_unconfigure_unchecked err=%s — boot goes on; re-check live pool slices by hand",
			services.RedactAlertText(err.Error()))
		return nil
	}
	for _, id := range chains {
		if cfg.P2PChains[id].Purpose != config.P2PPurposePool {
			return fmt.Errorf("liquidity_pool_unconfigure_refused: live pool slices on chain %d, which has no pool escrow configured — restore the preset row until they are terminal", id)
		}
		if reg.Purpose(id) != config.P2PPurposePool {
			log.Printf("[liquidity] WARN liquidity_pool_chain_not_running chain=%d: live pool slices, the chain is configured as the pool but its escrow is not running (RPC down at boot?) — slices wait until it is back", id)
		}
	}
	return nil
}
