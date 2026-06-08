package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/routes"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/ws"
)

func main() {
	// Load .env file (ignore error if file doesn't exist)
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Load configuration
	cfg := config.Load()

	// Connect to PostgreSQL
	_, err := config.ConnectDB(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}

	// Auto-migrate database tables
	if err := config.AutoMigrate(); err != nil {
		log.Fatalf("Failed to auto-migrate database: %v", err)
	}

	// Initialize the unified fee-rate config (seeds defaults if the table is
	// empty) and assign the process-wide singleton used by fee call sites.
	feeConfigService := services.InitFeeConfigService(config.DB)

	// Referral-graph service (SP2 of the fee/TP overhaul): single-query,
	// cycle-safe upline/downline traversal over users.referred_by. Additive —
	// SP3's TP engine and the TP/referrals UI consume it; existing behavior is
	// unchanged. Constructed here so it's available for wiring into handlers.
	referralGraphService := services.NewReferralGraphService(config.DB)

	// Fee/TP overhaul v2 engine (SP3). Constructed unconditionally so Stage 2
	// can wire OnFeeSettled into the fee-settled paths, but GATED by
	// cfg.IsFeeModelV2Enabled() — while the flag is off (default) the legacy
	// handlers.DistributeTPEarnings path stays live and callers must not invoke
	// this engine. TP minting rate comes from cfg (default 1 TP = $1).
	feeDistributionService := services.NewFeeDistributionService(config.DB, referralGraphService, cfg.TPPerUSDOrDefault())

	// SP7 — claim-eligibility gate (anti-sybil + KYC). TP keeps accruing for
	// everyone (SP3/SP5 unchanged); this service decides who may CLAIM (be in
	// SP6's merkle root): KYC-verified + not admin/auto-flagged + no self-referral
	// or referral cycle. It reuses the SP2 cycle-safe Uplines walk for cycle
	// detection. The bounded velocity heuristic auto-flags suspicious referral
	// surges for admin review; thresholds + on/off come from cfg. Constructed
	// unconditionally so SP6 + the admin tools + the signup hook can consume it.
	rewardEligibilityService := services.NewRewardEligibilityService(config.DB, referralGraphService)
	rewardEligibilityService.SetVelocityConfig(
		cfg.RewardVelocityEnabled,
		cfg.RewardVelocityMaxSignups,
		cfg.RewardVelocityWindow,
	)
	log.Printf("[sp7] claim-eligibility gate ready (velocity heuristic enabled=%t, max=%d/%s)",
		cfg.RewardVelocityEnabled, cfg.RewardVelocityMaxSignups, cfg.RewardVelocityWindow)
	if cfg.IsFeeModelV2Enabled() {
		log.Printf("[fee-v2] FEE_MODEL_V2_ENABLED=true — new fee/TP engine ACTIVE (tpPerUSD=%.4f)", cfg.TPPerUSDOrDefault())
	} else {
		log.Printf("[fee-v2] FEE_MODEL_V2_ENABLED=false — legacy TP path live; v2 engine constructed but dormant")
	}
	_ = referralGraphService

	// Get underlying sql.DB for health check and cleanup
	sqlDB, err := config.DB.DB()
	if err != nil {
		log.Fatalf("Failed to get underlying sql.DB: %v", err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			log.Printf("Error closing database connection: %v", err)
		}
	}()

	// Initialize Cloudinary
	if err := config.InitCloudinary(cfg); err != nil {
		log.Printf("Warning: Cloudinary initialization failed: %v", err)
	}

	// Initialize services
	priceService := services.NewPriceService()
	blockchainService := services.NewBlockchainService(cfg)

	// Initialize escrow service (sonic chain client)
	sonicClient := blockchainService.ClientForChain("sonic")
	// P2P talks to whichever Sonic network SONIC_NETWORK selected. The
	// other Sonic-side services (product escrow, service-contact, OTC)
	// use mainnet-fixed addresses and stay on the mainnet client.
	p2pSonicClient := blockchainService.ClientForNetwork(cfg.SonicNetwork, "sonic")
	if p2pSonicClient == nil {
		p2pSonicClient = sonicClient
	}
	if p2pSonicClient != nil {
		log.Printf("[config] P2P sonic client: network=%s chainID=%d", cfg.SonicNetwork, p2pSonicClient.ChainID())
	}
	escrowService := services.NewEscrowService(sonicClient, cfg)

	verifyEscrowAdmin(escrowService)

	// Initialize the P2P escrow adapter (separate contract; safe to no-op
	// when P2P_ESCROW_ADDRESS isn't set — service surfaces a clear error).
	// Kept only for the on-chain owner log below; the trade service now resolves
	// escrow per-chain via the registry.
	p2pEscrowService := services.NewP2PEscrowService(p2pSonicClient, cfg)

	// Per-chain escrow registry (Sonic + any enabled BSC). Boot verification
	// (eth_chainId + bytecode per chain) runs inside the constructor. The trade
	// service resolves escrow + EIP-712 domain per entity-chain from this registry.
	p2pEscrowRegistry := services.NewP2PEscrowRegistry(blockchainService, cfg)

	// Initialize service contact service
	serviceContactService := services.NewServiceContactService(sonicClient, cfg)

	// Initialize OTC service
	otcService := services.NewOTCService(sonicClient, cfg)

	// Initialize exchange rate service (OER + Bybit P2P hybrid)
	p2pService := services.NewP2PService(
		cfg.OpenExchangeRatesAppID,
		cfg.SupportedCurrencies,
		cfg.BybitP2PCurrencies,
		cfg.BybitProbeAmounts,
	)

	// Initialize event bus and WebSocket hub
	eventBus := events.NewBus()
	wsHub := ws.NewHub()

	// Initialize the in-process periodic-worker scheduler. Workers are
	// registered by feature wiring (Step 7 timeout watchdog, Step 9 chain
	// reconciler, etc.). Phase 1 keeps this in-process — see
	// internal/jobs/scheduler.go for the rationale and the Phase 2 migration
	// path to River.
	scheduler := jobs.NewScheduler()

	// Fee/TP overhaul v2 — SP5 payout-computation engine. Turns accrued TP +
	// cashback into a per-user claimable-MCGP ledger. It settles the user's
	// pending global share (SP4) via feeDistributionService, locks rewardUSD→MCGP
	// at the CURRENT OTC price (otcService, deferred when unavailable so a bad
	// rate is never locked), and emits tp.level.up on the event bus for SP8.
	// Constructed unconditionally; the periodic job below is GATED on the flag.
	rewardService := services.NewRewardService(
		config.DB,
		eventBus,
		feeDistributionService,
		services.MCGPPriceFromOTC(otcService),
	)

	// Bridge SP3 accrual → SP5 processing: every settled fee marks its TP/cashback
	// recipients dirty so the reward processor recomputes them on its next tick.
	// Without this the dirty set is always empty and the processor does nothing,
	// leaving the whole reward feature inert (no level rewards, cashback
	// conversions, or prizes) even with FEE_MODEL_V2_ENABLED on. Best-effort: a
	// failed mark only defers the reward to the user's next genuine activity (the
	// reward computation is idempotent and self-healing), so we log and move on.
	feeDistributionService.SetDirtyHook(func(ctx context.Context, userID uuid.UUID) {
		if err := rewardService.MarkUserDirty(ctx, userID); err != nil {
			log.Printf("[fee-v2] mark-dirty failed for user=%s (reward deferred to next activity): %v", userID, err)
		}
	})

	// Initialize email, push, and notification services
	emailService := services.NewEmailService(cfg.MailjetAPIKey, cfg.MailjetSecretKey)
	expoPush := services.NewExpoPushService(cfg.ExpoAccessToken)
	_ = services.NewNotificationService(config.DB, eventBus, wsHub, emailService, expoPush)

	// Initialize handlers with dependency injection
	h := handlers.NewHandlers(priceService, blockchainService, cfg, eventBus, emailService, otcService, p2pService)
	eh := handlers.NewExchangeHandler(p2pService)
	ch := handlers.NewCheckoutHandler(cfg, blockchainService, escrowService, eventBus, otcService)
	sch := handlers.NewServiceContactHandler(cfg, blockchainService, serviceContactService)

	// Wire the fee/TP v2 engine (SP3) into the fiat/on-chain fee-settled
	// handlers. Each site routes through handlers.settleFeeEarnings, which only
	// uses this engine when cfg.IsFeeModelV2Enabled(); while the flag is off the
	// legacy DistributeTPEarnings path stays live (nil here would be equivalent).
	h.FeeService = feeDistributionService
	ch.FeeService = feeDistributionService
	sch.FeeService = feeDistributionService
	// SP7 — make the claim-eligibility gate available to the signup velocity hook.
	h.EligibilityService = rewardEligibilityService
	mrh := handlers.NewMerchantRequestHandler(config.DB, eventBus)
	swh := handlers.NewSwapHandler(cfg, otcService, blockchainService)
	slh := handlers.NewSwapLiFiHandler(cfg.LiFiAPIKey, cfg.LiFiIntegratorID, cfg.LiFiIntegratorFee)
	psh := handlers.NewPrivateSaleHandler(config.DB, emailService, cfg)
	fch := handlers.NewFeeConfigHandler(config.DB, feeConfigService, cfg)
	// BLOCKER 3 — runnable fee/TP v2 cutover reset. Super-admin-gated route
	// (RequireP2PAdmin) + confirm-phrase guard. Run BEFORE the FEE_MODEL_V2_ENABLED
	// flip per the runbook in fee_distribution_reset.go.
	ftch := handlers.NewFeeTPCutoverHandler(config.DB)
	// SP7 admin claim-eligibility handler — reward-flag CRUD + eligibility review.
	rfh := handlers.NewRewardFlagHandler(config.DB, rewardEligibilityService)

	// SP6 root-publisher — turns the eligible (SP7) subset of SP5's per-user
	// claimable-MCGP balances into a CUMULATIVE merkle root the admin publishes
	// on-chain via MCGPRewardDistributor.setMerkleRoot (admin-signed; no backend
	// key). Dormant when REWARD_DISTRIBUTOR_ADDRESS is unset (publish errors
	// clearly, user claim-data returns empty). chainID = Sonic (same chain as MCGP).
	rewardMerkleService := services.NewRewardMerkleService(
		config.DB,
		rewardEligibilityService,
		cfg.RewardDistributorAddress,
		cfg.SonicChainID,
	)
	// rewardChainReader is the read-only on-chain reader for the distributor's
	// claimed[]/MCGP balanceOf. It's lifted out of the IsConfigured block so the
	// SAME reader feeds BOTH the SP6 reconciler AND the SP8 summary's claimed
	// adapter (totalClaimedMCGP). Built only when a distributor + RPC client +
	// MCGP token are all present; nil otherwise.
	var rewardChainReader services.RewardChainReader
	if rewardMerkleService.IsConfigured() {
		log.Printf("[sp6] reward distributor ready: %s (chainID=%d)", cfg.RewardDistributorAddress, cfg.SonicChainID)

		// SP6 reward reconciler + treasury monitor (design §5 SP6 bullet + §9):
		// once a minute, compares the published claimable ledger against on-chain
		// claimed[]/MCGP balance for the live root — logs reward_reconciler_drift_total
		// on a ledger-below-chain regression and reward_treasury_short_total when
		// the distributor can't cover outstanding claims (never partial-silent).
		// Read-only (no key). Registered only when an RPC client + MCGP token are
		// available so it can actually read the chain; Reconcile no-ops until an
		// admin publishes the first root. Mirrors the P2P chain reconciler.
		mcgpToken := blockchainService.TokenAddressForChainID(cfg.SonicChainID, "MCGP")
		if mcgpToken == "" {
			mcgpToken = cfg.MCGPTokenAddress
		}
		if sonicClient != nil && mcgpToken != "" {
			rewardChainReader = services.NewRewardChainReader(sonicClient.Client, mcgpToken)
		}
		if rewardChainReader != nil {
			scheduler.Register(jobs.NewRewardReconciler(rewardMerkleService, rewardChainReader, mcgpToken, time.Minute))
			log.Printf("[sp6] reward reconciler registered (interval=1m, mcgp=%s)", mcgpToken)
		} else {
			log.Printf("[sp6] reward reconciler dormant: no sonic RPC client or MCGP token address — cannot read on-chain claims/treasury")
		}
	} else {
		log.Printf("[sp6] reward distributor dormant: REWARD_DISTRIBUTOR_ADDRESS not set")
	}
	rdh := handlers.NewRewardDistributionHandler(rewardMerkleService)

	// Gate claim-data / prepare-claim on REMAINING claimable (cumulative − on-chain
	// claimed[account]) so a fully-claimed user is no longer handed a claim tx that
	// reverts "MCGP: nothing to claim" (and pays gas for it). Additive: when no
	// chain reader is available the merkle service keeps its membership-only
	// behavior. SetClaimedReader is nil-safe.
	rewardMerkleService.SetClaimedReader(rewardChainReader)

	// SP8 user-facing reward summary — the read-aggregator behind GET
	// /api/rewards/summary (the app's TP screen) + the prepare-claim convenience.
	// refresher = rewardService so a read folds the pending SP4 global share AND
	// awards newly-crossed levels before the level/claimable are computed (the
	// refresh is best-effort: a deferred conversion never hides accrual). pending =
	// feeDistributionService surfaces the not-yet-settled global-pool share. claimed
	// = an on-chain adapter over rewardChainReader.ClaimedOf so totalClaimedMCGP is
	// the user's real already-claimed amount and the hero claimable is reported NET
	// (cumulative − claimed) — the "Reward earned" figure drops after a claim and the
	// "Already claimed" row appears. NewOnChainClaimedReader returns nil (⇒ gross
	// cumulative, as before) when the distributor/RPC/token aren't all configured.
	rewardSummaryService := services.NewRewardSummaryService(
		config.DB,
		rewardService,
		feeDistributionService,
		services.NewOnChainClaimedReader(config.DB, rewardChainReader, cfg.RewardDistributorAddress),
	)
	rsh := handlers.NewRewardSummaryHandler(rewardSummaryService, rewardMerkleService)

	// Nomba — used for the Nigerian bank directory + name enquiry today,
	// and the auto-payout path in Phase 2. Returns nil when credentials
	// are absent; the handler then declines bank/resolve requests with a
	// 503 so dev environments keep booting.
	nombaClient := services.NewNombaClient(cfg.NombaBaseURL, cfg.NombaClientID, cfg.NombaClientSecret, cfg.NombaAccountID)
	if nombaClient == nil {
		log.Printf("Nomba dormant: NOMBA_CLIENT_ID / NOMBA_CLIENT_SECRET / NOMBA_ACCOUNT_ID not set — bank list and account verification disabled")
	}

	// P2P trade service — wired only when the contract address is set.
	// The handler is always constructed (so the Nomba-backed bank/resolve
	// routes work even before the escrow contract is deployed); trade
	// routes are gated separately on p2pHandler.IsConfigured().
	var (
		p2pTradeSvc   *services.P2PTradeService
		p2pMessageSvc *services.P2PMessageService
		merchantStats *services.MerchantStatsService
	)
	if cfg.P2PEscrowAddress != "" && p2pSonicClient != nil {
		// The trade service resolves escrow + EIP-712 domain (chainId, version)
		// per entity-chain from the registry, which probes eth_chainId per chain
		// at boot (see NewP2PEscrowRegistry / verifyP2PChainAtBoot). cfg.SonicChainID
		// is just the default chain for entity-less ops (CreateAd, pool ops); the
		// registry corrects the EIP-712 domain chainId to whatever the RPC reports
		// so signatures stay valid even if SONIC_CHAIN_ID drifts from the chain.
		p2pTradeSvc = services.NewP2PTradeService(config.DB, p2pEscrowRegistry, p2pService, eventBus, cfg.SonicChainID)
		// Resolve token symbols by the trade/ad's chainID so that a BSC
		// trade resolves to the BSC token address, not the Sonic one.
		p2pTradeSvc.SetTokenResolver(func(chainID int64, symbol string) string {
			addr := blockchainService.TokenAddressForChainID(chainID, symbol)
			log.Printf("[p2p tokenResolver] chainID=%d symbol=%s -> %s", chainID, symbol, addr)
			return addr
		})
		p2pTradeSvc.SetFloatOffsetMaxBps(cfg.P2PFloatOffsetMaxBps)
		// Wire the fee/TP v2 engine (SP3) — P2P is a NEW fee-settled integration.
		// Every release transition (ConfirmRelease, reconciler auto-release,
		// adminResolve→buyer) accrues the realized on-chain fee through a single
		// seam, gated on the flag. While FEE_MODEL_V2_ENABLED is off this is a
		// no-op and P2P keeps its pre-SP3 behavior of not accruing TP.
		p2pTradeSvc.SetFeeService(feeDistributionService, cfg.IsFeeModelV2Enabled())
		// Chat service — system messages auto-emitted on every trade
		// transition so the chat thread is self-documenting.
		p2pMessageSvc = services.NewP2PMessageService(config.DB, eventBus)
		p2pTradeSvc.SetMessageService(p2pMessageSvc)
		// Register the timeout watchdog — scans for pending_payment trades
		// past expires_at every minute and nudges participants to call the
		// permissionless cancelByTimeout. Phase 1 has no operator EOA, so
		// the watchdog itself doesn't broadcast a cancel tx.
		scheduler.Register(jobs.NewP2PTimeoutWatchdog(p2pTradeSvc, eventBus))
		// Register the chain reconciler — every 30s, walks non-terminal
		// trades and corrects DB drift against on-chain truth. Safety net
		// for dropped-response failures in the prepare/confirm flow.
		scheduler.Register(jobs.NewChainReconciler(p2pTradeSvc))
		// Register the merchant-reputation reconciler — recomputes windowed
		// stats nightly as a correctness backstop for the incremental path.
		merchantStats = services.NewMerchantStatsService(config.DB, eventBus, services.TrustedThresholds{
			MinReleased:   cfg.P2PTrustedMinReleased,
			MinCompletion: cfg.P2PTrustedMinCompletion,
			MinAgeDays:    cfg.P2PTrustedMinAgeDays,
		})
		scheduler.Register(jobs.NewMerchantReputationReconciler(merchantStats))
		if owner, err := p2pEscrowService.Owner(); err == nil {
			log.Printf("P2PEscrow contract owner (on-chain): %s", owner)
		}
	} else {
		log.Printf("P2P trade routes dormant: P2P_ESCROW_ADDRESS not set or chain client unavailable")
	}
	p2pHandler := handlers.NewP2PHandler(config.DB, p2pTradeSvc, p2pMessageSvc, nombaClient, merchantStats)

	// SP4 — global-pool settlement epoch. Folds the global TP pool's accrued
	// share into the per-user cumulative index once per epoch (default daily,
	// FEE_GLOBAL_POOL_SETTLE_INTERVAL). GATED on FEE_MODEL_V2_ENABLED: while the
	// flag is off the SP3 accrual path never runs, the pool stays empty, and
	// there's nothing to settle — so the worker is simply not registered
	// (dormant until cutover), independent of whether P2P is configured.
	if cfg.IsFeeModelV2Enabled() {
		interval := cfg.GlobalPoolSettleIntervalOrDefault()
		scheduler.Register(jobs.NewGlobalPoolSettler(feeDistributionService, interval))
		log.Printf("[fee-v2] global-pool settler registered (interval=%s)", interval)

		// SP5 — payout computation. Processes users with recent TP/cashback
		// activity (dirty-flagged, not a blind full scan): settles their pending
		// global share, awards newly-crossed cumulative level rewards, converts
		// cashback USD→MCGP at the locked OTC price, records L6+ prize_due rows,
		// and accumulates the per-user claimable-MCGP balance SP6 will publish.
		scheduler.Register(jobs.NewRewardProcessorJob(rewardService, time.Minute, 500))
		log.Printf("[fee-v2] reward processor registered (interval=1m)")
	} else {
		log.Printf("[fee-v2] global-pool settler + reward processor dormant: FEE_MODEL_V2_ENABLED=false")
	}

	// Backfill any pending transactions that already have a txHash.
	// These were submitted before the receipt-polling goroutine was added.
	go handlers.BackfillPendingTransactions(blockchainService)

	// Set Gin mode based on environment
	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create router with default middleware (logger and recovery)
	router := gin.Default()

	// Apply global middleware
	router.Use(middleware.CORS(cfg))
	router.Use(middleware.SecurityHeaders())
	router.Use(middleware.RequestLogger())

	// Override health check with DB status
	router.GET("/health", func(c *gin.Context) {
		dbStatus := "connected"
		if err := sqlDB.Ping(); err != nil {
			dbStatus = "disconnected"
		}

		c.JSON(http.StatusOK, gin.H{
			"status":   "ok",
			"message":  "TSA API is running",
			"database": dbStatus,
		})
	})

	// Setup all routes
	routes.SetupRoutes(router, cfg, h, ch, mrh, sch, swh, slh, psh, wsHub, eh, p2pHandler, fch, rfh, rdh, rsh, ftch)

	// Configure HTTP server
	port := cfg.Port
	if port == "" {
		port = "5000"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	// Start scheduler (no-op if no workers registered yet — future steps add them).
	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	defer schedulerCancel()
	scheduler.Start(schedulerCtx)

	// Start server in a goroutine
	go func() {
		log.Printf("TSA API server starting on port %s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Stop scheduler first so workers don't accept new work mid-shutdown.
	scheduler.Stop()

	// Stop event bus
	eventBus.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited gracefully")
}

// verifyEscrowAdmin logs the escrow contract's on-chain owner on startup so
// operators can sanity-check that the API is pointed at the right deployment.
// Admin role membership is verified per-request via IsAdmin — there's nothing
// to validate statically here.
func verifyEscrowAdmin(s *services.EscrowService) {
	onchain, err := s.Owner()
	if err != nil {
		log.Printf("WARN: could not read escrow owner() on startup: %v", err)
		return
	}
	log.Printf("Escrow contract owner (on-chain): %s", onchain)
}
