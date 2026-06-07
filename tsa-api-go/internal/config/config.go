package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// chainPreset bundles every value that has to flip together for one chain's
// P2P deployment (Sonic or BSC). Flipping piecemeal makes the EIP-712 voucher
// domain mismatch what the contract recovers, so every signature gets rejected.
type chainPreset struct {
	rpcURL          string
	chainID         int64
	usdcAddress     string
	usdtAddress     string
	mcgpAddress     string
	p2pEscrow       string
	// eip712Version is the EIP-712 domain `version` string the deployed
	// P2PEscrow at p2pEscrow was initialized with. It MUST move in lockstep
	// with p2pEscrow: v1 deployed "1", the two-sided v2 deployed "2". The
	// backend stamps this into the voucher typed-data the app signs; a value
	// that disagrees with the on-chain contract makes ecrecover resolve to a
	// wrong address and rejects every sell-ad signature.
	eip712Version   string
}

// sonicPresets holds the canonical Sonic P2P deployment for each network.
// To flip the server between testnet and mainnet, set SONIC_NETWORK and
// restart — no other env vars need to change. Individual addresses can
// still be overridden via the legacy SONIC_RPC_URL / *_TOKEN_ADDRESS /
// P2P_ESCROW_ADDRESS env vars (escape hatch for hot-fixes).
var sonicPresets = map[string]chainPreset{
	"mainnet": {
		rpcURL:      "https://rpc.soniclabs.com",
		chainID:     146,
		usdcAddress: "0x29219dd400f2Bf60E5a23d13Be72B486D4038894",
		usdtAddress: "0x6047828dc181963ba44974801ff68e538da5eaf9",
		mcgpAddress: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
		// Still the v1 (sell-only) escrow — mainnet v2 awaits the external
		// re-audit. Domain version stays "1" to match it.
		p2pEscrow:     "0x1Dc47600C713B216686dd20c349fDCd2575D3cE1",
		eip712Version: "1",
	},
	"testnet": {
		rpcURL:      "https://rpc.testnet.soniclabs.com",
		chainID:     14601,
		usdcAddress: "0x685CD180e603A15507f575E7A347610b83d36d41",
		usdtAddress: "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
		mcgpAddress: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
		// P2PEscrowV2 (two-sided) — deployed 2026-05-29, UUPS proxy,
		// initialized with EIP-712 domain version "2".
		p2pEscrow:     "0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7",
		eip712Version: "2",
	},
}

// bscPresets mirrors sonicPresets for BSC. p2pEscrow is EMPTY until P2PEscrowV2
// is deployed to BSC (see Plan 6) — an empty escrow keeps BSC P2P dormant (Load
// omits it from P2PChains). mcgpAddress is intentionally empty: MCGP is Sonic-
// only, so the fee-discount path is disabled on BSC (P2PEscrowV2 charges the
// standard fee when mcgpToken is address(0)). Testnet token addresses are filled
// in at deploy time via P2P_ESCROW_ADDRESS_BSC + the *_TOKEN_ADDRESS overrides.
var bscPresets = map[string]chainPreset{
	"mainnet": {
		rpcURL:        "https://bsc-dataseed.binance.org",
		chainID:       56,
		usdcAddress:   "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d",
		usdtAddress:   "0x55d398326f99059fF775485246999027B3197955",
		mcgpAddress:   "",
		p2pEscrow:     "", // set after the audit-gated BSC mainnet deploy
		eip712Version: "2",
	},
	"testnet": {
		rpcURL:        "https://data-seed-prebsc-1-s1.binance.org:8545",
		chainID:       97,
		usdcAddress:   "", // set at BSC-testnet deploy (test token addresses)
		usdtAddress:   "",
		mcgpAddress:   "",
		p2pEscrow:     "", // set after the BSC testnet deploy
		eip712Version: "2",
	},
}

// P2PChainConfig is the per-chain P2P deployment the trade/escrow services
// resolve by chainID. ChainID + EscrowAddress + EIP712Version move in lockstep
// (a mismatch rejects every voucher). Assets is the curated allow-list of
// tradeable asset symbols on this chain — no asset is split across chains.
type P2PChainConfig struct {
	ChainID       int64
	ChainKey      string // blockchainService key: "sonic", "bsc"
	EscrowAddress string
	EIP712Version string
	Assets        []string // e.g. ["MCGP"] on Sonic, ["USDT","USDC"] on BSC
}

// ChainConfig holds the configuration for a single EVM-compatible blockchain.
type ChainConfig struct {
	Name           string
	RPCURL         string
	ChainID        int64
	NativeCurrency string // "S", "BNB", "tBNB", etc.
}

// NetworkConfig holds chains and token addresses for a single network environment.
type NetworkConfig struct {
	Chains         map[string]ChainConfig
	TokenAddresses map[string]string // "chain:symbol" → contract address
}

type Config struct {
	DatabaseURL         string
	JWTSecret           string
	FrontendURL         string
	AdminURL            string
	LandingURL          string
	Port                string
	Env                 string
	CloudinaryCloudName string
	CloudinaryAPIKey    string
	CloudinaryAPISecret string

	// Sonic blockchain configuration
	SonicRPCURL         string
	SonicChainID        int64
	MCGPTokenAddress    string
	USDTTokenAddress    string
	USDCTokenAddress    string
	SystemWalletAddress string

	// BSC blockchain
	BSCRPCURL  string
	BSCChainID int64

	// Smart contract addresses
	ProductEscrowAddress   string
	ServiceContractAddress string
	OTCMarketplaceAddress  string
	P2PEscrowAddress       string
	// P2PEscrowEIP712Version is the voucher domain `version` matching the
	// deployed P2PEscrow (see chainPreset.eip712Version). Stamped into the
	// typed-data the app signs.
	P2PEscrowEIP712Version string

	// P2PChains is the per-chain P2P deployment registry keyed by chainID,
	// assembled in Load(). Sonic is always present; BSC is present only when
	// its escrow address is configured (dormant-by-default). Later refactors
	// resolve escrow/domain/version by trade.ChainID from this map.
	P2PChains map[int64]P2PChainConfig

	// SonicNetwork records which preset was active for this boot
	// ("mainnet" or "testnet"). Downstream code that needs to query the
	// right Networks[…] map for sonic-side lookups reads this rather than
	// re-parsing the SONIC_NETWORK env var.
	SonicNetwork string

	// Mailjet email service
	MailjetAPIKey    string
	MailjetSecretKey string

	// Expo push notifications. Optional — sends work unauthenticated; set to
	// enable Expo's enhanced push security. Dormant if unset.
	ExpoAccessToken string

	// Persona KYC verification
	PersonaAPIKey        string
	PersonaTemplateID    string
	PersonaBaseURL       string
	PersonaWebhookSecret string

	// LiFi swap aggregator
	LiFiAPIKey         string
	LiFiIntegratorID   string
	LiFiIntegratorFee  string

	// Open Exchange Rates configuration
	OpenExchangeRatesAppID string
	SupportedCurrencies     []string

	// Currencies routed through Bybit P2P (others use OER)
	BybitP2PCurrencies []string

	// Per-currency retail probe amount sent as the `amount` filter on Bybit
	// queries. Picking a retail-tier amount (~$3–4 equivalent) gets us a
	// tight, consensus-driven price; the unfiltered API returns by merchant
	// rank, surfacing large-ticket whales whose prices drift from the
	// retail consensus.
	BybitProbeAmounts map[string]string

	// P2P float-rate offset cap in basis points (e.g. 1000 = ±10%). Sellers
	// publishing float-rate ads pick an offset within ±this value relative
	// to the reference feed. Tune via env to widen/tighten platform-wide.
	P2PFloatOffsetMaxBps int

	// Nomba — used for Nigerian bank directory + account name enquiry today,
	// and the auto-payout path in Phase 2. Falls back to "feature disabled"
	// when credentials aren't set so dev environments work without them.
	NombaBaseURL      string
	NombaClientID     string
	NombaClientSecret string
	NombaAccountID    string

	// P2P merchant-reputation thresholds for the "Trusted" badge.
	P2PTrustedMinReleased    int64
	P2PTrustedMinCompletion  float64
	P2PTrustedMinAgeDays     int

	// Fee/TP overhaul (SP3) feature flag + tunables.
	//
	// FeeModelV2Enabled gates the new FeeDistributionService engine. Default
	// FALSE: while off, the legacy handlers.DistributeTPEarnings path stays the
	// live path and nothing changes in production. Flip to true (env
	// FEE_MODEL_V2_ENABLED=true) at the deliberate cutover. Read it via
	// Config.IsFeeModelV2Enabled() rather than poking the field directly so the
	// gate has one chokepoint.
	FeeModelV2Enabled bool

	// TPPerUSD is the TP minting rate: how many TP one USD of TP-pool value
	// mints. DEFAULT 1 (1 TP = $1). Tunable via FEE_TP_PER_USD; it sets level
	// PACING (see design §3), not the reward ratio.
	TPPerUSD float64

	// GlobalPoolSettleInterval is the cadence of the SP4 global-pool settlement
	// epoch (SettleGlobalPoolEpoch folds the pool delta into the cumulative
	// index once per interval). DEFAULT 24h (daily). Tunable via
	// FEE_GLOBAL_POOL_SETTLE_INTERVAL (a Go duration string, e.g. "1h", "15m").
	// The settlement job is GATED on FeeModelV2Enabled — dormant until cutover.
	GlobalPoolSettleInterval time.Duration

	// Network configurations: "mainnet" and "testnet"
	Networks map[string]NetworkConfig

	// Legacy flat fields — kept for backward compat, point to mainnet
	Chains         map[string]ChainConfig
	TokenAddresses map[string]string
}

func Load() *Config {
	// Pick the Sonic deployment preset first; individual env vars layered on
	// top can override any single field. Default is "mainnet" so a production
	// server with no SONIC_NETWORK set keeps its current behaviour.
	sonicNetwork := strings.ToLower(strings.TrimSpace(getEnv("SONIC_NETWORK", "mainnet")))
	preset, ok := sonicPresets[sonicNetwork]
	if !ok {
		log.Printf("[config] SONIC_NETWORK=%q is unknown; falling back to 'mainnet'", sonicNetwork)
		sonicNetwork = "mainnet"
		preset = sonicPresets["mainnet"]
	}
	log.Printf("[config] Sonic network = %s (chain %d, P2PEscrow %s)",
		sonicNetwork, preset.chainID, preset.p2pEscrow)

	cfg := &Config{
		SonicNetwork:        sonicNetwork,
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://localhost:5432/tsa?sslmode=disable"),
		JWTSecret:           getEnv("JWT_SECRET", ""),
		FrontendURL:         getEnv("FRONTEND_URL", "http://localhost:3000"),
		AdminURL:            getEnv("ADMIN_URL", "http://localhost:5173,https://admin.tsaconnectworld.com"),
		LandingURL:          getEnv("LANDING_URL", "https://tsaconnectworld.com"),
		Port:                getEnv("PORT", "5000"),
		Env:                 getEnv("ENV", "development"),
		CloudinaryCloudName: getEnv("CLOUDINARY_CLOUD_NAME", ""),
		CloudinaryAPIKey:    getEnv("CLOUDINARY_API_KEY", ""),
		CloudinaryAPISecret: getEnv("CLOUDINARY_API_SECRET", ""),

		// Sonic blockchain — preset by SONIC_NETWORK, overridable per-field.
		SonicRPCURL:         getEnv("SONIC_RPC_URL", preset.rpcURL),
		SonicChainID:        getEnvInt64("SONIC_CHAIN_ID", preset.chainID),
		MCGPTokenAddress:    getEnv("MCGP_TOKEN_ADDRESS", preset.mcgpAddress),
		USDTTokenAddress:    getEnv("USDT_TOKEN_ADDRESS", preset.usdtAddress),
		USDCTokenAddress:    getEnv("USDC_TOKEN_ADDRESS", preset.usdcAddress),
		SystemWalletAddress: getEnv("SYSTEM_WALLET_ADDRESS", "0xaF326D5D242C9A55590540f14658adDDd3586A8d"),

		// BSC blockchain
		BSCRPCURL:  getEnv("BSC_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"),
		BSCChainID: getEnvInt64("BSC_CHAIN_ID", 97),

		// Smart contract addresses
		ProductEscrowAddress:   getEnv("PRODUCT_ESCROW_ADDRESS", "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d"),
		ServiceContractAddress: getEnv("SERVICE_CONTACT_ADDRESS", "0x1304d7e287deB233131A9F75aA5867af1D9591d1"),
	}
	cfg.OTCMarketplaceAddress = getEnv("OTC_MARKETPLACE_ADDRESS", "")
	// P2PEscrow proxy address — preset by SONIC_NETWORK; override via env if
	// you need to point at a hot-fix deployment.
	cfg.P2PEscrowAddress = strings.TrimSpace(getEnv("P2P_ESCROW_ADDRESS", preset.p2pEscrow))
	// Must match the deployed contract's EIP-712 domain version (moves in
	// lockstep with P2P_ESCROW_ADDRESS; override only when overriding the addr).
	cfg.P2PEscrowEIP712Version = getEnv("P2P_ESCROW_EIP712_VERSION", preset.eip712Version)

	// ── Per-chain P2P registry ────────────────────────────────────────────
	// Sonic is always present (its escrow is preset/overridable above). BSC is
	// added only when its escrow address is configured (dormant-by-default).
	cfg.P2PChains = map[int64]P2PChainConfig{
		cfg.SonicChainID: {
			ChainID:       cfg.SonicChainID,
			ChainKey:      "sonic",
			EscrowAddress: cfg.P2PEscrowAddress,
			EIP712Version: cfg.P2PEscrowEIP712Version,
			// Transition: Sonic serves all three today. At BSC launch, narrow this
			// to []string{"MCGP"} (stablecoin P2P moves to BSC) — spec §4.5.
			Assets: []string{"USDT", "USDC", "MCGP"},
		},
	}

	bscNetwork := strings.ToLower(strings.TrimSpace(getEnv("BSC_NETWORK", "testnet")))
	bscPreset, okBSC := bscPresets[bscNetwork]
	if !okBSC {
		log.Printf("[config] BSC_NETWORK=%q is unknown; falling back to 'testnet'", bscNetwork)
		bscNetwork = "testnet"
		bscPreset = bscPresets["testnet"]
	}
	bscEscrow := strings.TrimSpace(getEnv("P2P_ESCROW_ADDRESS_BSC", bscPreset.p2pEscrow))
	if bscEscrow != "" {
		cfg.P2PChains[bscPreset.chainID] = P2PChainConfig{
			ChainID:       bscPreset.chainID,
			ChainKey:      "bsc",
			EscrowAddress: bscEscrow,
			EIP712Version: getEnv("P2P_ESCROW_EIP712_VERSION_BSC", bscPreset.eip712Version),
			Assets:        []string{"USDT", "USDC"},
		}
		log.Printf("[config] P2P chains enabled: sonic(%d) + bsc(%d, network=%s, escrow=%s)",
			cfg.SonicChainID, bscPreset.chainID, bscNetwork, bscEscrow)
	} else {
		log.Printf("[config] P2P chains enabled: sonic(%d) only — BSC dormant (no escrow for BSC_NETWORK=%s)",
			cfg.SonicChainID, bscNetwork)
	}

	// Mailjet email service
	cfg.MailjetAPIKey = os.Getenv("MAILJET_API_KEY")
	cfg.MailjetSecretKey = os.Getenv("MAILJET_SECRET_KEY")

	// Expo push notifications (optional)
	cfg.ExpoAccessToken = os.Getenv("EXPO_ACCESS_TOKEN")

	// Persona KYC
	cfg.PersonaAPIKey = os.Getenv("PERSONA_API_KEY")
	cfg.PersonaTemplateID = os.Getenv("PERSONA_TEMPLATE_ID")
	cfg.PersonaBaseURL = getEnv("PERSONA_BASE_URL", "https://withpersona.com/api/v1")
	cfg.PersonaWebhookSecret = os.Getenv("PERSONA_WEBHOOK_SECRET")

	// LiFi swap aggregator. API key is optional — without it we hit the
	// anonymous tier (rate-limited). Integrator + fee are also optional;
	// when both are set we collect a percentage cut, which requires a
	// matching registration at portal.li.fi with a fee wallet configured.
	cfg.LiFiAPIKey = os.Getenv("LIFI_API_KEY")
	cfg.LiFiIntegratorID = os.Getenv("LIFI_INTEGRATOR_ID")
	cfg.LiFiIntegratorFee = os.Getenv("LIFI_INTEGRATOR_FEE")

	// Open Exchange Rates API — free tier: 1,000 req/month, cached at 1 hour
	cfg.OpenExchangeRatesAppID = os.Getenv("OPEN_EXCHANGE_RATES_APP_ID")
	// "*" returns every ISO 4217 fiat currency in the metadata table; otherwise comma-separated.
	currenciesStr := getEnv("SUPPORTED_CURRENCIES", "*")
	cfg.SupportedCurrencies = splitAndTrim(currenciesStr, ",")

	// Bybit P2P routing — only currencies with deep P2P liquidity reflect real market rates
	bybitCurrenciesStr := getEnv("BYBIT_P2P_CURRENCIES", "NGN")
	cfg.BybitP2PCurrencies = splitAndTrim(bybitCurrenciesStr, ",")

	// Bybit probe amounts — "CUR:AMT,CUR:AMT". NGN→5000 (~$3) by default.
	// Add others as new currencies are routed through Bybit.
	cfg.BybitProbeAmounts = parseProbeAmounts(getEnv("BYBIT_PROBE_AMOUNTS", "NGN:5000"))

	// P2P float-rate offset cap (basis points). 1000 = ±10%, matches Binance
	// P2P's typical platform-wide deviation cap.
	cfg.P2PFloatOffsetMaxBps = int(getEnvInt64("P2P_FLOAT_OFFSET_MAX_BPS", 1000))

	// Nomba — sandbox by default. Production should set NOMBA_BASE_URL to
	// https://api.nomba.com and provide live credentials.
	cfg.NombaBaseURL = getEnv("NOMBA_BASE_URL", "https://sandbox.nomba.com")
	cfg.NombaClientID = os.Getenv("NOMBA_CLIENT_ID")
	cfg.NombaClientSecret = os.Getenv("NOMBA_CLIENT_SECRET")
	cfg.NombaAccountID = os.Getenv("NOMBA_ACCOUNT_ID")

	// P2P merchant-reputation thresholds.
	cfg.P2PTrustedMinReleased = getEnvInt64("P2P_TRUSTED_MIN_RELEASED", 50)
	cfg.P2PTrustedMinCompletion = getEnvFloat64("P2P_TRUSTED_MIN_COMPLETION", 0.95)
	cfg.P2PTrustedMinAgeDays = int(getEnvInt64("P2P_TRUSTED_MIN_AGE_DAYS", 30))

	// Fee/TP overhaul (SP3). Engine OFF by default so production keeps the
	// legacy TP path until the deliberate cutover. TP minting rate defaults to
	// 1 TP = $1 of pool value; tunable without code change.
	cfg.FeeModelV2Enabled = getEnvBool("FEE_MODEL_V2_ENABLED", false)
	cfg.TPPerUSD = getEnvFloat64("FEE_TP_PER_USD", 1.0)
	// SP4 global-pool settlement cadence — default daily.
	cfg.GlobalPoolSettleInterval = getEnvDuration("FEE_GLOBAL_POOL_SETTLE_INTERVAL", 24*time.Hour)

	// ── Mainnet configuration ──
	// Token addresses sourced from each chain's official documentation. Public
	// RPC URLs are used as defaults so the service boots without paid keys;
	// production deployments should override with Alchemy/Infura/QuickNode in
	// env to avoid public-RPC rate limits.
	mainnetChains := map[string]ChainConfig{
		"sonic":     {Name: "Sonic Network", RPCURL: getEnv("SONIC_MAINNET_RPC_URL", "https://rpc.soniclabs.com"), ChainID: getEnvInt64("SONIC_MAINNET_CHAIN_ID", 146), NativeCurrency: "S"},
		"bsc":       {Name: "BNB Smart Chain", RPCURL: getEnv("BSC_MAINNET_RPC_URL", "https://bsc-dataseed.binance.org"), ChainID: getEnvInt64("BSC_MAINNET_CHAIN_ID", 56), NativeCurrency: "BNB"},
		"ethereum":  {Name: "Ethereum", RPCURL: getEnv("ETHEREUM_MAINNET_RPC_URL", "https://eth.llamarpc.com"), ChainID: getEnvInt64("ETHEREUM_MAINNET_CHAIN_ID", 1), NativeCurrency: "ETH"},
		"polygon":   {Name: "Polygon", RPCURL: getEnv("POLYGON_MAINNET_RPC_URL", "https://polygon-rpc.com"), ChainID: getEnvInt64("POLYGON_MAINNET_CHAIN_ID", 137), NativeCurrency: "POL"},
		"arbitrum":  {Name: "Arbitrum One", RPCURL: getEnv("ARBITRUM_MAINNET_RPC_URL", "https://arb1.arbitrum.io/rpc"), ChainID: getEnvInt64("ARBITRUM_MAINNET_CHAIN_ID", 42161), NativeCurrency: "ETH"},
		"base":      {Name: "Base", RPCURL: getEnv("BASE_MAINNET_RPC_URL", "https://mainnet.base.org"), ChainID: getEnvInt64("BASE_MAINNET_CHAIN_ID", 8453), NativeCurrency: "ETH"},
		"optimism":  {Name: "Optimism", RPCURL: getEnv("OPTIMISM_MAINNET_RPC_URL", "https://mainnet.optimism.io"), ChainID: getEnvInt64("OPTIMISM_MAINNET_CHAIN_ID", 10), NativeCurrency: "ETH"},
		"avalanche": {Name: "Avalanche C-Chain", RPCURL: getEnv("AVALANCHE_MAINNET_RPC_URL", "https://api.avax.network/ext/bc/C/rpc"), ChainID: getEnvInt64("AVALANCHE_MAINNET_CHAIN_ID", 43114), NativeCurrency: "AVAX"},
		"linea":     {Name: "Linea", RPCURL: getEnv("LINEA_MAINNET_RPC_URL", "https://rpc.linea.build"), ChainID: getEnvInt64("LINEA_MAINNET_CHAIN_ID", 59144), NativeCurrency: "ETH"},
	}
	mainnetTokens := map[string]string{}
	if cfg.MCGPTokenAddress != "" {
		mainnetTokens["sonic:MCGP"] = cfg.MCGPTokenAddress
	}
	// Mainnet stablecoin addresses. Defaults are the canonical (most-used)
	// deployments per chain; override individually via env if a different
	// bridge/wrapper is preferred.
	for tokenKey, def := range map[string]string{
		"sonic:USDT":     getEnv("MAINNET_USDT_ADDRESS", "0x6047828dc181963ba44974801ff68e538da5eaf9"),
		"sonic:USDC":     getEnv("MAINNET_USDC_ADDRESS", "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"),
		"bsc:USDT":       getEnv("BSC_MAINNET_USDT_ADDRESS", "0x55d398326f99059fF775485246999027B3197955"),
		"bsc:USDC":       getEnv("BSC_MAINNET_USDC_ADDRESS", "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d"),
		"ethereum:USDT":  getEnv("ETHEREUM_MAINNET_USDT_ADDRESS", "0xdAC17F958D2ee523a2206206994597C13D831ec7"),
		"ethereum:USDC":  getEnv("ETHEREUM_MAINNET_USDC_ADDRESS", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
		"polygon:USDT":   getEnv("POLYGON_MAINNET_USDT_ADDRESS", "0xc2132D05D31c914a87C6611C10748AEb04B58e8F"),
		"polygon:USDC":   getEnv("POLYGON_MAINNET_USDC_ADDRESS", "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"),
		"arbitrum:USDT":  getEnv("ARBITRUM_MAINNET_USDT_ADDRESS", "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9"),
		"arbitrum:USDC":  getEnv("ARBITRUM_MAINNET_USDC_ADDRESS", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"),
		"base:USDT":      getEnv("BASE_MAINNET_USDT_ADDRESS", "0xfde4C96c8593536E31F229EA8f37b2ADa2699bb2"),
		"base:USDC":      getEnv("BASE_MAINNET_USDC_ADDRESS", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"),
		"optimism:USDT":  getEnv("OPTIMISM_MAINNET_USDT_ADDRESS", "0x94b008aA00579c1307B0EF2c499aD98a8ce58e58"),
		"optimism:USDC":  getEnv("OPTIMISM_MAINNET_USDC_ADDRESS", "0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85"),
		"avalanche:USDT": getEnv("AVALANCHE_MAINNET_USDT_ADDRESS", "0x9702230A8Ea53601f5cD2dc00fDBc13d4dF4A8c7"),
		"avalanche:USDC": getEnv("AVALANCHE_MAINNET_USDC_ADDRESS", "0xB97EF9Ef8734C71904D8002F8b6Bc66Dd9c48a6E"),
		"linea:USDT":     getEnv("LINEA_MAINNET_USDT_ADDRESS", "0xA219439258ca9da29E9Cc4cE5596924745e12B93"),
		"linea:USDC":     getEnv("LINEA_MAINNET_USDC_ADDRESS", "0x176211869cA2b568f2A7D4EE941E073a821EE1ff"),
	} {
		if def != "" {
			mainnetTokens[tokenKey] = def
		}
	}

	// ── Testnet configuration ──
	// Testnet RPC defaults are public testnet endpoints; testnet token
	// addresses are intentionally left empty for chains where we don't have
	// a verified faucet/bridge stablecoin deployment to point at. Setting an
	// env var promotes a chain's testnet stablecoin in production.
	testnetChains := map[string]ChainConfig{
		"sonic":     {Name: "Sonic Testnet", RPCURL: getEnv("SONIC_TESTNET_RPC_URL", "https://rpc.testnet.soniclabs.com"), ChainID: getEnvInt64("SONIC_TESTNET_CHAIN_ID", 14601), NativeCurrency: "S"},
		"bsc":       {Name: "BSC Testnet", RPCURL: getEnv("BSC_TESTNET_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"), ChainID: getEnvInt64("BSC_TESTNET_CHAIN_ID", 97), NativeCurrency: "tBNB"},
		"ethereum":  {Name: "Sepolia", RPCURL: getEnv("ETHEREUM_TESTNET_RPC_URL", "https://ethereum-sepolia-rpc.publicnode.com"), ChainID: getEnvInt64("ETHEREUM_TESTNET_CHAIN_ID", 11155111), NativeCurrency: "ETH"},
		"polygon":   {Name: "Polygon Amoy", RPCURL: getEnv("POLYGON_TESTNET_RPC_URL", "https://rpc-amoy.polygon.technology"), ChainID: getEnvInt64("POLYGON_TESTNET_CHAIN_ID", 80002), NativeCurrency: "POL"},
		"arbitrum":  {Name: "Arbitrum Sepolia", RPCURL: getEnv("ARBITRUM_TESTNET_RPC_URL", "https://sepolia-rollup.arbitrum.io/rpc"), ChainID: getEnvInt64("ARBITRUM_TESTNET_CHAIN_ID", 421614), NativeCurrency: "ETH"},
		"base":      {Name: "Base Sepolia", RPCURL: getEnv("BASE_TESTNET_RPC_URL", "https://sepolia.base.org"), ChainID: getEnvInt64("BASE_TESTNET_CHAIN_ID", 84532), NativeCurrency: "ETH"},
		"optimism":  {Name: "OP Sepolia", RPCURL: getEnv("OPTIMISM_TESTNET_RPC_URL", "https://sepolia.optimism.io"), ChainID: getEnvInt64("OPTIMISM_TESTNET_CHAIN_ID", 11155420), NativeCurrency: "ETH"},
		"avalanche": {Name: "Avalanche Fuji", RPCURL: getEnv("AVALANCHE_TESTNET_RPC_URL", "https://api.avax-test.network/ext/bc/C/rpc"), ChainID: getEnvInt64("AVALANCHE_TESTNET_CHAIN_ID", 43113), NativeCurrency: "AVAX"},
		"linea":     {Name: "Linea Sepolia", RPCURL: getEnv("LINEA_TESTNET_RPC_URL", "https://rpc.sepolia.linea.build"), ChainID: getEnvInt64("LINEA_TESTNET_CHAIN_ID", 59141), NativeCurrency: "ETH"},
	}
	testnetTokens := map[string]string{}
	// Sonic testnet token defaults come from the testnet preset (the same
	// source the P2P stack uses when SONIC_NETWORK=testnet). This keeps the
	// wallet's token-discovery surface in sync with what the P2P escrow has
	// whitelisted — without it, the wallet looks up sonic:USDT against
	// mainnet addresses on the testnet RPC and shows nothing.
	testnetSonic := sonicPresets["testnet"]
	if addr := getEnv("TESTNET_MCGP_ADDRESS", testnetSonic.mcgpAddress); addr != "" {
		testnetTokens["sonic:MCGP"] = addr
	}
	if addr := getEnv("TESTNET_USDT_ADDRESS", testnetSonic.usdtAddress); addr != "" {
		testnetTokens["sonic:USDT"] = addr
	}
	if addr := getEnv("TESTNET_USDC_ADDRESS", testnetSonic.usdcAddress); addr != "" {
		testnetTokens["sonic:USDC"] = addr
	}
	if addr := getEnv("BSC_TESTNET_USDT_ADDRESS", getEnv("BSC_USDT_ADDRESS", "")); addr != "" {
		testnetTokens["bsc:USDT"] = addr
	}
	if addr := getEnv("BSC_TESTNET_USDC_ADDRESS", getEnv("BSC_USDC_ADDRESS", "")); addr != "" {
		testnetTokens["bsc:USDC"] = addr
	}
	// Optional testnet stablecoin overrides for the new chains. These are
	// off by default — set the env var only when the BE is pointed at a
	// specific testnet faucet token.
	for tokenKey, env := range map[string]string{
		"ethereum:USDT":  "ETHEREUM_TESTNET_USDT_ADDRESS",
		"ethereum:USDC":  "ETHEREUM_TESTNET_USDC_ADDRESS",
		"polygon:USDT":   "POLYGON_TESTNET_USDT_ADDRESS",
		"polygon:USDC":   "POLYGON_TESTNET_USDC_ADDRESS",
		"arbitrum:USDT":  "ARBITRUM_TESTNET_USDT_ADDRESS",
		"arbitrum:USDC":  "ARBITRUM_TESTNET_USDC_ADDRESS",
		"base:USDT":      "BASE_TESTNET_USDT_ADDRESS",
		"base:USDC":      "BASE_TESTNET_USDC_ADDRESS",
		"optimism:USDT":  "OPTIMISM_TESTNET_USDT_ADDRESS",
		"optimism:USDC":  "OPTIMISM_TESTNET_USDC_ADDRESS",
		"avalanche:USDT": "AVALANCHE_TESTNET_USDT_ADDRESS",
		"avalanche:USDC": "AVALANCHE_TESTNET_USDC_ADDRESS",
		"linea:USDT":     "LINEA_TESTNET_USDT_ADDRESS",
		"linea:USDC":     "LINEA_TESTNET_USDC_ADDRESS",
	} {
		if addr := os.Getenv(env); addr != "" {
			testnetTokens[tokenKey] = addr
		}
	}

	cfg.Networks = map[string]NetworkConfig{
		"mainnet": {Chains: mainnetChains, TokenAddresses: mainnetTokens},
		"testnet": {Chains: testnetChains, TokenAddresses: testnetTokens},
	}

	// Legacy flat fields point to mainnet for backward compat
	cfg.Chains = mainnetChains
	cfg.TokenAddresses = mainnetTokens

	return cfg
}

func getEnvFloat64(key string, fallback float64) float64 {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed
		}
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// getEnvDuration parses a Go duration env var (e.g. "24h", "15m", "1h30m"). An
// unset, unparseable, or non-positive value yields the fallback.
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := time.ParseDuration(strings.TrimSpace(value)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// getEnvBool parses a boolean env var. Accepts the strconv.ParseBool set
// (1/t/T/TRUE/true/0/f/F/FALSE/false …). An unset or unparseable value yields
// the fallback.
func getEnvBool(key string, fallback bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

// IsFeeModelV2Enabled is the single chokepoint for the fee/TP v2 gate. While it
// returns false the legacy handlers.DistributeTPEarnings path is live and the
// new FeeDistributionService engine must not be invoked.
func (c *Config) IsFeeModelV2Enabled() bool {
	if c == nil {
		return false
	}
	return c.FeeModelV2Enabled
}

// TPPerUSDOrDefault returns the configured TP minting rate, falling back to 1.0
// (1 TP = $1) when unset or non-positive so the engine always has a sane rate.
func (c *Config) TPPerUSDOrDefault() float64 {
	if c == nil || c.TPPerUSD <= 0 {
		return 1.0
	}
	return c.TPPerUSD
}

// GlobalPoolSettleIntervalOrDefault returns the SP4 settlement cadence, falling
// back to 24h (daily) when unset or non-positive so the job always has a sane
// tick period.
func (c *Config) GlobalPoolSettleIntervalOrDefault() time.Duration {
	if c == nil || c.GlobalPoolSettleInterval <= 0 {
		return 24 * time.Hour
	}
	return c.GlobalPoolSettleInterval
}

// parseProbeAmounts parses a "CUR:AMT,CUR:AMT" list into a map. Entries
// missing a colon or with empty key/value are skipped silently — better
// than failing boot over a typo, since the caller treats absence as
// "no amount filter for this currency".
func parseProbeAmounts(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitAndTrim(s, ",") {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		cur := strings.ToUpper(strings.TrimSpace(kv[0]))
		amt := strings.TrimSpace(kv[1])
		if cur == "" || amt == "" {
			continue
		}
		out[cur] = amt
	}
	return out
}

func splitAndTrim(s, sep string) []string {
	var result []string
	for _, part := range strings.Split(s, sep) {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
		result = append(result, trimmed)
		}
	}
	return result
}
