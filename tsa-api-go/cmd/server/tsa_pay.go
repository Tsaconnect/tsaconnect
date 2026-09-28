package main

import (
	"encoding/hex"
	"log"
	"os"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// tsaPayMinMasterKeyBytes is the shortest TSA_PAY_WEBHOOK_MASTER_KEY we'll
// sign webhooks with — an HMAC key materially shorter than its digest
// (SHA-256, 32 bytes) is weaker than the digest it's meant to protect.
const tsaPayMinMasterKeyBytes = 32

// buildTSAPay wires the TSA Pay merchant-administration handler, or leaves
// it dormant (returns nil) with a logged reason. Dormant-by-default,
// matching every other optional integration in this file: a deploy that
// never sets TSA_PAY_* changes nothing.
//
// orders, intents, quoter and rates are the ramp's own services (nil when
// Paycrest, or swapped sells, are off). A naira payment needs orders; converting
// another token into its USDC also needs intents and the quoter; paying a crypto
// merchant needs those plus rates (the provider rate its amount is sized from).
// With no orders the start routes stay unwired and answer 503; with orders but
// no intents, direct payments work and converted or crypto ones answer
// "unavailable".
//
// When live, it also registers the two background workers TSA Pay needs to
// make any progress on its own: tsa-pay-webhooks (delivers signed events to
// merchant endpoints) and tsa-pay-reconciler (the expiry sweep, extended by
// Parts B/C). Neither has a webhook to receive from any provider, so
// nothing else ever drives them — a missed tick is simply caught up by the
// next one (see internal/jobs's package doc on idempotent workers).
func buildTSAPay(cfg *config.Config, db *gorm.DB, banks services.BankDirectory, chains *services.BlockchainService, scheduler *jobs.Scheduler,
	orders *services.PaycrestOrderService, intents *services.SellIntentService, quoter *services.RampSellQuoter,
	rates *services.PaycrestQuoteService, bus *events.Bus, mailer *services.EmailService) *handlers.PayHandler {
	if !cfg.TSAPayEnabled {
		log.Printf("[tsa-pay] dormant: TSA_PAY_ENABLED is not set")
		return nil
	}

	master, err := hex.DecodeString(cfg.TSAPayWebhookMasterKey)
	if err != nil || len(master) < tsaPayMinMasterKeyBytes {
		log.Printf("[tsa-pay] dormant: TSA_PAY_WEBHOOK_MASTER_KEY must be at least %d hex-encoded bytes", tsaPayMinMasterKeyBytes)
		return nil
	}

	feeBps, ok := services.PayFeeBps(os.Getenv("PAYCREST_SENDER_FEE_PERCENT"))
	if !ok {
		log.Printf("[tsa-pay] dormant: PAYCREST_SENDER_FEE_PERCENT is unset or unparsable — TSA Pay prices off the same platform fee as the ramp")
		return nil
	}

	merchants := services.NewPayMerchantService(db, banks, chains, master, time.Now)
	if mailer != nil {
		// An owner's guarded changes (crypto payout, keys, webhooks, the webhook
		// secret) wait on a code emailed to them; with no working mailer those
		// changes are refused rather than left waiting.
		merchants.SetMailer(mailer)
	} else {
		log.Printf("[tsa-pay] WARNING: no mailer — merchant owners cannot create or replace API keys, change webhooks or payouts")
	}
	payments := services.NewPayPaymentService(db, merchants, feeBps, cfg.TSAPayCheckoutBaseURL, time.Now)
	if chains != nil {
		// Proves an expired order's deposit address is empty before its payment is
		// handed back; without it no expired order is ever released.
		payments.SetDepositReader(chains)
	}
	if bus != nil {
		// "Payment received" alerts to each merchant's owner.
		payments.SetEvents(bus)
	}
	sender := services.NewPayWebhookSender(db, merchants, services.NewPayWebhookHTTPClient(), time.Now)

	scheduler.Register(jobs.NewPayWebhookWorker(sender, 15*time.Second), jobs.AlertOnFailureStreak())
	scheduler.Register(jobs.NewPayReconciler(payments, 30*time.Second), jobs.AlertOnFailureStreak())

	handler := handlers.NewPayHandler(merchants, payments, sender, cfg.TSAPayLiveEnabled).WithEvents(bus).WithOpenLinks(payments)
	if checkout := buildPayCheckout(payments, orders, intents, quoter, rates, cfg.TSAPayLiveEnabled); checkout != nil {
		handler.WithCheckout(checkout)
	}

	log.Printf("[tsa-pay] merchant administration, payment API and webhook delivery ready (live_enabled=%v, naira start=%v)",
		cfg.TSAPayLiveEnabled, orders != nil)
	return handler
}

// buildPayCheckout wires the start path, or returns nil (the routes then answer
// 503). The nil checks are explicit because a nil *SellIntentService stored in
// an interface would read as wired. Crypto payments are switched on only when
// every piece they need exists; naira payments never depend on them.
func buildPayCheckout(payments *services.PayPaymentService, orders *services.PaycrestOrderService,
	intents *services.SellIntentService, quoter *services.RampSellQuoter,
	rates *services.PaycrestQuoteService, live bool) *services.PayCheckoutService {
	// The reconciler follows converted and crypto payments through the intent
	// service alone; it needs no quoter or order service. Turning either off must
	// not strand payments already in flight, so it is wired whenever intents exist.
	if intents != nil && payments != nil {
		payments.SetIntents(intents)
	}
	if orders == nil {
		return nil
	}
	if intents == nil || quoter == nil {
		return services.NewPayCheckoutService(payments, orders, nil, nil, live)
	}
	checkout := services.NewPayCheckoutService(payments, orders, intents, quoter, live)
	if rates != nil {
		// The merchant's token is priced through the ramp quoter's own LiFi
		// client, not a second one.
		checkout.SetCrypto(quoter, rates)
	}
	return checkout
}
