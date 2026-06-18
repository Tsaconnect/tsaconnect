package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
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

	// Fee/TP engine (SP3). Wired into every fee-settled path via OnFeeSettled.
	// TP minting rate comes from cfg (default 100 TP per $1, i.e. 1 TP = $0.01).
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
	log.Printf("[fee-v2] fee/TP engine ACTIVE (tpPerUSD=%.4f)", cfg.TPPerUSDOrDefault())
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

	// Initialize escrow service (sonic chain client). All Sonic-side services
	// (escrow, product-escrow, service-contact, OTC, P2P) follow SONIC_NETWORK:
	// resolve the client for the selected network so testnet operation actually
	// talks to testnet, falling back to the mainnet client if the selected
	// network has no sonic chain configured. (Previously ClientForChain pinned
	// these to mainnet — correct on mainnet, but it made SONIC_NETWORK=testnet a
	// no-op for the escrow stack; the configurable-FeeManager cutover needs the
	// whole Sonic stack on one network so the boot guard checks the right chain.)
	sonicClient := blockchainService.ClientForNetwork(cfg.SonicNetwork, "sonic")
	if sonicClient == nil {
		sonicClient = blockchainService.ClientForChain("sonic")
	}
	p2pSonicClient := sonicClient
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

	// Wire the fee/TP engine (SP3) into the fiat/on-chain fee-settled handlers.
	// Each site routes through handlers.settleFeeEarnings.
	h.FeeService = feeDistributionService
	ch.FeeService = feeDistributionService
	sch.FeeService = feeDistributionService
	// SP7 — make the claim-eligibility gate available to the signup velocity hook.
	h.EligibilityService = rewardEligibilityService
	// PMOnboarding is wired below after nombaClient is initialised; pass nil for now.
	mrh := handlers.NewMerchantRequestHandlerWithPM(config.DB, eventBus, nil)
	swh := handlers.NewSwapHandler(cfg, otcService, blockchainService)
	slh := handlers.NewSwapLiFiHandler(cfg.LiFiAPIKey, cfg.LiFiIntegratorID, cfg.LiFiIntegratorFee)
	// AllSwap server-verified settle (design §8, I8): re-queries LiFi + accrues
	// the verified integrator fee.
	slh.Config = cfg
	slh.FeeService = feeDistributionService
	psh := handlers.NewPrivateSaleHandler(config.DB, emailService, cfg)
	fch := handlers.NewFeeConfigHandler(config.DB, feeConfigService, cfg)

	// Admin-configurable reward payout-token mapping (PM: payout token flexible,
	// not hardcoded). Seeds the launch defaults on first boot; the SP5 reward
	// processor resolves the payout token/chain per reward via this service.
	rewardPayoutService, err := services.NewRewardPayoutConfigService(config.DB)
	if err != nil {
		log.Fatalf("Failed to init reward payout config: %v", err)
	}
	rph := handlers.NewRewardPayoutHandler(rewardPayoutService)
	// Wire the resolver into the SP5 processor so level/cashback rewards are
	// denominated in the admin-configured payout token (cashback follows the
	// payment token; TP-level → MCGP for launch).
	rewardService.SetPayoutResolver(rewardPayoutService)

	// Configurable-FeeManager admin set-fee flow + drift reconciler (design
	// §5/§10). DORMANT-BY-DEFAULT: a multi-chain FeeManager reader is built only
	// over the configured FeeManagerChains that also have a reachable RPC client.
	// With no FeeManager chain configured the reader is nil → the admin endpoints
	// surface "not configured" and the reconciler no-ops (gated like other dormant
	// integrations). The endpoints stay 409-gated on FEE_MODEL_V2_ENABLED too.
	var feeManagerReader services.FeeManagerChainReader
	if len(cfg.FeeManagerChains) > 0 {
		rpcByChain := map[int64]services.FeeManagerRPCClient{}
		for chainID := range cfg.FeeManagerChains {
			client, _ := blockchainService.ClientForChainID(chainID)
			if client != nil && client.Client != nil {
				rpcByChain[chainID] = client.Client
			}
		}
		feeManagerReader = services.NewMultiChainFeeManagerReaderFromClients(rpcByChain)
	}
	feeRateIntentService := services.NewFeeRateIntentService(config.DB, cfg, feeManagerReader)
	fah := handlers.NewFeeAdminHandler(cfg, feeRateIntentService, feeConfigService, feeManagerReader)
	if len(cfg.FeeManagerChains) > 0 && feeManagerReader != nil {
		// Hourly fee-rate drift reconciler (design §10): FeeManager (truth) vs the
		// fee_config replica; refresh-on-drift (chain wins) + alert; keep
		// last-known-good on RPC failure. No escrow-getter (A1) — only FeeManager↔replica.
		scheduler.Register(jobs.NewFeeManagerReconciler(feeConfigService, cfg, feeManagerReader, time.Hour))
		log.Printf("[fee-manager] drift reconciler registered (interval=1h, chains=%d)", len(cfg.FeeManagerChains))
	} else {
		log.Printf("[fee-manager] dormant: no FeeManager chain configured with a reachable RPC — admin set-fee flow + drift reconciler off")
	}

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

	// VERIFY a setMerkleRoot confirmation ON-CHAIN before flipping an epoch to
	// "published" (finding #3): ConfirmPublish/MarkPublished require the supplied
	// txHash to have mined successfully AND the distributor's live root to equal the
	// epoch's expected root, so a wrong/early/failed hash can't strand a whole
	// epoch's claims. The SAME rewardChainReader (an *evmRewardChainReader, which
	// also satisfies RewardPublishVerifier) is reused. Additive + nil-safe: with no
	// chain reader wired the prior trust-the-admin-hash behavior is preserved.
	if pv, ok := rewardChainReader.(services.RewardPublishVerifier); ok {
		rewardMerkleService.SetPublishVerifier(pv)
	}

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

	// Corporate PM onboarding — resume-able Nomba sub-account provisioning.
	// DORMANT-BY-DEFAULT: built only when Nomba creds are present (nombaClient != nil).
	// When nil, ApproveMerchantRequest on a p2p_merchant request returns a clear 503.
	if nombaClient != nil {
		mrh = handlers.NewMerchantRequestHandlerWithPM(config.DB, eventBus, services.NewPMOnboardingService(config.DB, nombaClient))
		log.Printf("[pm] onboarding service ready (resume-able Nomba provisioning)")
	} else {
		log.Printf("[pm] onboarding service dormant: Nomba creds absent — corporate PM approval disabled")
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
		// Wire the fee/TP engine (SP3) — P2P is a fee-settled integration.
		// Every release transition (ConfirmRelease, reconciler auto-release,
		// adminResolve→buyer) accrues the realized on-chain fee through a single seam.
		p2pTradeSvc.SetFeeService(feeDistributionService)
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

	// ── PM automation (Nomba-backed buy/sell auto-settle) ────────────────────
	// DORMANT-BY-DEFAULT, gated bottom-up:
	//   - signer: built only for a usable backend. "kms" is not implemented yet
	//     (Task 10) → nil + log. "local" needs PM_RELEASE_PRIVATE_KEY → nil if
	//     unset. The signer holds the ONE admin EOA key that broadcasts
	//     adminRelease, so no key ⇒ no automation.
	//   - pmAuto: built only when signer != nil AND a P2PTradeService exists
	//     (P2P itself configured). Uses the disabled Nomba gateway for now, so the
	//     buy flow works fully and the sell-payout path fails clearly until the
	//     live gateway lands (Task 9).
	//   - nwh (webhook): built only when NOMBA_WEBHOOK_SECRET is set AND pmAuto
	//     exists. Unset secret ⇒ the route stub 503s.
	//   - pmh (admin ops): built only when pmAuto exists.
	// With none of the PM env set the server boots with all of these nil and the
	// PM routes either don't register (admin) or 503 (webhook).
	pmSvc := services.NewPMService(config.DB)
	// Wire the PM NGN ledger into the trade service so PM sell-opens reserve the
	// fiat payout from the PM's NGN (and cancels release it). nil-safe: when P2P
	// is dormant p2pTradeSvc is nil and this is skipped.
	if p2pTradeSvc != nil {
		p2pTradeSvc.SetPMService(pmSvc)
	}
	pmNameLookup := func(ctx context.Context, id uuid.UUID) (string, error) {
		var u models.User
		if err := config.DB.WithContext(ctx).Select("name").First(&u, "id = ?", id).Error; err != nil {
			return "", err
		}
		return u.Name, nil
	}

	var releaseSigner services.ReleaseSigner
	switch cfg.PMReleaseSigner {
	case "kms":
		log.Printf("[pm] KMS signer not yet implemented; PM release disabled")
	default:
		if cfg.PMReleasePrivateKey != "" && sonicClient != nil {
			keyHex := strings.TrimPrefix(cfg.PMReleasePrivateKey, "0x")
			key, kerr := crypto.HexToECDSA(keyHex)
			if kerr != nil {
				log.Printf("[pm] invalid PM_RELEASE_PRIVATE_KEY; PM release disabled: %v", kerr)
			} else {
				releaseSigner = services.NewLocalKeyReleaseSigner(key, sonicClient)
				log.Printf("[pm] local release signer ready (admin EOA %s)", releaseSigner.Address())
				// Catch a signer/admin-EOA misconfig at boot rather than at the
				// first release: if the derived signer address doesn't match the
				// configured PM_ADMIN_EOA_ADDRESS, adminRelease will revert
				// on-chain because the key isn't a registered admin.
				if cfg.PMAdminEOAAddress != "" && !strings.EqualFold(releaseSigner.Address(), cfg.PMAdminEOAAddress) {
					log.Printf("[pm] WARNING: release signer address %s does not match configured PM_ADMIN_EOA_ADDRESS %s — adminRelease will revert on-chain until the key matches a registered admin", releaseSigner.Address(), cfg.PMAdminEOAAddress)
				}
			}
		} else {
			log.Printf("[pm] PM release disabled: PM_RELEASE_PRIVATE_KEY unset or sonic client unavailable")
		}
	}

	var (
		pmAuto *services.PMAutomationService
		pmh    *handlers.PMHandler
		nwh    *handlers.NombaWebhookHandler
	)
	if releaseSigner != nil && p2pTradeSvc != nil {
		// Gateway selection: live Nomba transfer gateway when client credentials
		// are configured, otherwise the dormant gateway (sell payouts error until
		// configured). Dormant-by-default per the repo rule.
		var nombaGateway services.NombaGateway = services.NewDisabledNombaGateway()
		var pmNombaClient *services.NombaClient
		if cfg.NombaClientID != "" && cfg.NombaClientSecret != "" && cfg.NombaAccountID != "" {
			nombaClient := services.NewNombaClient(cfg.NombaBaseURL, cfg.NombaClientID, cfg.NombaClientSecret, cfg.NombaAccountID)
			if nombaClient == nil {
				// NewNombaClient returns nil when any required credential is missing;
				// guard here in case the condition above diverges from its requirements.
				log.Printf("[pm] WARNING: Nomba client returned nil despite creds present — check NOMBA_BASE_URL, NOMBA_CLIENT_ID, NOMBA_CLIENT_SECRET, NOMBA_ACCOUNT_ID; gateway disabled")
			} else {
				nombaGateway = services.NewNombaHTTPGateway(nombaClient)
				pmNombaClient = nombaClient
				log.Printf("[pm] live Nomba gateway enabled (base=%s)", cfg.NombaBaseURL)
			}
		} else {
			log.Printf("[pm] Nomba gateway disabled (need NOMBA_CLIENT_ID + NOMBA_CLIENT_SECRET + NOMBA_ACCOUNT_ID) — sell payouts will error until configured")
		}
		pmAuto = services.NewPMAutomationService(config.DB, pmSvc, p2pTradeSvc, releaseSigner, pmNameLookup, nombaGateway, cfg.NombaSenderName)
		pmh = handlers.NewPMHandler(pmAuto, pmSvc)
		if cfg.NombaWebhookSecret != "" {
			nwh = handlers.NewNombaWebhookHandler(
				services.NewNombaWebhookVerifier(cfg.NombaWebhookSecret),
				services.NewNombaWebhookStore(config.DB),
				pmAuto,
			)
			log.Printf("[pm] Nomba webhook handler ready")
		} else {
			log.Printf("[pm] Nomba webhook dormant: NOMBA_WEBHOOK_SECRET not set")
		}
		// M2 liveness: periodically recover PM trades wedged at `paid` with no
		// release (auto-release failed after markPaid, broadcast never landed) and
		// re-dispatch failed/stale Nomba webhook events. Complements the chain
		// reconciler + timeout watchdog, which don't cover paid-orphans or dropped
		// dispatches.
		scheduler.Register(jobs.NewPMRecoveryJob(pmAuto))
		log.Printf("[pm] PM recovery job registered (paid-orphan re-drive + webhook re-dispatch)")
		// NGN ledger drift reconciler (VA-only model): compares the SUM of all
		// active PMs' off-chain available_ngn against the live single-parent Nomba
		// balance every 5m and logs drift (pm_ngn_reconciler_drift_total).
		// Read-only. Only registered when a live Nomba client exists (it needs
		// FetchParentBalance); dormant otherwise.
		if pmNombaClient != nil {
			scheduler.Register(jobs.NewPMNGNReconciler(
				&pmNGNLedgerSource{pm: pmSvc, nomba: pmNombaClient},
				5*time.Minute,
			))
			log.Printf("[pm] PM NGN reconciler registered (ledger-vs-Nomba drift, 5m)")
		} else {
			log.Printf("[pm] PM NGN reconciler dormant: no live Nomba client to fetch the parent balance")
		}
		log.Printf("[pm] PM automation ACTIVE (sell-payout fails until live Nomba gateway lands)")
	} else {
		log.Printf("[pm] PM automation dormant: no release signer or P2P trade service unavailable")
	}

	// SP4 — global-pool settlement epoch. Folds the global TP pool's accrued
	// share into the per-user cumulative index once per epoch (default daily,
	// FEE_GLOBAL_POOL_SETTLE_INTERVAL).
	{
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
	routes.SetupRoutes(router, cfg, h, ch, mrh, sch, swh, slh, psh, wsHub, eh, p2pHandler, fch, rfh, rdh, rsh, fah, rph, pmh, nwh)

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
// pmNGNLedgerSource adapts PMService + NombaClient to the
// jobs.PMNGNLedgerSource interface the NGN reconciler consumes. Kept in cmd/
// (not services/) so the services package never imports jobs (the dependency
// runs one way: jobs → services).
type pmNGNLedgerSource struct {
	pm    *services.PMService
	nomba *services.NombaClient
}

func (s *pmNGNLedgerSource) SumActiveAvailableNGN(ctx context.Context) (string, int, error) {
	return s.pm.SumActiveAvailableNGN(ctx)
}

func (s *pmNGNLedgerSource) FetchParentBalance(ctx context.Context) (string, error) {
	return s.nomba.FetchParentBalance(ctx)
}

// to validate statically here.
func verifyEscrowAdmin(s *services.EscrowService) {
	onchain, err := s.Owner()
	if err != nil {
		log.Printf("WARN: could not read escrow owner() on startup: %v", err)
		return
	}
	log.Printf("Escrow contract owner (on-chain): %s", onchain)
}
