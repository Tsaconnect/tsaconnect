package main

import (
	"context"
	"flag"
	"log"
	"math/big"
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
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/chains/btc"
	"github.com/ojimcy/tsa-api-go/internal/chains/solana"
	"github.com/ojimcy/tsa-api-go/internal/chains/sui"
	"github.com/ojimcy/tsa-api-go/internal/chains/tron"
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

// pmProvisioner picks the fiat provisioner for corporate PM virtual accounts,
// honoring cfg.ResolveFiatProvider (same resolver as payouts / webhooks / bank
// directory, so they can never disagree). ResolveFiatProvider now always returns
// "flutterwave" (Nomba is retired), so this yields the Flutterwave static-VA
// provisioner when FW is configured, else a nil interface (not a typed-nil) that
// makes PMOnboardingService's `provisioner == nil` guard 503 ApprovePM. The
// trailing Nomba branch is kept as the dormant seam for a future provider and is
// currently unreachable.
func pmProvisioner(cfg *config.Config, fwClient *services.FlutterwaveClient, fwCustomerID string, nombaClient *services.NombaClient) services.NombaProvisioner {
	if cfg.ResolveFiatProvider() == "flutterwave" {
		// FW selected → FW or nothing. Never silently fall back to Nomba (which
		// we're migrating off, and whose sandbox caps VAs at 2 per holder): a
		// missing FW company-customer id must surface as a dormant provisioner
		// so ApprovePM 503s with a clear error, rather than routing a live PM to
		// the wrong provider and failing deep in Nomba's create-VA call.
		// Guard the typed-nil so the interface is a true nil (not a typed-nil).
		if p := services.NewFlutterwaveVAProvisioner(fwClient, fwCustomerID); p != nil {
			return p
		}
		return nil
	}
	if nombaClient != nil {
		return nombaClient
	}
	return nil
}

// p2pCentralRouting decides whether P2P buy screens may send buyers to the
// company account, and what to say when they may not.
//
// Telling a buyer where to send naira and being able to turn that naira back
// into their trade are two halves of one thing, but boot wires them far apart:
// routing needs only a live Instant Transfer service, while the P2P claim
// source is registered inside the PM-automation block, which additionally needs
// a release key and a chain client. With IT live, central mode on and PM
// automation dormant the halves disagreed — every buyer was pointed at the
// company account while nothing existed to attribute their transfer, so every
// P2P payment held indefinitely, and the only boot warning covered the inverse
// case.
//
// It REFUSES to route rather than warning and routing anyway. The fallback is
// the merchant's own account, which still attributes the payment and still
// settles the trade; a warning only records the loss after a night of holds
// nobody was reading the log to prevent. Central mode is a boot-time config, so
// the operator who set it can see the refusal at the same moment they set it.
func p2pCentralRouting(itLive, p2pClaimsRegistered, centralEnabled bool) (route bool, warning string) {
	if itLive && p2pClaimsRegistered {
		return true, ""
	}
	// Off the cutover there is nothing to warn about: merchant accounts are
	// where buyers are supposed to be sent, and staging INBOUND_CENTRAL_* with
	// the mode off is how a cutover is prepared.
	if !centralEnabled {
		return false, ""
	}
	if !itLive {
		return false, "Instant Transfer is dormant, so P2P cannot reach the company account and " +
			"will keep showing merchant virtual accounts in central mode"
	}
	return false, "P2P buyers are being kept on merchant accounts in central mode: the P2P claim " +
		"source did not register (PM automation is dormant — it needs PM_RELEASE_PRIVATE_KEY and a " +
		"Sonic client), so a payment into the company account would have nothing to attribute it to"
}

// isNUBAN reports whether s is a Nigerian NUBAN: exactly ten digits.
//
// Deliberately shape-only. It cannot catch a transposed digit — only a name
// enquiry against the bank can — but every customer's money is routed by this
// one string, so a truncated paste or a stray character is worth catching at
// boot rather than from a support ticket.
func isNUBAN(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 10 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func main() {
	migrateFlag := flag.Bool("migrate", false,
		"run database migrations and exit; used by the deploy between build and restart")
	flag.Parse()

	// Load .env from the PROCESS WORKING DIRECTORY, and say which one that was.
	//
	// Naming the directory is the whole point of these two lines. Under systemd
	// the unit sets WorkingDirectory=/srv/tsa-api/current and supplies config
	// through EnvironmentFile=/srv/tsa-api/shared/env, so no .env is expected
	// there — but an operator who edits a .env inside their own clone gets no
	// signal that nothing reads it. The file is real, the edit looks applied,
	// and the service keeps running on the old value. "No .env file found" did
	// not distinguish that case from a normal systemd boot; the path does.
	//
	// Precedence, for the same reason: godotenv NEVER overrides a variable that
	// is already set, so wherever both exist, systemd's EnvironmentFile wins.
	wd, _ := os.Getwd()
	if err := godotenv.Load(); err != nil {
		log.Printf("[config] no .env in %q — using the process environment", wd)
	} else {
		log.Printf("[config] loaded .env from %q (does not override variables already set)", wd)
	}

	// Load configuration
	cfg := config.Load()

	// Central inbound mode sends every payer to one company account, so an
	// unset account number is not a dormant feature — it is a pay-into screen
	// with a blank number on it, and users transferring money nowhere. There is
	// no safe degraded mode, so refuse to boot rather than serve it.
	if cfg.InboundCentralEnabled() {
		central := services.CentralAccount{
			AccountNumber: cfg.InboundCentralAccountNumber,
			BankName:      cfg.InboundCentralBankName,
			AccountName:   cfg.InboundCentralAccountName,
		}
		if !central.IsConfigured() {
			log.Fatalf("[config] REFUSING TO START: INBOUND_MODE=central requires " +
				"INBOUND_CENTRAL_ACCOUNT_NUMBER, INBOUND_CENTRAL_BANK_NAME and " +
				"INBOUND_CENTRAL_ACCOUNT_NAME to all be set")
		}
		// A NUBAN is ten digits. This will not catch a transposition — only the
		// bank can — but it catches a truncated paste, a stray space, or a sort
		// code pasted into the wrong variable, and every one of those sends
		// every customer's money to an account we do not own.
		if !isNUBAN(central.AccountNumber) {
			log.Fatalf("[config] REFUSING TO START: INBOUND_CENTRAL_ACCOUNT_NUMBER %q "+
				"is not a 10-digit NUBAN", central.AccountNumber)
		}
		// The wallet-statement poll is the ONLY way a central credit is ever
		// seen — wallet funding fires no webhook. Booting central without
		// Flutterwave credentials serves pay-into screens for an account
		// nothing reads, so money arrives and not even a `held` row exists.
		if strings.TrimSpace(cfg.FlutterwaveClientID) == "" || strings.TrimSpace(cfg.FlutterwaveClientSecret) == "" {
			log.Fatalf("[config] REFUSING TO START: INBOUND_MODE=central requires Flutterwave " +
				"credentials — the wallet-statement poll is the only way a credit is ever seen")
		}
		// A held credit is customer money nobody has been able to attribute,
		// and by design nothing releases it automatically — no refund timer, no
		// expiry. The alert is the ONLY thing that tells anyone to go look.
		//
		// The dangerous state is the CONFIGURED one: an operator who sets
		// INBOUND_ALERT_EMAILS is entitled to believe they are paged, and if
		// Mailjet has no credentials every one of those alerts is discarded at
		// the first call without anyone noticing. An empty list is merely a
		// choice — nobody is paged and nobody thinks otherwise — so it says so
		// plainly and does not shout. Neither is fatal: the admin queue still
		// holds the evidence, and a mail outage must not stop the API taking
		// money.
		switch {
		case len(cfg.InboundAlertEmails) == 0:
			log.Printf("[config] INBOUND_MODE=central with no INBOUND_ALERT_EMAILS — " +
				"held credits will raise no alert and are only visible in the admin queue")
		case !services.MailerConfigured(cfg.MailjetAPIKey, cfg.MailjetSecretKey):
			log.Printf("[config] WARNING: INBOUND_ALERT_EMAILS names %d recipient(s) but Mailjet is not configured — "+
				"every hold alert will be dropped and nobody at those addresses will hear that money is stuck",
				len(cfg.InboundAlertEmails))
		default:
			log.Printf("[config] inbound hold alerts will go to %d recipient(s)", len(cfg.InboundAlertEmails))
		}
	}

	// Build the non-EVM chain registry. Each (network, chain) is registered only
	// when its config is present, so absent combos stay dormant (ForKey → false).
	// mainnet + testnet can be live at once, mirroring the EVM Networks map.
	chainRegistry := chains.NewRegistry()
	for _, tcfg := range cfg.NonEVMChains {
		switch tcfg.Family {
		case "tron":
			tg := tron.NewTronGrid(tcfg.RPCURL, tcfg.APIKey)
			chainRegistry.Register(tcfg.Network, tcfg.ChainKey, tron.NewClient(tcfg, tg))
			log.Printf("chains: %s/%s registered (%s)", tcfg.Network, tcfg.ChainKey, tcfg.RPCURL)
		case "btc":
			esp := btc.NewEsplora(tcfg.RPCURL)
			chainRegistry.Register(tcfg.Network, tcfg.ChainKey, btc.NewClient(tcfg, esp))
			log.Printf("chains: %s/%s registered (%s)", tcfg.Network, tcfg.ChainKey, tcfg.RPCURL)
		case "solana":
			srpc := solana.NewRPC(tcfg.RPCURL)
			chainRegistry.Register(tcfg.Network, tcfg.ChainKey, solana.NewClient(tcfg, srpc))
			log.Printf("chains: %s/%s registered (%s)", tcfg.Network, tcfg.ChainKey, tcfg.RPCURL)
		case "sui":
			srpc := sui.NewRPC(tcfg.RPCURL)
			chainRegistry.Register(tcfg.Network, tcfg.ChainKey, sui.NewClient(tcfg, srpc))
			log.Printf("chains: %s/%s registered (%s)", tcfg.Network, tcfg.ChainKey, tcfg.RPCURL)
		}
	}
	if len(chainRegistry.Keys()) == 0 {
		log.Printf("chains: no non-EVM chains configured (dormant)")
	}

	// Connect to PostgreSQL
	_, err := config.ConnectDB(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}

	// Schema migration runs once per deploy, not once per process — several
	// processes share this database during a cutover and would race on DDL locks.
	switch resolveMigrateMode(*migrateFlag, os.Getenv("MIGRATE_ON_BOOT")) {
	case migrateOnly:
		if err := config.AutoMigrate(); err != nil {
			log.Fatalf("Failed to auto-migrate database: %v", err)
		}
		log.Println("[migrate] schema migration complete")
		return
	case migrateOnBoot:
		log.Println("[migrate] MIGRATE_ON_BOOT set — migrating during boot (development path)")
		if err := config.AutoMigrate(); err != nil {
			log.Fatalf("Failed to auto-migrate database: %v", err)
		}
	case migrateSkip:
		log.Println("[migrate] skipped at boot; deploy runs `-migrate` (set MIGRATE_ON_BOOT=true for local dev)")
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

	// Backfill supported_tokens.chains for rows created before a chain was
	// added to a token's default set (e.g. USDT/USDC predate Arbitrum/Base
	// support) — additive-only, safe to run on every boot regardless of
	// migrate mode. See EnsureSupportedTokenChains for the bug this fixes.
	handlers.EnsureSupportedTokenChains()

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

	// Exchange-rate service (OER + Bybit P2P hybrid). Constructed early because IT
	// pricing (below) converts MCGP's USD price to Naira against this feed.
	// Currency registry — the sole validation authority for any currency
	// reaching a money path, and the source of per-currency payout floors.
	// Seeds missing rows without overwriting operator-tuned values.
	currencyRegistry := services.NewCurrencyRegistry(config.DB)

	p2pService := services.NewP2PService(
		cfg.OpenExchangeRatesAppID,
		cfg.SupportedCurrencies,
		cfg.BybitP2PCurrencies,
		cfg.BybitProbeAmounts,
	)

	// MCGP Swap Contract B (entitlement-ledger swap) — premium buy/spend/sell +
	// legacy redeem. Dormant unless MCGP_SWAP_B_ADDRESS is set; the handler is
	// nil in that case so its /api/mcgp routes are not registered.
	var mcgpSwapHandler *handlers.MCGPSwapHandler
	var itHandler *handlers.ITHandler
	var itService *services.ITService // hoisted: the PM block wires automation onto it
	mcgpSwapService := services.NewMCGPSwapService(sonicClient, cfg)
	if mcgpSwapService.IsConfigured() {
		mcgpSwapHandler = handlers.NewMCGPSwapHandler(config.DB, mcgpSwapService)
		log.Printf("[config] MCGP swap (Contract B) enabled: %s", cfg.MCGPSwapBAddress)

		// IT (Instant Transfer) operator signer — dev path uses the local key
		// (prod MUST use KMS). Built only when the key is set AND a chain client
		// exists; otherwise IT stays dormant.
		if !rawSignerKeyAllowed(cfg) && cfg.ITOperatorPrivateKey != "" {
			// The raw-env-key local signer is a dev/staging convenience. Refuse it
			// in production unless ALLOW_RAW_SIGNER_KEYS opts back in — the
			// interim path until the operator key comes from KMS. IT stays dormant.
			log.Printf("[config] IT dormant: raw IT_OPERATOR_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)")
		} else if cfg.ITOperatorPrivateKey != "" && sonicClient != nil {
			if key, kerr := crypto.HexToECDSA(cfg.ITOperatorPrivateKey); kerr != nil {
				log.Printf("[config] IT operator key invalid; IT dormant: %v", kerr)
			} else {
				alertRawSignerKeyInProduction(cfg, "IT_OPERATOR_PRIVATE_KEY")
				mcgpSwapService.SetOperatorSigner(services.NewLocalKeyReleaseSigner(key, sonicClient))
				itService = services.NewITService(config.DB, mcgpSwapService, services.NewP2PFxProvider(p2pService))
				itService.SetCompanyPMID(cfg.ITPMID)
				itService.SetMinSellNgnKobo(cfg.ITMinSellNgnKobo)
				itService.SetCurrencyRegistry(currencyRegistry)
				itService.SetInboundCentral(cfg.InboundCentralEnabled(), services.CentralAccount{
					AccountNumber: cfg.InboundCentralAccountNumber,
					BankName:      cfg.InboundCentralBankName,
					AccountName:   cfg.InboundCentralAccountName,
				})
				itHandler = handlers.NewITHandler(config.DB, itService, currencyRegistry)
				log.Printf("[config] IT (Instant Transfer) enabled; operator=%s", mcgpSwapService.OperatorAddress())
			}
		} else {
			log.Printf("IT routes dormant: IT_OPERATOR_PRIVATE_KEY not set")
		}

		// Gasless relaying (EIP-2771). Dormant unless BOTH the forwarder address
		// and a relayer key are configured; with either missing the app self-pays.
		if !rawSignerKeyAllowed(cfg) && cfg.RelayerPrivateKey != "" {
			// Same rule the IT operator key already follows: a raw key out of the
			// environment is a dev/staging convenience, refused in production
			// unless ALLOW_RAW_SIGNER_KEYS opts back in. The relayer key is
			// HOTTER than the operator key — it signs continuously from a
			// network-facing process — so if anything it deserves the stricter
			// treatment. Gasless stays dormant; the app self-pays.
			log.Printf("[config] gasless dormant: raw RELAYER_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)")
		} else if cfg.MCGPForwarderAddress != "" && cfg.RelayerPrivateKey != "" && sonicClient != nil {
			if key, kerr := crypto.HexToECDSA(cfg.RelayerPrivateKey); kerr != nil {
				log.Printf("[config] relayer key invalid; gasless dormant: %v", kerr)
			} else {
				alertRawSignerKeyInProduction(cfg, "RELAYER_PRIVATE_KEY")
				signer := services.NewLocalKeyReleaseSigner(key, sonicClient)
				limits := services.RelayerLimits{
					SponsorLimits: services.SponsorLimits{
						PerUserPerWindow: cfg.RelayerPerUserPerHour,
						Window:           time.Hour,
						DailyGasBudget:   bigFromDecimal(cfg.RelayerDailyGasBudget),
						BalanceFloor:     bigFromDecimal(cfg.RelayerBalanceFloorWei),
					},
					MaxDeadline:  cfg.RelayerMaxDeadline,
					GasCeiling:   cfg.RelayerGasCeiling,
					MinSponsored: bigFromDecimal(cfg.RelayerMinSponsored),
				}
				allow := []services.AllowedCall{
					// spend(to, amount, ref) — amount is calldata word 1.
					{Target: cfg.MCGPSwapBAddress, Selector: services.SelectorOf("spend(address,uint256,bytes32)"), AmountWordIndex: 1},
				}
				relayer, rerr := services.NewRelayerService(
					signer, services.EVMRelayChain{Client: sonicClient},
					services.EVMRelayTxBuilder{Client: sonicClient},
					cfg.MCGPForwarderAddress, allow, limits, nil, nil,
				)
				if rerr != nil {
					log.Printf("[config] relayer init failed; gasless dormant: %v", rerr)
				} else if serr := relayer.AssertRoleSeparation(context.Background(), services.SwapRoleReader{Swap: mcgpSwapService}); serr != nil {
					// Hard-fail rather than run: a relayer key that is also the
					// operator turns a hot-key compromise into an operator one.
					log.Fatalf("[config] REFUSING TO START: %v", serr)
				} else {
					mcgpSwapHandler = mcgpSwapHandler.WithRelayer(relayer, cfg.MCGPSwapBAddress)
					log.Printf("[config] gasless relaying enabled; forwarder=%s relayer=%s", cfg.MCGPForwarderAddress, signer.Address())
				}
			}
		} else {
			log.Printf("gasless relaying dormant: MCGP_FORWARDER_ADDRESS / RELAYER_PRIVATE_KEY not set")
		}
	} else {
		log.Printf("MCGP swap routes dormant: MCGP_SWAP_B_ADDRESS not set")
	}

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

	// Utility bills — ClubKonnect vend paid with spendable MCGP via the operator
	// debit path. Needs BOTH the vendor credentials and the MCGP operator signer
	// (set in the IT block above); missing either keeps /api/bills dormant.
	var billHandler *handlers.BillHandler
	var billService *services.BillService
	clubkonnect := services.NewClubkonnectClient(cfg)
	if clubkonnect.IsEnabled() && mcgpSwapService.HasOperator() {
		billPaymentService := services.NewBillPaymentService(config.DB, mcgpSwapService)
		billService = services.NewBillService(
			config.DB, clubkonnect, billPaymentService, mcgpSwapService,
			services.NewP2PFxProvider(p2pService), cfg.BillsDataMarkupBps, cfg.BillsQuoteTTL,
		)
		billHandler = handlers.NewBillHandler(billService)
		log.Printf("[config] utility bills enabled (ClubKonnect)")
	} else if clubkonnect.IsEnabled() {
		log.Printf("[config] bills dormant: ClubKonnect configured but no MCGP operator signer")
	}

	// Initialize event bus and WebSocket hub
	eventBus := events.NewBus()
	wsHub := ws.NewHub()

	// Initialize the in-process periodic-worker scheduler. Workers are
	// registered by feature wiring (Step 7 timeout watchdog, Step 9 chain
	// reconciler, etc.). Phase 1 keeps this in-process — see
	// internal/jobs/scheduler.go for the rationale and the Phase 2 migration
	// path to River.
	scheduler := jobs.NewScheduler()
	// Cancelled at shutdown. Background work started outside the scheduler (an
	// on-demand inbound check) derives from it so it stops with everything else.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	// MCGP swap (Contract B) read-only backing/stale-pending reconciler. Gated on
	// the swap being configured; never moves money (alerts only).
	if mcgpSwapService.IsConfigured() {
		scheduler.Register(jobs.NewMCGPSwapReconciler(mcgpSwapService, config.DB, time.Minute), jobs.AlertOnFailureStreak())
	}

	// Instant Transfer: close unpaid MCGP buy orders the settle path has
	// already stopped honouring, so the hub stops showing a user an order
	// nothing will fulfil. Registered wherever IT is live — it needs no
	// provider, only the database.
	if itService != nil {
		scheduler.Register(jobs.NewBuyOrderExpiry(itService, time.Hour), jobs.AlertOnFailureStreak())
		// MCGP sells stopped between the on-chain debit and the naira payout:
		// resolved from the debit record and Flutterwave's transfer record only.
		scheduler.Register(jobs.NewITSellReconciler(itService, 2*time.Minute), jobs.AlertOnFailureStreak())
	}

	// Booking requests: close the ones nobody answered, and — the reason this
	// matters — stop an approved, provider-priced quote staying payable forever.
	// Database-only, so it runs wherever the API does.
	scheduler.Register(jobs.NewBookingRequestExpiry(
		services.NewBookingRequestService(config.DB), time.Hour))

	// Bill orders: drive lost vends / deferred settles to terminal states, and
	// watch the prepaid ClubKonnect float (alert-only).
	if billService != nil {
		scheduler.Register(jobs.NewBillReconciler(billService, time.Minute), jobs.AlertOnFailureStreak())
		scheduler.Register(jobs.NewBillFloatMonitor(clubkonnect, cfg.BillsFloatAlertNgn, 10*time.Minute))
		// Dual-write drift watch. The legacy NGN columns and pm_balances are
		// written together; this is the only thing that would notice if they
		// stopped agreeing, and it must read clean before the legacy columns are
		// ever dropped. Read-only.
		scheduler.Register(jobs.NewPMBalanceDivergenceMonitor(
			config.DB, services.DescribePMBalanceDivergence, 10*time.Minute))
	}

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
	// Spot-listing reads price MCGP from Contract B's on-chain quote (nil-safe;
	// falls back to the IT rate when the swap isn't configured).
	h.MCGPSwap = mcgpSwapService
	// Non-EVM chain registry (dormant-safe: handlers return 503 when ChainRegistry
	// is nil or a given chain key is absent from the registry).
	h.ChainRegistry = chainRegistry
	eh := handlers.NewExchangeHandler(p2pService)
	ch := handlers.NewCheckoutHandler(cfg, blockchainService, escrowService, eventBus, otcService)
	sch := handlers.NewServiceContactHandler(cfg, blockchainService, serviceContactService)

	// Wire the fee/TP engine (SP3) into the fiat/on-chain fee-settled handlers.
	// Each site routes through handlers.settleFeeEarnings. GATED on the v2
	// kill-switch: when FEE_MODEL_V2_ENABLED is OFF (default) these stay nil and
	// every live charge path no-ops the engine, behaving exactly as v1. The
	// reward subsystem (SP4/SP5/SP6) keeps its dependency on the constructed
	// service but is independently dormant (no accrual ⇒ nothing to compute).
	if cfg.FeeModelV2Enabled {
		log.Printf("[fee-v2] FEE_MODEL_V2_ENABLED=true — wiring the v2 fee/TP engine into live charge paths")
		h.FeeService = feeDistributionService
		ch.FeeService = feeDistributionService
		sch.FeeService = feeDistributionService
	} else {
		log.Printf("[fee-v2] FEE_MODEL_V2_ENABLED=false — v2 fee/TP engine DORMANT; live fee paths run v1 behavior")
	}
	// SP7 — make the claim-eligibility gate available to the signup velocity hook.
	h.EligibilityService = rewardEligibilityService
	// PMOnboarding is wired below after nombaClient is initialised; pass nil for now.
	mrh := handlers.NewMerchantRequestHandlerWithPM(config.DB, eventBus, nil)
	swh := handlers.NewSwapHandler(cfg, otcService, blockchainService)
	// OTC listing controls — PM buy/sell kill-switches + listing metadata for
	// the app's Easyswap "OTC" tab (DB-only; no on-chain interaction).
	osh := handlers.NewOTCSettingsHandler(config.DB)
	// Fee wallet — the admin-assigned destination for system fees. The per-chain
	// escrow registry is passed so the panel can compare the assignment against
	// what every deployed escrow actually pays, contract by contract.
	fwlh := handlers.NewFeeWalletHandler(config.DB, cfg.SystemWalletAddress, p2pEscrowRegistry)
	// Reward programme enrolment forms — reuses the existing eligibility and
	// referral-graph services rather than restating their rules.
	rewardClaimService := services.NewRewardClaimService(config.DB, rewardEligibilityService, referralGraphService)
	rch := handlers.NewRewardClaimHandler(config.DB, rewardClaimService, referralGraphService)
	// Admin-editable per-feature fee splits — the engine consults these before
	// falling back to its compiled-in table.
	feeSplitConfigService := services.NewFeeSplitConfigService(config.DB)
	feeDistributionService.WithSplitConfigs(feeSplitConfigService)
	tph := handlers.NewTPAdminHandler(config.DB, feeSplitConfigService, feeDistributionService)
	slh := handlers.NewSwapLiFiHandler(cfg.LiFiBaseURL, cfg.LiFiAPIKey, cfg.LiFiIntegratorID, cfg.LiFiIntegratorFee)
	// AllSwap server-verified settle (design §8, I8): re-queries LiFi + accrues
	// the verified integrator fee.
	slh.Config = cfg
	// Values the from-amount when LiFi cannot be re-queried; without it that
	// path has no USD basis and defers the fee rather than guessing one.
	slh.PriceService = priceService
	// Rates come from fee_config (what admins edit), not the env constant.
	slh.FeeConfigService = feeConfigService
	slh.StatusClient = services.NewLiFiStatusClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey)
	if cfg.FeeModelV2Enabled {
		slh.FeeService = feeDistributionService
	}
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
	// surface "not configured" (503) and the reconciler no-ops (gated like other
	// dormant integrations). NOTE: these admin endpoints are gated by FeeManager
	// chain configuration, NOT by FEE_MODEL_V2_ENABLED — the v2 kill-switch only
	// governs whether the engine is wired into live charge paths.
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

	// Collects platform fees for swaps whose client never called settle. Without
	// it, a swap whose app was killed between broadcast and settle owes a fee
	// that nothing in the system would ever notice.
	if cfg.FeeModelV2Enabled && feeDistributionService != nil {
		scheduler.Register(&jobs.SwapFeeSweeper{
			DB:           config.DB,
			FeeService:   feeDistributionService,
			PriceService: priceService,
			FeeBps:       cfg.AllSwapNominalFeeBpsOrDefault(),
		})
		log.Printf("[fees] swap-fee sweeper registered (grace=10m, %d bps)",
			cfg.AllSwapNominalFeeBpsOrDefault())
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

	// Flutterwave v4 — the preferred fiat gateway (Nomba declined production
	// API access, 2026-07). Dormant-by-default: nil without creds.
	fwClient := services.NewFlutterwaveClient(cfg.FlutterwaveBaseURL, cfg.FlutterwaveIdpURL, cfg.FlutterwaveClientID, cfg.FlutterwaveClientSecret)
	if fwClient == nil {
		log.Printf("Flutterwave dormant: FLUTTERWAVE_CLIENT_ID / FLUTTERWAVE_CLIENT_SECRET not set")
	}

	// The one operator alert channel for inbound money the pipeline cannot
	// account for: a credit it refused to attribute, and a window where our
	// ledger disagrees with the provider's. Both are read by the same people
	// out of the same INBOUND_ALERT_EMAILS list, and building it once here is
	// what stops the matcher and the reconciler drifting onto different lists.
	//
	// Constructed unconditionally — with no recipients or no mailer it is a
	// no-op, never a nil the callers have to guard.
	inboundAlerts := services.NewInboundHoldNotifier(emailService, cfg.InboundAlertEmails, cfg.AdminConsoleBaseURL())

	// Workers registered with jobs.AlertOnFailureStreak page the same people
	// when they fail every tick for ~5 minutes (2026-09-11..17: the inbound
	// poller and reconciler failed for six days and only a log line knew).
	// Every such worker is a money path, so this is the same audience as a
	// held credit rather than a new list to configure.
	scheduler.SetFailureNotifier(workerFailureAlerts{to: inboundAlerts})

	// Central-account inbound attribution (design 2026-09-10). Polls the
	// Flutterwave wallet statement and writes every customer credit to
	// inbound_credits. There is NO webhook for wallet funding, so this poll is
	// the only way such a credit is ever seen.
	//
	// Registered in BOTH inbound modes on purpose: under `va` it attributes
	// nothing and simply records what lands, which is how the design wants the
	// statement observed against real traffic before the flag flips. The
	// matching step is wired separately via SetMatcher, below, once the products
	// that can claim a credit have been built — ingestion is deliberately useful
	// on its own.
	var inboundPoller *jobs.InboundCreditPoller
	if fwClient != nil {
		inboundPoller = jobs.NewInboundCreditPoller(
			services.NewInboundIngestor(config.DB, fwClient, "NGN", cfg.InboundStatementLookback),
			cfg.InboundPollIntervalOrDefault(),
		)
		inboundPoller.SetCheckCooldown(cfg.InboundCheckCooldown)
		scheduler.Register(inboundPoller, jobs.AlertOnFailureStreak())
		log.Printf("[inbound] credit poller registered (mode=%s interval=%s lookback=%s)",
			cfg.InboundMode, cfg.InboundPollIntervalOrDefault(), cfg.InboundStatementLookback)

		// The safety net over that poll (design §7). Per-user VAs made
		// attribution cryptographic; a free-text sender name and a poll with no
		// webhook backstop do not, so this is what tells us the pipeline has not
		// silently lost or duplicated money. It compares Σ inbound_credits
		// against Flutterwave's own credit total for a settled window and alerts
		// in BOTH directions — short means a customer paid and nobody has their
		// money, long means one payment was recorded twice.
		//
		// Read-only, and registered alongside the poller in both modes for the
		// same reason: what it checks is the ingestor, which runs in both.
		//
		// The gap alert is wired here rather than left to the log line it
		// shipped with, because a short gap is the one finding with NO other
		// discovery path: the credit never reached inbound_credits, so there is
		// no held row in the admin queue for anyone to come across.
		inboundReconciler := jobs.NewInboundReconciler(
			jobs.NewInboundReconcileSource(config.DB, fwClient),
			"NGN",
			5*time.Minute,
		)
		inboundReconciler.SetNotifier(inboundGapAlerts{to: inboundAlerts})
		scheduler.Register(inboundReconciler, jobs.AlertOnFailureStreak())
		log.Printf("[inbound] reconciler registered (Σ inbound_credits vs statement credits, 5m, both directions)")
	} else {
		log.Printf("[inbound] credit poller dormant: no Flutterwave client")
	}

	// Wire IT's live withdrawal FX quote (GET /api/it/withdraw/quote) onto
	// Flutterwave's pass-through rate feed. Optional seam — nil fwClient (or
	// IT dormant) leaves QuoteWithdrawal returning ErrITQuoteUnavailable.
	if itService != nil && fwClient != nil {
		itService.SetRateQuoter(fwClient)
		itService.SetFlutterwaveClient(fwClient)
		log.Printf("[it] withdrawal FX quote + per-user fiat VA activate wired (Flutterwave)")
	}

	// Attribution: decide which user each recorded inbound credit belongs to
	// (design §5.3). Wired after IT so the claim source sees a fully configured
	// service, and only when there is a poller to drive it.
	//
	// Registered in BOTH inbound modes, like the ingestor. Under `va` the MCGP
	// claim source still lists open buy orders, but no customer is being told to
	// pay into the central account, so in practice nothing matches and every
	// stray credit holds — which is exactly the shadow run the design asks for
	// before the flag flips.
	//
	// Held outside the block so the P2P source can join later: it needs the PM
	// service, the trade service and the release signer, none of which exist
	// yet at this point in boot. A product that registers late still matches
	// everything ingested before it — credits are durable and re-run.
	var inboundMatcher *services.InboundMatcher

	// Operator queue over held inbound credits (central-account design §8).
	//
	// ALWAYS registered, in both inbound modes and with or without Flutterwave
	// credentials. The ingestor records credits regardless of mode, and a held
	// credit has no automatic exit — a queue that only exists when the poller
	// is healthy is a queue nobody can drain after an outage.
	//
	// Constructed HERE, before any claim source exists, so registration can go
	// through one path (registerInboundClaim below) instead of two lists that
	// have to be kept in step by hand.
	inboundAdminService := services.NewInboundAdminService(config.DB)

	// registerInboundClaim is the ONLY way a product joins inbound attribution.
	//
	// The matcher decides who a credit belongs to; the hold queue explains to an
	// operator why it could not decide. Those must consult the SAME sources — a
	// product registered with one and not the other produces holds whose
	// evidence view is silently empty, which is worse than no queue at all
	// because it looks like an answer. One call, both registries.
	registerInboundClaim := func(claimType string, src services.ClaimSource) {
		if inboundMatcher != nil {
			inboundMatcher.Register(claimType, src)
		}
		inboundAdminService.AddClaimSource(claimType, src)
	}

	if inboundPoller != nil && itService != nil {
		inboundMatcher = services.NewInboundMatcher(config.DB)
		// Without this the matcher holds credits and tells nobody: the queue
		// has no automatic exit and is only discoverable by an operator
		// deciding to open it.
		inboundMatcher.SetHoldNotifier(inboundAlerts)
		inboundAdminService.SetBinder(inboundMatcher)
		registerInboundClaim(services.InboundClaimTypeITMCGP,
			services.NewITMCGPClaimSource(config.DB, itService))
		registerInboundClaim(services.InboundClaimTypeITStable,
			services.NewITStableClaimSource(itService))
		inboundPoller.SetMatcher(inboundMatcher)
		if itHandler != nil {
			itHandler.SetPaymentChecker(inboundPoller, workerCtx)
		}
		log.Printf("[inbound] matcher wired (claim sources: %s, %s)",
			services.InboundClaimTypeITMCGP, services.InboundClaimTypeITStable)
	} else if inboundPoller != nil {
		// Ingest keeps running. Credits pile up as `unmatched` and are
		// attributed the moment a matcher exists — nothing about them expires.
		log.Printf("[inbound] matcher dormant: IT service unavailable — credits will be recorded but not attributed")
	}

	// Brokered stablecoin ramp (design doc 2026-08-27-stablecoin-fiat-ramp).
	// A user sends USDC/USDT straight to Flutterwave's own Polygon address —
	// the platform is never in the custody path — and once the deposit is
	// proven on-chain and credited, Flutterwave converts it to fiat and the
	// existing IT payout path settles it. Dormant unless the Polygon deposit
	// address AND our merchant id are both configured — a missing credential
	// must leave the rest of IT bootable, matching how every other optional
	// integration in this service degrades (Nomba, Didit KYC, LiFi).
	//
	// The two legs are gated SEPARATELY. They share the pricing knobs and the
	// Flutterwave converter, but only SELL needs Polygon: the deposit address
	// is where sell users send funds and the RPC client verifies those deposits
	// on-chain. Buy needs neither. Gating buy behind sell's prerequisites is
	// not the safe default it looks like — the buy routes, the rate quoter and
	// the webhook fork all live outside this block, so a deployment with
	// Flutterwave configured but no Polygon would have TAKEN naira, suppressed
	// the MCGP credit, and had no watcher to convert or deliver it.
	stableSellEnabled := false
	stableBuyEnabled := false
	if itService != nil && fwClient != nil && cfg.FlutterwaveMerchantID != "" {
		if err := itService.SetStableSpreadBps(cfg.StableSpreadBps); err != nil {
			log.Printf("[it stable] invalid STABLE_SPREAD_BPS: %v", err)
		}
		itService.SetStableRateLockTTL(cfg.StableRateLockTTL)
		itService.SetStableOverDeliveryToleranceBps(cfg.StableOverDeliveryTolBps)
		// A malformed floor is loud but not fatal: the setter leaves the
		// documented default in force, so the guard still holds. Booting
		// with NO floor is the outcome worth preventing, and cannot happen.
		if err := itService.SetStableMinSell(cfg.StableMinSell); err != nil {
			log.Printf("[it stable] WARNING: IT_STABLE_MIN_SELL invalid (%v) — falling back to the %s default",
				err, services.DefaultStableMinSellMajor)
		}
		itService.SetStableConverter(fwClient, cfg.FlutterwaveMerchantID)

		// BUY: naira in, stablecoin out to the user's own address. Needs the
		// converter and merchant id above, the rate quoter wired earlier, and
		// the outbound deliverer. HasStableBuy is the same predicate the quote,
		// intent creation and the webhook fork consult, so what boots and what
		// the request path believes cannot drift apart.
		itService.SetStableDeliverer(fwClient)
		if itService.HasStableBuy() {
			scheduler.Register(jobs.NewStableBuyWatcher(itService, 30*time.Second), jobs.AlertOnFailureStreak())
			stableBuyEnabled = true
			log.Printf("[it stable] brokered stablecoin BUY ready (spread=%dbps min=%s)",
				cfg.StableSpreadBps, cfg.StableMinSell)
		} else {
			log.Printf("[it stable] BUY dormant — no live FX rate source wired")
		}

		// SELL: stablecoin in on Polygon, naira out. Additionally needs
		// Flutterwave's own deposit address and a Polygon RPC client to prove
		// the user's deposit on-chain.
		polygonClient := blockchainService.ClientForChain("polygon")
		switch {
		case cfg.FWStableDepositAddressPolygon == "":
			log.Printf("[it stable] SELL dormant — set FW_STABLE_DEPOSIT_ADDRESS_POLYGON to enable")
		case polygonClient == nil:
			log.Printf("[it stable] SELL dormant — no Polygon RPC client configured")
		default:
			itService.SetStableDepositAddress("POLYGON", cfg.FWStableDepositAddressPolygon)
			itService.SetStableVerifier(
				// cfg.LiFiBaseURL is the same resolved base the AllSwap
				// handler uses (handlers/swap_lifi_handler.go) — defaults to
				// LiFi's public API, overridable via LIFI_BASE_URL for a
				// local rehearsal against a stand-in server.
				services.NewLiFiStatusClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey),
				services.NewLogTransferVerifier(polygonClient.Client),
				map[string]string{
					models.ITAssetUSDC: cfg.Networks["mainnet"].TokenAddresses["polygon:USDC"],
					models.ITAssetUSDT: cfg.Networks["mainnet"].TokenAddresses["polygon:USDT"],
				},
			)
			scheduler.Register(jobs.NewStableSellWatcher(itService, 30*time.Second), jobs.AlertOnFailureStreak())
			stableSellEnabled = true
			log.Printf("[it stable] brokered stablecoin SELL ready (spread=%dbps min=%s)",
				cfg.StableSpreadBps, cfg.StableMinSell)
		}
	} else {
		log.Printf("[it stable] dormant — set FLUTTERWAVE_MERCHANT_ID (and Flutterwave credentials) to enable")
	}

	// Resolve the company customer that parents all FW virtual accounts. Uses
	// FLUTTERWAVE_CUSTOMER_ID when set, else mints one lazily and persists it
	// (per environment) so operators needn't find it in the dashboard.
	fwCustomerID, fwCustErr := services.ResolveFlutterwaveCompanyCustomer(
		context.Background(), config.DB, fwClient, cfg.FlutterwaveBaseURL, cfg.FlutterwaveCustomerID,
		services.CompanyCustomerParams{
			Name:  cfg.FlutterwaveCompanyName,
			Email: cfg.FlutterwaveCompanyEmail,
			Phone: cfg.FlutterwaveCompanyPhone,
		},
	)
	if fwCustErr != nil {
		log.Printf("[fw] company customer unresolved — VA provisioning stays dormant: %v", fwCustErr)
		fwCustomerID = ""
	} else if fwCustomerID != "" {
		log.Printf("[fw] company customer resolved (VA parent)")
	}

	// Deposit/withdrawal fee-quote engine (PM fee spec, batch 11). Provider fees
	// pass through live Flutterwave GetFees when the client is up, else the
	// config fallback percentage; the P2P FX feed converts the flat USD platform
	// fee for NGN quotes. The same engine now DEDUCTS on the Instant Transfer
	// legs (see the IT wiring below) — the quote the user is shown is the one
	// the money follows.
	feeQuoteQuoter := services.NewFlutterwaveFeeQuoter(fwClient)
	if feeQuoteQuoter == nil {
		log.Printf("[fee-quote] provider fees on config fallback (%d bps): Flutterwave dormant", cfg.FeeQuote.ProviderFallbackBps)
	} else {
		log.Printf("[fee-quote] provider fees live via Flutterwave GetFees")
	}
	feeQuoteService := services.NewFeeQuoteService(cfg.FeeQuote, feeQuoteQuoter)
	// Brokered stablecoin flows (the ramp wired above) already charge their
	// entire margin as the 1.55% spread baked into QuoteStableSell — the
	// swap/routing bps legs must NOT also apply, or the user is double-billed.
	//
	// Gated on the ramp actually being live. Where it is dormant no trade is
	// ever priced by the spread, so dropping the bps legs would hand out an
	// unearned discount on a deployment this feature is switched off for.
	// EITHER leg being live is enough: each prices its own trades through the
	// spread, so a buy-only deployment would double-bill without this.
	feeQuoteService.SetBrokeredStableFlows(stableSellEnabled || stableBuyEnabled)
	feeQuoteHandler := handlers.NewFeeQuoteHandler(
		feeQuoteService,
		services.NewP2PFxProvider(p2pService),
	)

	// Instant Transfer settles NET of fees: the same quote the app shows is the
	// one the payout and the credit are computed from. Without a quoter IT
	// refuses to price a transfer rather than settling one for free, so this
	// wiring is required for IT to operate at all.
	if itService != nil {
		itService.SetFeeQuoter(feeQuoteService)
		itService.SetFeeDistributor(feeDistributionService)
	}

	// Bank directory + name enquiry follow the resolved fiat provider so the
	// app's bank dropdown and the payout gateway can never disagree on codes.
	var bankDirectory services.BankDirectory
	if cfg.ResolveFiatProvider() == "flutterwave" {
		bankDirectory = services.NewFlutterwaveBankDirectory(fwClient, "NG")
	}
	if bankDirectory == nil {
		bankDirectory = services.NewNombaBankDirectory(nombaClient)
	}
	if bankDirectory == nil {
		log.Printf("Bank directory dormant: no fiat provider credentials — bank list + account resolve endpoints will 503")
	} else {
		log.Printf("Bank directory active (provider=%s)", cfg.ResolveFiatProvider())
	}

	// Corporate PM onboarding — resume-able VA provisioning via the resolved
	// fiat provider (Flutterwave static VAs when selected, else Nomba).
	// DORMANT-BY-DEFAULT: built only when the chosen provider is configured.
	// When nil, ApproveMerchantRequest on a p2p_merchant request returns a clear 503.
	if prov := pmProvisioner(cfg, fwClient, fwCustomerID, nombaClient); prov != nil {
		mrh = handlers.NewMerchantRequestHandlerWithPM(config.DB, eventBus, services.NewPMOnboardingService(config.DB, prov))
		log.Printf("[pm] onboarding service ready (provider=%s)", cfg.ResolveFiatProvider())
	} else {
		log.Printf("[pm] onboarding service dormant: no fiat provider configured — corporate PM approval disabled")
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
		// adminResolve→buyer) accrues the realized on-chain fee through a single
		// seam. Gated on the v2 kill-switch (dormant by default).
		if cfg.FeeModelV2Enabled {
			p2pTradeSvc.SetFeeService(feeDistributionService)
		}
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
		scheduler.Register(jobs.NewChainReconciler(p2pTradeSvc), jobs.AlertOnFailureStreak())
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
	p2pHandler := handlers.NewP2PHandler(config.DB, p2pTradeSvc, p2pMessageSvc, bankDirectory, merchantStats)
	// Deliberately NOT wired here, even though ITService — the only holder of
	// the company account — already exists. The P2P claim source is registered
	// much further down, inside the PM-automation block, and until it is there
	// is nothing to attribute a P2P payment to. Routing is decided after that
	// block so the two cannot be enabled independently; see p2pCentralRouting.
	p2pClaimsRegistered := false

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
	// Wire the active-PM lookup into the merchant-request handler so the PM-upgrade
	// submit guard runs in PRODUCTION (active PM → 400 "You are already a payment
	// merchant"), not just in tests. Without this, an already-active PM submitting a
	// p2p_merchant body would instead hit the duplicate-request check with a
	// different message. pmSvc is constructed unconditionally, so this preserves
	// dormant-by-default: GetActiveByUserID returns ErrPMNotFound when no PM exists
	// and the submit proceeds normally.
	mrh.PMService = pmSvc
	log.Printf("[pm] merchant-request handler wired with active-PM guard")
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
		if !rawSignerKeyAllowed(cfg) && cfg.PMReleasePrivateKey != "" {
			// Same rule as the IT operator, relayer and ramp sponsor keys: a raw
			// key out of the environment is a dev/staging convenience, refused in
			// production unless ALLOW_RAW_SIGNER_KEYS opts back in. PM release
			// stays disabled — automation/webhook/admin PM routes surface "not
			// configured" rather than signing with a key that shouldn't be here.
			log.Printf("[pm] PM release disabled: raw PM_RELEASE_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)")
		} else if cfg.PMReleasePrivateKey != "" && sonicClient != nil {
			keyHex := strings.TrimPrefix(cfg.PMReleasePrivateKey, "0x")
			key, kerr := crypto.HexToECDSA(keyHex)
			if kerr != nil {
				log.Printf("[pm] invalid PM_RELEASE_PRIVATE_KEY; PM release disabled: %v", kerr)
			} else {
				alertRawSignerKeyInProduction(cfg, "PM_RELEASE_PRIVATE_KEY")
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
		fwh    *handlers.FlutterwaveWebhookHandler
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
		// Flutterwave is the sole payout gateway (ResolveFiatProvider always
		// returns "flutterwave" now that Nomba is retired). When FW creds are
		// present it goes live; otherwise the payout gateway falls back to the
		// nombaGateway sentinel — which is the disabled no-op unless someone
		// deliberately sets NOMBA_* creds (a retired path removed with the Nomba
		// implementation). In prod FW creds are always set, so this is FW.
		gateway := nombaGateway
		if fwClient != nil {
			gateway = services.NewFlutterwaveGateway(fwClient, cfg.NombaSenderName)
			log.Printf("[pm] live Flutterwave gateway enabled (base=%s)", cfg.FlutterwaveBaseURL)
		} else {
			log.Printf("[pm] Flutterwave gateway dormant: FLUTTERWAVE_CLIENT_ID/SECRET missing — sell payouts error until configured")
		}
		pmAuto = services.NewPMAutomationService(config.DB, pmSvc, p2pTradeSvc, releaseSigner, pmNameLookup, gateway, cfg.NombaSenderName)
		// Verify every Flutterwave charge.completed against the /charges API before
		// crediting — a signed webhook alone is not trusted. Without this the
		// dispatcher fails closed and refuses to settle charge events.
		if fwClient.IsEnabled() {
			pmAuto.SetFlutterwaveChargeVerifier(fwClient)
			log.Printf("[fw] charge verification ACTIVE (webhook credits are API-confirmed)")
		}
		// IT automation — webhook routing always (references arrive regardless),
		// payout gateway only when a LIVE gateway exists (the disabled gateway
		// would flip HasAutoPayout and force bank capture with no way to pay).
		if itService != nil {
			pmAuto.SetITHooks(itService)
			_, fwLive := gateway.(*services.FlutterwaveGateway)
			if fwLive || pmNombaClient != nil {
				itService.SetPayoutGateway(gateway)
				if fwLive {
					// Transfer read-back: the evidence the sell reconciler and the
					// admin settle/reverse guards act on.
					itService.SetPayoutTransfers(fwClient)
					itService.SetFWAbsenceTrusted(cfg.ITSellFWAbsenceTrusted)
				}
				log.Printf("[it] sell auto-payout ACTIVE (gateway live)")
			} else {
				log.Printf("[it] sell auto-payout dormant: no live payout gateway")
			}
		}
		pmh = handlers.NewPMHandler(pmAuto, pmSvc)
		// Wire admin PM approval (pending → approved) and merchant self-activation
		// (approved → active, mints the Flutterwave static VA from the merchant's
		// own BVN/NIN). Both use the same onboarding service; when FW isn't
		// configured the provisioner is nil and approve/activate 503 while
		// reject/pause/resume still work — it never routes provisioning to Nomba.
		if prov := pmProvisioner(cfg, fwClient, fwCustomerID, pmNombaClient); prov != nil {
			pmOnboarding := services.NewPMOnboardingService(config.DB, prov)
			pmh.SetApprover(pmOnboarding)
			pmh.SetActivator(pmOnboarding)
			log.Printf("[pm] admin PM approval + merchant activation ready (provider=%s)", cfg.ResolveFiatProvider())
		} else {
			log.Printf("[pm] admin PM approval/activation dormant: no fiat provider configured — approve/activate will 503 (reject/pause/resume still available)")
		}
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
		if cfg.FlutterwaveWebhookSecret != "" {
			fwh = handlers.NewFlutterwaveWebhookHandler(
				services.NewFlutterwaveWebhookVerifier(cfg.FlutterwaveWebhookSecret),
				services.NewWebhookStoreForProvider(config.DB, services.ProviderFlutterwave),
				pmAuto,
			)
			log.Printf("[pm] Flutterwave webhook handler ready")
		} else {
			log.Printf("[pm] Flutterwave webhook dormant: FLUTTERWAVE_WEBHOOK_SECRET not set")
		}
		// M2 liveness: periodically recover PM trades wedged at `paid` with no
		// release (auto-release failed after markPaid, broadcast never landed) and
		// re-dispatch failed/stale Nomba webhook events. Complements the chain
		// reconciler + timeout watchdog, which don't cover paid-orphans or dropped
		// dispatches.
		scheduler.Register(jobs.NewPMRecoveryJob(pmAuto), jobs.AlertOnFailureStreak())
		log.Printf("[pm] PM recovery job registered (paid-orphan re-drive + webhook re-dispatch)")

		// Instant per-trade settlement forward (central-account design decision
		// 7): on settle, sweep the merchant's naira to their own settlement
		// bank rather than leaving it to a manual withdrawal.
		//
		// Wired in BOTH inbound modes, and deliberately so. The forward is a
		// consequence of a trade settling, not of how the money was attributed,
		// and the disburse webhook it depends on arrives regardless of the
		// flag. Gating it on central mode would mean a forward submitted before
		// a rollback could never be confirmed, which would leave the merchant
		// permanently over-credited.
		//
		// fwClient doubles as the payout-balance reader: the pre-check refuses
		// to submit a transfer the payout account cannot fund, which the
		// stablecoin sell leg does and the MCGP sell leg's absence of is the
		// root of its stranded-value bug. A nil client simply skips the
		// pre-check — the ledger ordering, not this check, is what makes the
		// forward safe.
		var balanceReader services.PayoutBalanceReader
		if fwClient != nil {
			balanceReader = fwClient
		}
		settlementForwarder := services.NewP2PSettlementForwarder(
			config.DB, pmSvc, gateway, balanceReader, cfg.NombaSenderName)
		pmAuto.SetSettlementForwarder(settlementForwarder)
		scheduler.Register(jobs.NewP2PForwardSweepJob(settlementForwarder), jobs.AlertOnFailureStreak())
		log.Printf("[pm] settlement forward wired (balance pre-check=%t); unanswered-transfer sweep registered",
			balanceReader != nil)

		// P2P joins the matcher here rather than alongside the IT sources,
		// because only now do the trade service and release signer exist. The
		// claim's user is the BUYER — they send the naira — while the merchant
		// is credited and forwarded to; see inbound_claim_p2p.go.
		p2pClaims := services.NewP2PTradeClaimSource(config.DB, pmSvc, p2pTradeSvc, releaseSigner)
		p2pClaims.SetForwarder(settlementForwarder)
		registerInboundClaim(services.InboundClaimTypeP2PTrade, p2pClaims)
		// Read below to decide whether P2P buyers may be sent to the company
		// account at all. Set from the registration itself, never from the
		// conditions that led here, so the two can't be reasoned about apart.
		p2pClaimsRegistered = true
		if inboundMatcher != nil {
			log.Printf("[inbound] matcher gained the %s claim source", services.InboundClaimTypeP2PTrade)
		} else {
			// No matcher, so nothing auto-binds — but the hold queue still gets
			// the source, so an operator can see and assign P2P candidates.
			log.Printf("[inbound] no matcher wired; %s registered for the hold queue only",
				services.InboundClaimTypeP2PTrade)
		}
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

	// Both halves of P2P central attribution are now known, so decide here —
	// after the claim source either registered or didn't — rather than beside
	// the handler, where only half the answer exists.
	if route, warning := p2pCentralRouting(itService != nil, p2pClaimsRegistered, cfg.InboundCentralEnabled()); route {
		p2pHandler.SetInboundCentralSource(itService)
		log.Printf("[inbound] P2P buyers routed to the company account (%s claim source registered)",
			services.InboundClaimTypeP2PTrade)
	} else if warning != "" {
		log.Printf("[inbound] WARNING: %s", warning)
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

	// Non-EVM confirm/reconcile worker: walks pending wallet_activity rows and
	// finalizes them against on-chain truth. Guarded on the registry having at
	// least one chain configured (e.g. Tron); dormant otherwise.
	if len(chainRegistry.Keys()) > 0 {
		scheduler.Register(jobs.NewNonEVMReconciler(config.DB, chainRegistry))
		log.Printf("chains: non-EVM reconciler registered (%d chain(s))", len(chainRegistry.Keys()))
	}

	// Backfill any pending transactions that already have a txHash.
	// These were submitted before the receipt-polling goroutine was added.
	go handlers.BackfillPendingTransactions(blockchainService)

	// Set Gin mode based on environment
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create router with default middleware (logger and recovery)
	router := gin.Default()

	// nginx is the only proxy in front of this service and it always overwrites
	// X-Forwarded-For (see deploy/nginx-tsa-api-site.conf). Without this, Gin trusts
	// every peer and a client can choose the IP attributed to it — which drives the
	// per-IP rate limiter and the auth/IT-agreement audit logs.
	if err := router.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		log.Fatalf("failed to set trusted proxies: %v", err)
	}

	// Apply global middleware
	router.Use(middleware.CORS(cfg))
	router.Use(middleware.SecurityHeaders())
	router.Use(middleware.RequestLogger())

	// Override health check with DB status
	router.GET("/health", healthHandler(sqlDB.Ping, buildCommit))

	// ─── Paycrest ramp ─────────────────────────────────────────────────────
	// Dormant unless PAYCREST_API_KEY is set: no key, nil client, routes never
	// registered. Quoting works without a key (the rate endpoints are public),
	// but there is no point offering a quote we cannot turn into an order.
	paycrestClient := services.NewPaycrestClient(
		os.Getenv("PAYCREST_BASE_URL"), os.Getenv("PAYCREST_API_KEY"))
	var paycrestHandler *handlers.PaycrestHandler
	var paycrestWebhookHandler *handlers.PaycrestWebhookHandler
	// Hoisted out of the `if paycrestClient.IsEnabled()` block below (where
	// buildRampConversion actually runs) so the dispute service and handler,
	// wired further down, can pick it up whether or not it ended up nil.
	var conversionOrch *services.ConversionOrchestrator
	if paycrestClient.IsEnabled() {
		paycrestDir := services.NewPaycrestDirectory(paycrestClient, bankDirectory)
		paycrestQuotes := services.NewPaycrestQuoteService(paycrestClient, nil, nil)
		// Also sets the fee on paycrestQuotes, so quotes price in what orders send.
		paycrestOrders := services.NewPaycrestOrderService(
			config.DB, paycrestClient, paycrestQuotes, paycrestDir,
			os.Getenv("PAYCREST_SENDER_FEE_PERCENT"), os.Getenv("PAYCREST_SENDER_FEE_ADDRESS"))
		if paycrestOrders != nil {
			paycrestOrders.SetRoundTripBudgetBps(cfg.RampRoundTripBudgetBps)
			if !paycrestOrders.ConversionPriced() {
				log.Printf("[paycrest] conversion unpriced: RAMP_ROUND_TRIP_BUDGET_BPS=%d must be > 0 and below PAYCREST_SENDER_FEE_PERCENT (in bps); converted buys and swapped sells stay off",
					cfg.RampRoundTripBudgetBps)
			}
			// Server-side kill switch: an operator can now stop the rail for a
			// currency by flipping its ramp_provider column, not just change
			// what the app is told. See PaycrestOrderService.SetCurrencyRegistry.
			paycrestOrders.SetCurrencyRegistry(currencyRegistry)
		}
		// Direction lives on the order book, not in our config: /api/it/currencies
		// asks the quote service which way each currency can actually trade, so
		// the app never offers a leg no provider will fill. itHandler is nil
		// when IT itself is unconfigured, and the endpoint falls back to
		// "both directions" whenever this is unwired.
		if itHandler != nil {
			itHandler.SetRampLegs(paycrestQuotes)
		}
		paycrestHandler = handlers.NewPaycrestHandler(paycrestOrders, paycrestQuotes, paycrestDir)
		if rampSponsor := buildRampSponsorship(cfg, blockchainService, paycrestOrders); rampSponsor != nil {
			paycrestHandler = paycrestHandler.WithSponsorship(rampSponsor)
			scheduler.Register(jobs.NewGasFloatMonitor(rampSponsor, 10*time.Minute))
		}
		// Converted buys: Paycrest delivers into our own treasury instead of
		// the user's wallet, then the treasury bridges it via LiFi. Dormant
		// unless RAMP_CONVERSION_BUY is set — see buildRampConversion. The
		// nil-orchestrator gate itself lives in registerRampConversionWorkers
		// (and is unit-tested there), not inlined here.
		var treasury *services.Treasury
		treasury, conversionOrch = buildRampConversion(cfg, blockchainService, paycrestOrders, paycrestQuotes)
		registerRampConversionWorkers(scheduler, treasury, conversionOrch, cfg.RampTreasuryKeptUSDCAlert)
		// Webhooks need their own secret. Without it the verifier is nil and the
		// route answers 503 — fail closed rather than accept unsigned events.
		if v := services.NewPaycrestWebhookVerifier(os.Getenv("PAYCREST_API_SECRET")); v != nil && paycrestOrders != nil {
			paycrestWebhookHandler = handlers.NewPaycrestWebhookHandler(
				v, services.NewWebhookStoreForProvider(config.DB, services.ProviderPaycrest), paycrestOrders)
			log.Printf("[paycrest] webhook endpoint ready")
		} else {
			log.Printf("[paycrest] webhook DORMANT — set PAYCREST_API_SECRET; orders advance by polling only")
		}
		if paycrestOrders != nil {
			// No public webhook endpoint yet, so orders only advance because
			// something polls them.
			scheduler.Register(jobs.NewPaycrestOrderPoller(paycrestOrders, 20*time.Second), jobs.AlertOnFailureStreak())
			log.Printf("[paycrest] ramp ready (poller 20s)")
		} else {
			log.Printf("[paycrest] client configured but order service could not be built — routes dormant")
		}
	} else {
		log.Printf("[paycrest] dormant — set PAYCREST_API_KEY to enable")
	}

	// One dispute queue across every order type that cannot resolve itself.
	// Subjects are registered per stack rather than unconditionally: a dormant
	// integration should have no disputable orders, not a route that fails when
	// someone tries to use it.
	disputeService := services.NewDisputeService(config.DB, eventBus)
	disputeService.RegisterSubject(models.DisputeSubjectPaycrestOrder, services.PaycrestDisputeSubject(config.DB))
	disputeService.RegisterSubject(models.DisputeSubjectITIntent, services.ITDisputeSubject(config.DB))
	if billService != nil {
		disputeService.RegisterSubject(models.DisputeSubjectBillOrder, services.BillDisputeSubject(config.DB))
		// The bills stack's own report endpoints now read and write this same
		// table, so the shipped app keeps working while the console moves to
		// the unified queue.
		billService.SetDisputes(disputeService)
	}
	if conversionOrch != nil {
		// A converted-buy dispute's detail carries its own leg ledger and
		// what it owes, straight from the ledger a refund would use.
		disputeService.SetRampLegsReader(conversionOrch)
	}
	disputeHandler := handlers.NewDisputeHandler(disputeService).WithRampAdmin(conversionOrch)

	inboundAdminHandler := handlers.NewInboundAdminHandler(inboundAdminService)

	// Setup all routes
	routes.SetupRoutes(router, cfg, h, ch, mrh, sch, swh, slh, psh, wsHub, eh, p2pHandler, fch, rfh, rdh, rsh, fah, rph, pmh, nwh, mcgpSwapHandler, itHandler, osh, rch, tph, fwh, feeQuoteHandler, fwlh, billHandler, paycrestHandler, paycrestWebhookHandler, disputeHandler, inboundAdminHandler)

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
	scheduler.Start(workerCtx)

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
	workerCancel()
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

// inboundGapAlerts adapts the inbound alert channel onto the reconciler's
// jobs.InboundGapNotifier, for the same reason pmNGNLedgerSource lives here:
// jobs imports services, so the notifier cannot name jobs.InboundGap without
// reversing that. The field copy is the price of the one-way dependency and is
// cheaper than the alternative, which is moving mail composition into jobs
// where the matcher — the notifier's primary caller — cannot reach it.
type inboundGapAlerts struct {
	to *services.InboundHoldNotifier
}

func (a inboundGapAlerts) NotifyInboundGap(ctx context.Context, gap jobs.InboundGap) error {
	return a.to.NotifyGap(ctx, services.InboundGapAlert{
		Currency:     gap.Currency,
		From:         gap.From,
		To:           gap.To,
		IngestedKobo: gap.IngestedKobo,
		ProviderKobo: gap.ProviderKobo,
		GapKobo:      gap.GapKobo,
		IngestedRows: gap.IngestedRows,
		ProviderRows: gap.ProviderRows,
		Streak:       gap.Streak,
	})
}

// workerFailureAlerts adapts the inbound alert channel onto the scheduler's
// jobs.WorkerFailureNotifier; the field copy exists for the same one-way
// dependency reason as inboundGapAlerts.
type workerFailureAlerts struct {
	to *services.InboundHoldNotifier
}

func (a workerFailureAlerts) NotifyWorkerFailing(ctx context.Context, f jobs.WorkerFailure) error {
	return a.to.NotifyWorkerFailing(ctx, workerFailureAlert(f))
}

func (a workerFailureAlerts) NotifyWorkerRecovered(ctx context.Context, f jobs.WorkerFailure) error {
	return a.to.NotifyWorkerRecovered(ctx, workerFailureAlert(f))
}

func workerFailureAlert(f jobs.WorkerFailure) services.WorkerFailureAlert {
	return services.WorkerFailureAlert{
		Worker:              f.Worker,
		ConsecutiveFailures: f.ConsecutiveFailures,
		FailingSince:        f.FailingSince,
		FailingFor:          f.FailingFor,
		LastError:           f.LastError,
	}
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

// bigFromDecimal parses a decimal config string; empty or invalid yields nil,
// which the relayer treats as "limit not configured".
func bigFromDecimal(s string) *big.Int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil
	}
	return v
}
