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
	NativeCurrency string // "S", "tBNB", etc.
}

type Config struct {
	DatabaseURL         string
	JWTSecret           string
	FrontendURL         string
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

	// Multi-chain configuration (populated in Load)
	Chains         map[string]ChainConfig
	TokenAddresses map[string]string // "chain:symbol" → contract address
}

func Load() *Config {
	cfg := &Config{
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://localhost:5432/tsa?sslmode=disable"),
		JWTSecret:           getEnv("JWT_SECRET", ""),
		FrontendURL:         getEnv("FRONTEND_URL", "http://localhost:3000"),
		Port:                getEnv("PORT", "5000"),
		Env:                 getEnv("ENV", "development"),
		CloudinaryCloudName: getEnv("CLOUDINARY_CLOUD_NAME", ""),
		CloudinaryAPIKey:    getEnv("CLOUDINARY_API_KEY", ""),
		CloudinaryAPISecret: getEnv("CLOUDINARY_API_SECRET", ""),

		// Sonic blockchain
		SonicRPCURL:         getEnv("SONIC_RPC_URL", "https://rpc.testnet.soniclabs.com"),
		SonicChainID:        getEnvInt64("SONIC_CHAIN_ID", 14601),
		MCGPTokenAddress:    getEnv("MCGP_TOKEN_ADDRESS", "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"),
		USDTTokenAddress:    getEnv("USDT_TOKEN_ADDRESS", ""),
		USDCTokenAddress:    getEnv("USDC_TOKEN_ADDRESS", ""),
		SystemWalletAddress: getEnv("SYSTEM_WALLET_ADDRESS", "0xaF326D5D242C9A55590540f14658adDDd3586A8d"),

		// BSC blockchain
		BSCRPCURL:  getEnv("BSC_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"),
		BSCChainID: getEnvInt64("BSC_CHAIN_ID", 97),
	}

	cfg.Chains = map[string]ChainConfig{
		"sonic": {Name: "Sonic Network", RPCURL: cfg.SonicRPCURL, ChainID: cfg.SonicChainID, NativeCurrency: "S"},
		"bsc":   {Name: "BNB Smart Chain", RPCURL: cfg.BSCRPCURL, ChainID: cfg.BSCChainID, NativeCurrency: "tBNB"},
	}

	cfg.TokenAddresses = map[string]string{}
	if cfg.MCGPTokenAddress != "" {
		cfg.TokenAddresses["sonic:MCGP"] = cfg.MCGPTokenAddress
	}
	if cfg.USDTTokenAddress != "" {
		cfg.TokenAddresses["sonic:USDT"] = cfg.USDTTokenAddress
	}
	if cfg.USDCTokenAddress != "" {
		cfg.TokenAddresses["sonic:USDC"] = cfg.USDCTokenAddress
	}
	bscUSDT := getEnv("BSC_USDT_ADDRESS", "")
	if bscUSDT != "" {
		cfg.TokenAddresses["bsc:USDT"] = bscUSDT
	}
	bscUSDC := getEnv("BSC_USDC_ADDRESS", "")
	if bscUSDC != "" {
		cfg.TokenAddresses["bsc:USDC"] = bscUSDC
	}

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
