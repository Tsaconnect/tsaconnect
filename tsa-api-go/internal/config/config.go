package config

import (
	"os"
	"strconv"
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
		AdminURL:            getEnv("ADMIN_URL", "http://localhost:5173"),
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
		ProductEscrowAddress:   getEnv("PRODUCT_ESCROW_ADDRESS", "0xc5E5165cbCB056E4d212727cD4A6642CD5EB886d"),
		ServiceContractAddress: getEnv("SERVICE_CONTACT_ADDRESS", "0xf870DCC5741030990aF1e43D021D986A286C77A6"),
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

	// ── Mainnet configuration ──
	// Token addresses sourced from https://docs.soniclabs.com/sonic/build-on-sonic/contract-addresses
	mainnetChains := map[string]ChainConfig{
		"sonic": {Name: "Sonic Network", RPCURL: getEnv("SONIC_MAINNET_RPC_URL", "https://rpc.soniclabs.com"), ChainID: getEnvInt64("SONIC_MAINNET_CHAIN_ID", 146), NativeCurrency: "S"},
		"bsc":   {Name: "BNB Smart Chain", RPCURL: getEnv("BSC_MAINNET_RPC_URL", "https://bsc-dataseed.binance.org"), ChainID: getEnvInt64("BSC_MAINNET_CHAIN_ID", 56), NativeCurrency: "BNB"},
	}
	mainnetTokens := map[string]string{}
	if cfg.MCGPTokenAddress != "" {
		mainnetTokens["sonic:MCGP"] = cfg.MCGPTokenAddress
	}
	// Mainnet stablecoin addresses (official Sonic contract addresses as defaults)
	if addr := getEnv("MAINNET_USDT_ADDRESS", "0x6047828dc181963ba44974801ff68e538da5eaf9"); addr != "" {
		mainnetTokens["sonic:USDT"] = addr
	}
	if addr := getEnv("MAINNET_USDC_ADDRESS", "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"); addr != "" {
		mainnetTokens["sonic:USDC"] = addr
	}
	if addr := getEnv("BSC_MAINNET_USDT_ADDRESS", "0x55d398326f99059fF775485246999027B3197955"); addr != "" {
		mainnetTokens["bsc:USDT"] = addr
	}
	if addr := getEnv("BSC_MAINNET_USDC_ADDRESS", "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d"); addr != "" {
		mainnetTokens["bsc:USDC"] = addr
	}

	// ── Testnet configuration ──
	testnetChains := map[string]ChainConfig{
		"sonic": {Name: "Sonic Testnet", RPCURL: getEnv("SONIC_TESTNET_RPC_URL", "https://rpc.testnet.soniclabs.com"), ChainID: getEnvInt64("SONIC_TESTNET_CHAIN_ID", 14601), NativeCurrency: "S"},
		"bsc":   {Name: "BSC Testnet", RPCURL: getEnv("BSC_TESTNET_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"), ChainID: getEnvInt64("BSC_TESTNET_CHAIN_ID", 97), NativeCurrency: "tBNB"},
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
