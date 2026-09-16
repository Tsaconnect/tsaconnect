package main

import (
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// buildRampConversion wires the ramp treasury and the converted-buy state
// machine, or returns (nil, nil) — dormant — with a log line saying why.
//
// A key shared with any other privileged signer is fatal, not merely
// dormant: unlike the gas sponsor (which only ever pays gas), the treasury
// holds USER FUNDS in transit, so a compromise of a shared key would be a
// compromise of customer money. That check runs BEFORE anything else here,
// including before chains/orders/quotes are even consulted, so a
// misconfigured key is caught even if the rest of the wiring is unavailable.
func buildRampConversion(cfg *config.Config, chains *services.BlockchainService, orders *services.PaycrestOrderService, quotes *services.PaycrestQuoteService) (*services.Treasury, *services.ConversionOrchestrator) {
	if !cfg.RampConversionBuyEnabled {
		log.Printf("[ramp-conversion] dormant: RAMP_CONVERSION_BUY is off")
		return nil, nil
	}
	if cfg.RampTreasuryPrivateKey == "" {
		log.Printf("[ramp-conversion] dormant: RAMP_TREASURY_PRIVATE_KEY not set")
		return nil, nil
	}
	if cfg.Env == "production" {
		log.Printf("[ramp-conversion] dormant: raw RAMP_TREASURY_PRIVATE_KEY refused in production (use KMS)")
		return nil, nil
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.RampTreasuryPrivateKey, "0x"))
	if err != nil {
		log.Printf("[ramp-conversion] dormant: invalid RAMP_TREASURY_PRIVATE_KEY: %v", err)
		return nil, nil
	}
	if err := assertDistinctKey("RAMP_TREASURY_PRIVATE_KEY", key, map[string]string{
		"RELAYER_PRIVATE_KEY":      cfg.RelayerPrivateKey,
		"IT_OPERATOR_PRIVATE_KEY":  cfg.ITOperatorPrivateKey,
		"PM_RELEASE_PRIVATE_KEY":   cfg.PMReleasePrivateKey,
		"RAMP_SPONSOR_PRIVATE_KEY": cfg.RampSponsorPrivateKey,
	}); err != nil {
		fatalf("[config] REFUSING TO START: %v", err)
		return nil, nil
	}

	if chains == nil {
		log.Printf("[ramp-conversion] dormant: no chain registry available")
		return nil, nil
	}

	lanes := map[string]*services.TreasuryLane{}
	for _, chain := range cfg.RampTreasuryChains {
		floor, skipReason := rampTreasuryLaneFloor(cfg, chain)
		if skipReason != "" {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_lane_skipped_total=1 chain=%s reason=%s", chain, skipReason)
			continue
		}
		lane := buildSponsorLane(chains, chain, key, "ramp_treasury_"+chain, services.SponsorLimits{
			BalanceFloor: floor,
		}, nil)
		if lane == nil {
			log.Printf("[ramp-conversion] skipping %s: no chain client", chain)
			continue
		}
		chainID, ok := chains.ChainIDForName(chain)
		if !ok {
			log.Printf("[ramp-conversion] skipping %s: no chain id configured", chain)
			continue
		}
		lanes[chain] = &services.TreasuryLane{SponsorLane: lane, ChainID: chainID}
	}
	if len(lanes) == 0 {
		log.Printf("[ramp-conversion] dormant: no usable chains in RAMP_TREASURY_CHAINS")
		return nil, nil
	}

	treasury := services.NewTreasury(lanes, services.TreasuryCaps{
		MaxInFlightUSDPerOrder: cfg.RampTreasuryMaxInflightUSDPerOrder,
		MaxInFlightUSDPerChain: cfg.RampTreasuryMaxInflightUSDPerChain,
	}, nil)
	// An empty address means the configured lanes disagree about their
	// signer (NewTreasury already logged the mismatch ALERT) — a hard
	// refusal, not a silent proceed, because an empty address would
	// otherwise be handed to Paycrest as a delivery address.
	addr := treasury.Address()
	if addr == "" {
		log.Printf("[ramp-conversion] dormant: treasury lanes disagree on signer, refusing to hand Paycrest an empty delivery address")
		return nil, nil
	}

	// li.quest/v1 is LiFi's public API base — same default the AllSwap
	// handler and the IT stable-sell verifier use.
	lifiQuoteClient := services.NewLiFiQuoteClient("https://li.quest/v1", cfg.LiFiAPIKey, cfg.LiFiIntegratorID)
	statusClient := services.NewLiFiStatusClient("https://li.quest/v1", cfg.LiFiAPIKey)

	transfers := map[string]services.ERC20TransferVerifier{}
	for chain := range lanes {
		if client := chains.ClientForChain(chain); client != nil {
			transfers[chain] = services.NewLogTransferVerifier(client.Client)
		}
	}

	orchestrator := services.NewConversionOrchestrator(
		config.DB, treasury, &services.GormRampLegStore{DB: config.DB},
		lifiQuoteClient, statusClient, transfers, chains,
		cfg.RampConversionRetryWindow, cfg.RampConversionStallWindow, cfg.RampConversionLegCeiling,
		cfg.RampConversionMaxShortfallUSD, nil,
	)

	if quotes != nil {
		quotes.SetLiFiQuoter(lifiQuoteClient)
		quotes.SetChainRegistry(chains)
		quotes.SetTreasuryAddress(addr)
		quotes.SetConversionMinimums(cfg.RampConversionMinUSDByChain, cfg.RampConversionMinUSD)
	}
	if orders != nil {
		orders.SetTreasury(addr)
		orders.SetTreasuryCaps(treasury)
		orders.SetConversionEnabled(true)
	}

	log.Printf("[ramp-conversion] enabled treasury=%s chains=%v caps=order:$%.0f/chain:$%.0f",
		addr, cfg.RampTreasuryChains, cfg.RampTreasuryMaxInflightUSDPerOrder, cfg.RampTreasuryMaxInflightUSDPerChain)
	return treasury, orchestrator
}

// rampTreasuryLaneFloor validates and parses a chain's native-balance floor.
// A lane MUST NOT be built with a nil floor: nil means "no limit" downstream
// (see rampSponsorLaneLimits' doc comment for the same reasoning), so a
// missing or malformed override skips the chain rather than running it
// unbounded.
func rampTreasuryLaneFloor(cfg *config.Config, chain string) (floor *big.Int, skipReason string) {
	floor = bigFromDecimal(cfg.RampTreasuryNativeFloorWei[chain])
	if floor == nil {
		return nil, "invalid or missing RAMP_TREASURY_NATIVE_FLOOR_WEI_" + strings.ToUpper(chain)
	}
	return floor, ""
}
