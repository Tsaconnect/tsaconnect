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
// the order desk (plan C1 P4), and hands the desk to ITService and the slice
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
	w.desk, w.nonces = buildPoolDesk(cfg, bs, reg, trades, pm, w.treasury, w.gates)
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
// every part exists. The burst generation sums the endpoint generations of the
// clients an open touches (P10): any failover in any of them aborts a burst.
func buildPoolDesk(cfg *config.Config, bs *services.BlockchainService, reg *services.P2PEscrowRegistry, trades *services.P2PTradeService,
	pm *services.PMService, treasury *services.Treasury, gates services.PoolGate) (*services.PoolDesk, *services.TreasuryNonceWatch) {
	pool, ok := reg.PoolChain()
	if ok {
		if err := services.PoolOrdersAllowedOn(pool.ChainID); err != nil {
			// Review I3: expected on a mainnet pool row until plan C2 lands. Plan
			// B's LP onboarding keeps running on the same row (wirePoolOrders
			// still builds and returns the gates).
			log.Printf("[liquidity] pool orders wait for plan C2's settle path on %s; LP onboarding stays on", pool.ChainKey)
			return nil, nil
		}
	}
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
	nonces := services.NewTreasuryNonceWatch(pool.ChainKey, client.Probe(), treasury.Address(), 30*time.Second, nil)
	desk, err := services.NewPoolDesk(services.PoolDeskConfig{
		DB: config.DB, PM: pm, Trades: trades, Stock: stock, Token: tok, Escrow: escrow, EscrowAddr: escrow.ContractAddress(),
		Treasury: treasury, Gates: gates, Probe: client.Probe(), Nonces: nonces, BuyerUserID: buyer, ChainName: poolChainName(cfg),
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
// product until the RPC came back. That case is a warning.
func checkPoolUnconfigure(db *gorm.DB, cfg *config.Config, reg *services.P2PEscrowRegistry) error {
	if db == nil || cfg == nil {
		return nil
	}
	var chains []int64
	if err := db.Model(&models.P2PTrade{}).
		Where("it_intent_id IS NOT NULL AND status NOT IN ?", []string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
		Distinct("chain_id").Pluck("chain_id", &chains).Error; err != nil {
		return fmt.Errorf("liquidity: read live slices: %w", err)
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
