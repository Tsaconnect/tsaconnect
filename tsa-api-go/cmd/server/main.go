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
	p2pEscrowService := services.NewP2PEscrowService(p2pSonicClient, cfg)

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

	// Initialize email and notification services
	emailService := services.NewEmailService(cfg.MailjetAPIKey, cfg.MailjetSecretKey)
	_ = services.NewNotificationService(config.DB, eventBus, wsHub, emailService)

	// Initialize handlers with dependency injection
	h := handlers.NewHandlers(priceService, blockchainService, cfg, eventBus, emailService, otcService, p2pService)
	eh := handlers.NewExchangeHandler(p2pService)
	ch := handlers.NewCheckoutHandler(cfg, blockchainService, escrowService, eventBus, otcService)
	sch := handlers.NewServiceContactHandler(cfg, blockchainService, serviceContactService)
	mrh := handlers.NewMerchantRequestHandler(config.DB, eventBus)
	swh := handlers.NewSwapHandler(cfg, otcService, blockchainService)
	slh := handlers.NewSwapLiFiHandler(cfg.LiFiAPIKey, cfg.LiFiIntegratorID, cfg.LiFiIntegratorFee)
	psh := handlers.NewPrivateSaleHandler(config.DB, emailService, cfg)

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
	)
	if cfg.P2PEscrowAddress != "" && p2pSonicClient != nil {
		p2pTradeSvc = services.NewP2PTradeService(config.DB, p2pEscrowService, p2pService, eventBus, cfg.SonicChainID)
		// Resolve token symbols against the active Sonic network so a
		// SONIC_NETWORK=testnet boot looks up testnet token addresses
		// instead of falling back to the mainnet defaults (which would
		// build a deposit() call against the wrong contract — the user's
		// testnet wallet has zero balance there, surfacing as a confusing
		// "ERC20: transfer amount exceeds balance" gas-estimate revert).
		network := cfg.SonicNetwork
		if network == "" {
			network = "mainnet"
		}
		p2pTradeSvc.SetTokenResolver(func(symbol string) string {
			addr := blockchainService.TokenAddressForNetwork(network, "sonic", symbol)
			log.Printf("[p2p tokenResolver] symbol=%s network=%s -> %s", symbol, network, addr)
			return addr
		})
		p2pTradeSvc.SetFloatOffsetMaxBps(cfg.P2PFloatOffsetMaxBps)
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
		if owner, err := p2pEscrowService.Owner(); err == nil {
			log.Printf("P2PEscrow contract owner (on-chain): %s", owner)
		}
	} else {
		log.Printf("P2P trade routes dormant: P2P_ESCROW_ADDRESS not set or chain client unavailable")
	}
	p2pHandler := handlers.NewP2PHandler(config.DB, p2pTradeSvc, p2pMessageSvc, nombaClient)

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
	routes.SetupRoutes(router, cfg, h, ch, mrh, sch, swh, slh, psh, wsHub, eh, p2pHandler)

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
