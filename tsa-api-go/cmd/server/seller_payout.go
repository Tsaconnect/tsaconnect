package main

import (
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// wireSellerPayouts turns on fiat payouts for marketplace sellers who set a
// bank account: checkout prices them and makes the treasury the order's
// on-chain seller, the treasury signs the seller-only escrow calls, and the
// payout machine carries each released order to the seller's bank. It needs
// the ramp treasury (Sonic and Base lanes, the escrow as a call target),
// Paycrest, and the escrow; with any missing the orders pay the seller's
// wallet exactly as before. There is no switch: a seller with no payout
// method is untouched.
func wireSellerPayouts(cfg *config.Config, chains *services.BlockchainService, treasury *services.Treasury,
	orders *services.PaycrestOrderService, quotes *services.PaycrestQuoteService, escrow *services.EscrowService,
	methods *services.SellerPayoutService, ch *handlers.CheckoutHandler, scheduler *jobs.Scheduler, bus *events.Bus,
	alerts services.SellerPayoutAlerter, fw *services.FlutterwaveClient) *handlers.SellerPayoutAdminHandler {
	if treasury == nil || orders == nil || quotes == nil || escrow == nil || methods == nil || ch == nil {
		log.Printf("[seller-payout] dormant: needs the treasury, Paycrest, the escrow and payout methods")
		return nil
	}
	sw := services.NewSellerPayoutSwitch(config.DB)
	senderFee := func() string { return services.PayFeeSplit{FeePercent: cfg.TSAPayFeePercent}.SenderFee(false) }
	lifiQuotes := services.NewLiFiQuoteClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey, cfg.LiFiIntegratorID)
	planner := services.NewSellerPayoutPlanner(methods, quotes, treasury, senderFee, cfg.SellerPayoutBridgeBps).
		WithSwitch(sw).WithBridge(lifiQuotes)
	// The company route: the central account (the Flutterwave wallet IT sells pay
	// out of) pays the naira; preferred over Paycrest whenever it can run.
	var companyPayer services.SellerPayoutCompanyPayer
	if fw != nil {
		// Built whenever Flutterwave is: a payout already sent must stay observable
		// even if the route is turned off for NEW orders (MAX_NGN=0).
		companyPayer = services.FlutterwaveCompanyPayer{Gateway: services.NewFlutterwaveGateway(fw, cfg.NombaSenderName), Client: fw}
		if cfg.SellerPayoutCompanyMaxKobo > 0 {
			planner.WithCompany(companyPayer, config.DB, cfg.RampCompanyTakeBps, cfg.SellerPayoutCompanyFeeKobo, cfg.SellerPayoutCompanyMaxKobo)
		}
	}
	ch.WithPayouts(planner)
	ch.WithSellerCalls(services.NewSellerEscrowCalls(treasury, escrow))

	orchestrator := services.NewSellerPayoutOrchestrator(
		config.DB, treasury, &services.GormRampLegStore{DB: config.DB},
		lifiQuotes,
		services.NewLiFiStatusClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey),
		rampTransferVerifiers(chains, evmChainKeys(cfg)), orders, escrow, cfg.ProductEscrowAddress,
		cfg.RampConversionRetryWindow, cfg.RampConversionLegCeiling, nil,
	).WithEvents(bus).WithSwitch(sw).WithAlerts(alerts)
	if companyPayer != nil {
		orchestrator.WithCompany(companyPayer, cfg.SellerPayoutCompanyFeeKobo)
	}
	scheduler.Register(jobs.NewSellerPayoutWorker(orchestrator, 30*time.Second), jobs.AlertOnFailureStreak())
	log.Printf("[seller-payout] enabled treasury=%s bridge_bps=%d company_route=%v company_new_orders_max_ngn=%d company_fee_ngn=%d take_bps=%d",
		treasury.Address(), cfg.SellerPayoutBridgeBps, companyPayer != nil, cfg.SellerPayoutCompanyMaxKobo/100,
		cfg.SellerPayoutCompanyFeeKobo/100, cfg.RampCompanyTakeBps)
	return &handlers.SellerPayoutAdminHandler{Admin: services.NewSellerPayoutAdmin(config.DB, orchestrator), Switch: sw}
}
