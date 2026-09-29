package main

import (
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// buildNameChanges wires name change by re-verification. The service and its
// sweeper always run — a change already running (a fee held, a Didit decision
// on its way, a held name waiting for an admin, a refund owed) is finished
// whatever the flag says — but users can start one only with
// NAME_CHANGE_ENABLED on.
//
// Each way to pay the fee turns on with what it needs: spendable MCGP with the
// operator signer (the same debit hold bills use), a stablecoin with the ramp
// treasury. Neither wired leaves the fee unpayable, and the feature says so.
func buildNameChanges(cfg *config.Config, swap *services.MCGPSwapService, treasury *services.Treasury,
	chains *services.BlockchainService, bus *events.Bus, scheduler *jobs.Scheduler) *services.NameChangeService {
	svc := services.NewNameChangeService(config.DB, services.NameChangeConfig{
		Enabled:                 cfg.NameChangeEnabled,
		FeeUSD:                  cfg.NameChangeFeeUSD,
		MaxPerYear:              cfg.NameChangeMaxPerYear,
		MaxAttempts:             cfg.NameChangeMaxAttempts,
		TTL:                     cfg.NameChangeTTL,
		MaxSessionsPer30Days:    cfg.NameChangeMaxSessionsPer30Days,
		MaxRefundedCancels:      cfg.NameChangeMaxRefundedCancels,
		RefundWindow:            cfg.NameChangeRefundWindow,
		RequireFaceMatch:        cfg.NameChangeRequireFaceMatch,
		MinConfirmations:        cfg.NameChangeMinConfirmations,
		MinConfirmationsByChain: cfg.NameChangeMinConfirmationsByChain,
		// The wallet-proof challenges are HMAC'd with a key derived from the
		// JWT secret (the service domain-separates it): no new secret to run.
		ProofKey: []byte(cfg.JWTSecret),
	}, bus, time.Now)

	mcgp, stable := false, false
	if swap != nil && swap.HasOperator() {
		svc.SetMCGP(services.NewBillPaymentService(config.DB, swap).ForPurpose(models.BillPayPurposeNameChange), swap)
		mcgp = true
	}
	if treasury != nil && chains != nil {
		// The treasury's own chains, proven with the same verifiers converted
		// buys use.
		svc.SetStableRail(services.NewTreasuryFeeRail(treasury, rampTransferVerifiers(chains, treasury.ChainKeys()),
			services.FeeTxReaders(chains, treasury.ChainKeys())))
		stable = true
	}

	// The sweeper runs whatever the flag says: a change already paid for must
	// be finished — its hold settled or reversed, its refund sent — even after
	// new ones are switched off.
	scheduler.Register(jobs.NewNameChangeSweeper(svc, time.Minute), jobs.AlertOnFailureStreak())
	if !cfg.NameChangeEnabled {
		log.Printf("[name-change] new changes off: NAME_CHANGE_ENABLED is off (running changes are still finished)")
		return svc
	}
	if !svc.Enabled() {
		log.Printf("[name-change] ALERT name_change_enabled_but_dormant=1 NAME_CHANGE_ENABLED is on but NAME_CHANGE_FEE_USD=%q is not a usable fee — no new change can start until it is fixed", cfg.NameChangeFeeUSD)
		return svc
	}
	log.Printf("[name-change] enabled fee=$%s pay_mcgp=%v pay_stablecoin=%v max_per_year=%d attempts=%d ttl=%s",
		cfg.NameChangeFeeUSD, mcgp, stable, cfg.NameChangeMaxPerYear, cfg.NameChangeMaxAttempts, cfg.NameChangeTTL)
	return svc
}
