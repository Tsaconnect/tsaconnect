package main

import (
	"encoding/hex"
	"log"
	"os"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
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
// payments/webhooks stay nil here — Task 6/8 fill them in and only then
// does PayHandler.Enabled() (and so routes.SetupRoutes' /admin/pay group)
// turn on. scheduler is accepted now, unused, for that same forward
// compatibility: Task 8 registers the webhook-delivery and reconciler
// workers here.
func buildTSAPay(cfg *config.Config, db *gorm.DB, banks services.BankDirectory, chains *services.BlockchainService, scheduler *jobs.Scheduler) *handlers.PayHandler {
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
	payments := services.NewPayPaymentService(db, merchants, feeBps, cfg.TSAPayCheckoutBaseURL, time.Now)
	log.Printf("[tsa-pay] merchant administration and payment API ready (live_enabled=%v)", cfg.TSAPayLiveEnabled)
	return handlers.NewPayHandler(merchants, payments, nil, cfg.TSAPayLiveEnabled)
}
