package config

import (
	"os"
	"strconv"
	"strings"
)

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

	// Mailjet email service
	MailjetAPIKey    string
	MailjetSecretKey string

	// Persona KYC verification
	PersonaAPIKey        string
	PersonaTemplateID    string
	PersonaBaseURL       string
	PersonaWebhookSecret string

	// Open Exchange Rates configuration
	OpenExchangeRatesAppID string
	SupportedCurrencies     []string

	// Currencies routed through Bybit P2P (others use OER)
	BybitP2PCurrencies []string

	// Network configurations: "mainnet" and "testnet"
	Networks map[string]NetworkConfig

	// Legacy flat fields — kept for backward compat, point to mainnet
	Chains         map[string]ChainConfig
	TokenAddresses map[string]string
}

func Load() *Config {
	cfg := &Config{
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

		// Sonic blockchain (env vars override testnet defaults)
		SonicRPCURL:         getEnv("SONIC_RPC_URL", "https://rpc.testnet.soniclabs.com"),
		SonicChainID:        getEnvInt64("SONIC_CHAIN_ID", 14601),
		MCGPTokenAddress:    getEnv("MCGP_TOKEN_ADDRESS", "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"),
		USDTTokenAddress:    getEnv("USDT_TOKEN_ADDRESS", ""),
		USDCTokenAddress:    getEnv("USDC_TOKEN_ADDRESS", ""),
		SystemWalletAddress: getEnv("SYSTEM_WALLET_ADDRESS", "0xaF326D5D242C9A55590540f14658adDDd3586A8d"),

		// BSC blockchain
		BSCRPCURL:  getEnv("BSC_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"),
		BSCChainID: getEnvInt64("BSC_CHAIN_ID", 97),

		// Smart contract addresses
		ProductEscrowAddress:   getEnv("PRODUCT_ESCROW_ADDRESS", "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d"),
		ServiceContractAddress: getEnv("SERVICE_CONTACT_ADDRESS", "0x1304d7e287deB233131A9F75aA5867af1D9591d1"),
	}
	cfg.OTCMarketplaceAddress = getEnv("OTC_MARKETPLACE_ADDRESS", "")

	// Mailjet email service
	cfg.MailjetAPIKey = os.Getenv("MAILJET_API_KEY")
	cfg.MailjetSecretKey = os.Getenv("MAILJET_SECRET_KEY")

	// Persona KYC
	cfg.PersonaAPIKey = os.Getenv("PERSONA_API_KEY")
	cfg.PersonaTemplateID = os.Getenv("PERSONA_TEMPLATE_ID")
	cfg.PersonaBaseURL = getEnv("PERSONA_BASE_URL", "https://withpersona.com/api/v1")
	cfg.PersonaWebhookSecret = os.Getenv("PERSONA_WEBHOOK_SECRET")

	// Open Exchange Rates API — free tier: 1,000 req/month, cached at 1 hour
	cfg.OpenExchangeRatesAppID = os.Getenv("OPEN_EXCHANGE_RATES_APP_ID")
	// "*" returns every ISO 4217 fiat currency in the metadata table; otherwise comma-separated.
	currenciesStr := getEnv("SUPPORTED_CURRENCIES", "*")
	cfg.SupportedCurrencies = splitAndTrim(currenciesStr, ",")

	// Bybit P2P routing — only currencies with deep P2P liquidity reflect real market rates
	bybitCurrenciesStr := getEnv("BYBIT_P2P_CURRENCIES", "NGN")
	cfg.BybitP2PCurrencies = splitAndTrim(bybitCurrenciesStr, ",")

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
	if addr := getEnv("TESTNET_MCGP_ADDRESS", ""); addr != "" {
		testnetTokens["sonic:MCGP"] = addr
	}
	if addr := getEnv("TESTNET_USDT_ADDRESS", ""); addr != "" {
		testnetTokens["sonic:USDT"] = addr
	}
	if addr := getEnv("TESTNET_USDC_ADDRESS", "0x0BA304580ee7c9a980CF72e55f5Ed2E9fd30Bc51"); addr != "" {
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
