package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// marketplaceEscrowChain is the chain the marketplace escrow lives on, by key:
// the checkout handler reads it the same way (ClientForChain("sonic")).
const marketplaceEscrowChain = "sonic"

// buildMarketplaceFunding wires "pay with another token" at checkout, or
// returns nil (routes never registered) with a line saying why.
//
// It reuses what the ramp already built — the quoter, the sell intents and the
// gas sponsor — when the Paycrest block built them; otherwise it builds a
// delivery-only quoter and intent service of its own (no Paycrest, no Base
// proof: a checkout's funding lands on Sonic and is proven there). Pass nil
// for whatever was not built.
func buildMarketplaceFunding(cfg *config.Config, chains *services.BlockchainService, quoter *services.RampSellQuoter,
	intents *services.SellIntentService, source, sonicGas *services.RampSponsorship, fees *services.FeeDistributionService) *services.MarketplaceFundingService {
	if !cfg.MarketplaceFundingEnabled {
		log.Printf("[marketplace-funding] dormant: MARKETPLACE_FUNDING is off")
		return nil
	}
	if chains == nil {
		log.Printf("[marketplace-funding] dormant: no chain registry")
		return nil
	}
	client := chains.ClientForChain(marketplaceEscrowChain)
	chainID, ok := chains.ChainIDForName(marketplaceEscrowChain)
	if client == nil || !ok {
		log.Printf("[marketplace-funding] dormant: no %s mainnet client", marketplaceEscrowChain)
		return nil
	}
	tokens := map[string]string{}
	for _, symbol := range []string{"USDC", "USDT"} {
		if addr := chains.TokenAddress(marketplaceEscrowChain, symbol); addr != "" {
			tokens[symbol] = addr
		}
	}
	if len(tokens) == 0 {
		log.Printf("[marketplace-funding] dormant: no USDC/USDT address on %s", marketplaceEscrowChain)
		return nil
	}
	if cfg.MarketplaceEscrowGasPerOrder == 0 {
		log.Printf("[marketplace-funding] dormant: MARKETPLACE_ESCROW_GAS_PER_ORDER must be > 0")
		return nil
	}
	if quoter == nil {
		// Delivery-only: no Paycrest quote service and no offramp pricing —
		// every funding names its token and is given its whole budget.
		quoter = services.NewRampSellQuoter(
			services.NewLiFiQuoteClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey, cfg.LiFiIntegratorID),
			nil, chains, "", 0)
		configureRampQuoter(quoter, cfg, chains)
	}
	if intents == nil {
		intents = services.NewSellIntentService(config.DB, quoter,
			services.NewLiFiStatusClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey), nil, nil, "", time.Now)
		wireDeliveryIntents(intents, cfg, chains, fees)
	}
	svc := services.NewMarketplaceFundingService(config.DB, services.MarketplaceFundingConfig{
		ChainID: chainID, Tokens: tokens, GasPerOrder: cfg.MarketplaceEscrowGasPerOrder,
	}, quoter, intents, nil, fundingChain{client: client}, time.Now)
	// Nil pointers stay nil interfaces: a typed nil would read as "wired".
	var src services.SourceGasSponsor
	var dst services.DestGasSponsor
	if source != nil {
		src = source
	}
	if sonicGas != nil {
		dst = sonicGas
	}
	svc.SetSponsors(src, dst)
	svc.SetSourceBalances(fundingSourceBalances{chains: chains})
	log.Printf("[marketplace-funding] enabled (chain %d, tokens %v, sonic gas lane=%v, source top-ups=%v)",
		chainID, tokens, sonicGas != nil, source != nil)
	return svc
}

// fundingChain reads the escrow chain for the funding: her balances and the
// gas price, through the same client the checkout's escrow txs are built on.
type fundingChain struct{ client *blockchain.EVMClient }

func (f fundingChain) TokenBalance(_ context.Context, token, wallet string) (*big.Int, error) {
	return f.client.GetTokenBalance(token, wallet)
}

func (f fundingChain) NativeBalance(_ context.Context, wallet string) (*big.Int, error) {
	return f.client.GetBalance(wallet)
}

func (f fundingChain) GasPrice(_ context.Context) (*big.Int, error) {
	return f.client.SuggestGasPrice()
}

// fundingSourceBalances reads her balance of the token she pays with on its own
// chain, through the registry's client for that chain.
type fundingSourceBalances struct{ chains *services.BlockchainService }

func (f fundingSourceBalances) client(chainID int64) (*blockchain.EVMClient, error) {
	client, _ := f.chains.ClientForChainID(chainID)
	if client == nil {
		return nil, fmt.Errorf("no client for chain %d", chainID)
	}
	return client, nil
}

func (f fundingSourceBalances) TokenBalanceOn(_ context.Context, chainID int64, token, wallet string) (*big.Int, error) {
	client, err := f.client(chainID)
	if err != nil {
		return nil, err
	}
	return client.GetTokenBalance(token, wallet)
}

func (f fundingSourceBalances) NativeBalanceOn(_ context.Context, chainID int64, wallet string) (*big.Int, error) {
	client, err := f.client(chainID)
	if err != nil {
		return nil, err
	}
	return client.GetBalance(wallet)
}
