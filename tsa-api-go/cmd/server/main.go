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
	mrh := handlers.NewMerchantRequestHandler(config.DB, eventBus)
	swh := handlers.NewSwapHandler(cfg, otcService, blockchainService)
	slh := handlers.NewSwapLiFiHandler(cfg.LiFiAPIKey, cfg.LiFiIntegratorID, cfg.LiFiIntegratorFee)
	psh := handlers.NewPrivateSaleHandler(config.DB, emailService, cfg)
	fch := handlers.NewFeeConfigHandler(config.DB, feeConfigService)

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
	routes.SetupRoutes(router, cfg, h, ch, mrh, sch, swh, slh, psh, wsHub, eh, p2pHandler, fch)

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
