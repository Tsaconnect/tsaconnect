package main

import (
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// buildPayRefunds wires TSA Pay refunds (spec 2026-10-08 Part 3). It returns
// the service for the refund routes, or nil — the routes then answer 503
// refunds_unavailable — when refunds are off (TSA_PAY_REFUNDS) or there is no
// chain registry.
//
// Off, the service is still built and set as the payments' refund aggregator:
// payment views, totals and webhooks keep reading refunds already made, and
// none is ever hidden. Only the routes and the confirmer need the flag.
//
// The proof is a SECOND StableTransferVerifier, over every mainnet chain with
// a client — never the ramp treasury's lanes, so a refund is proven whether or
// not the treasury is configured or killed. Depth is RAMP_TREASURY_CONFIRMATIONS.
// The confirmer is registered with AlertOnFailureStreak: a silent confirmer
// leaves merchants unsure whether to pay twice.
//
// No chain is excluded at boot: one whose RPC refuses receipt reads surfaces
// through that alert and the pay_refund_unconfirmed ALERT. That is why the flag
// exists — repoint such RPCs before turning it on (tsa-api-go/CLAUDE.md).
func buildPayRefunds(cfg *config.Config, db *gorm.DB, payments *services.PayPaymentService, merchants *services.PayMerchantService,
	chains *services.BlockchainService, bus *events.Bus, scheduler *jobs.Scheduler) *services.PayRefundService {
	state := "off"
	if cfg.TSAPayRefundsEnabled {
		state = "on"
	}
	log.Printf("[tsa-pay] refunds: %s (TSA_PAY_REFUNDS)", state)
	if chains == nil || payments == nil {
		log.Printf("[tsa-pay] refunds dormant: no chain registry")
		return nil
	}
	keys := evmChainKeys(cfg)
	verifier := services.NewStableTransferVerifier(rampTransferVerifiers(chains, keys), services.FeeTxReaders(chains, keys))
	refunds := services.NewPayRefundService(db, payments, merchants, services.NewBlockchainRefundNetwork(chains), verifier,
		cfg.RampTreasuryConfirmations, time.Now)
	if bus != nil {
		refunds.SetEvents(bus)
	}
	payments.SetRefundAggregator(refunds)
	if !cfg.TSAPayRefundsEnabled {
		return nil
	}
	scheduler.Register(jobs.NewPayRefundConfirmer(refunds, 30*time.Second), jobs.AlertOnFailureStreak())
	log.Printf("[tsa-pay] refunds ready over %d mainnet chains (confirmations=%d)", len(keys), cfg.RampTreasuryConfirmations)
	return refunds
}
