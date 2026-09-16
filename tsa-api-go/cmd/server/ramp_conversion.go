package main

import (
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// buildRampConversion wires the ramp treasury and the converted-buy state
// machine, or returns (nil, nil) — dormant — with a log line saying why.
//
// A key shared with any other privileged signer is fatal, not merely
// dormant: unlike the gas sponsor (which only ever pays gas), the treasury
// holds USER FUNDS in transit, so a compromise of a shared key would be a
// compromise of customer money. The collision check (assertDistinctKey) runs
// as soon as a key is available to check — after the flag, key-presence,
// production and parse checks that necessarily come first (there is no key
// to check before then) — but still well before chains/orders/quotes are
// ever consulted, so a misconfigured key is caught even when the rest of the
// wiring is unavailable.
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

	caps, ok := rampTreasuryCaps(cfg)
	if !ok {
		log.Printf("[ramp-conversion] dormant: refusing to start with an invalid exposure cap")
		return nil, nil
	}

	if chains == nil {
		log.Printf("[ramp-conversion] dormant: no chain registry available")
		return nil, nil
	}

	lanes := map[string]*services.TreasuryLane{}
	var missing []string
	for _, chain := range cfg.RampTreasuryChains {
		floor, skipReason := rampTreasuryLaneFloor(cfg, chain)
		if skipReason != "" {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_lane_skipped_total=1 chain=%s reason=%s", chain, skipReason)
			missing = append(missing, chain)
			continue
		}
		lane := buildSponsorLane(chains, chain, key, "ramp_treasury_"+chain, services.SponsorLimits{
			BalanceFloor: floor,
		}, nil)
		if lane == nil {
			log.Printf("[ramp-conversion] skipping %s: no chain client", chain)
			missing = append(missing, chain)
			continue
		}
		chainID, idOK := chains.ChainIDForName(chain)
		if !idOK {
			log.Printf("[ramp-conversion] skipping %s: no chain id configured", chain)
			missing = append(missing, chain)
			continue
		}
		lanes[chain] = &services.TreasuryLane{SponsorLane: lane, ChainID: chainID}
	}
	if len(missing) > 0 {
		// Named once, in one line, rather than only inferred from the
		// per-chain skip lines above: RampConversionMinUSDByChain is
		// populated for every configured chain regardless of whether it got
		// a lane, so without this a chain here would otherwise be quotable
		// (SetConversionMinimums below would have an entry for it) but
		// unexecutable — a quote the treasury can never fulfil.
		log.Printf("[ramp-conversion] chains configured but without a usable treasury lane (not offered as a converted-buy destination): %v", missing)
	}
	if len(lanes) == 0 {
		log.Printf("[ramp-conversion] dormant: no usable chains in RAMP_TREASURY_CHAINS")
		return nil, nil
	}

	treasury := services.NewTreasury(lanes, caps, nil)
	// An empty address means the configured lanes disagree about their
	// signer (NewTreasury already logged the mismatch ALERT) — a hard
	// refusal, not a silent proceed, because an empty address would
	// otherwise be handed to Paycrest as a delivery address.
	addr := treasury.Address()
	if addr == "" {
		log.Printf("[ramp-conversion] dormant: treasury lanes disagree on signer, refusing to hand Paycrest an empty delivery address")
		return nil, nil
	}

	// cfg.LiFiBaseURL is the same resolved base every LiFi client in the
	// backend uses (AllSwap handler, IT stable-sell verifier) — defaults to
	// LiFi's public API but can be overridden (LIFI_BASE_URL) to point a
	// local rehearsal at a stand-in server.
	lifiQuoteClient := services.NewLiFiQuoteClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey, cfg.LiFiIntegratorID)
	statusClient := services.NewLiFiStatusClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey)

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
		// Quotable chains and treasury lanes are independent sets —
		// RampConversionMinUSDByChain covers every chain in
		// RAMP_TREASURY_CHAINS, but `lanes` can be a strict subset (missing
		// floor, no chain client, no chain id). Filtering here means the
		// quote service never offers a converted venue on a chain this
		// treasury cannot actually execute on.
		quotes.SetConversionMinimums(rampQuotableConversionMinimums(cfg.RampConversionMinUSDByChain, lanes), cfg.RampConversionMinUSD)
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

// rampQuotableConversionMinimums restricts mins (populated for every chain in
// RAMP_TREASURY_CHAINS by config.Load) to the chains that actually got a
// treasury lane. Quotable chains and treasury lanes are independent sets — a
// missing floor, no chain client, or no chain id can drop a configured chain
// from lanes — so passing the unfiltered map to
// PaycrestQuoteService.SetConversionMinimums would let it offer a converted
// venue on a chain this treasury can never execute on: the user would see a
// quote and then an error at order time.
func rampQuotableConversionMinimums(mins map[string]float64, lanes map[string]*services.TreasuryLane) map[string]float64 {
	out := make(map[string]float64, len(lanes))
	for chain := range lanes {
		if v, ok := mins[chain]; ok {
			out[chain] = v
		}
	}
	return out
}

// rampTreasuryCaps validates the two exposure caps before they ever reach
// Treasury.CheckCaps. CheckCaps treats a cap <= 0 as "no cap" (its own,
// already-tested convention) — which is the OPPOSITE of what a boot-time
// misconfiguration here must mean: RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER
// or _PER_CHAIN parsing to zero or negative must refuse to start, not
// silently remove the guard it names. Same ALERT shape as a missing/
// malformed native floor (rampTreasuryLaneFloor) — fail closed, not open.
func rampTreasuryCaps(cfg *config.Config) (caps services.TreasuryCaps, ok bool) {
	if cfg.RampTreasuryMaxInflightUSDPerOrder <= 0 {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_cap_invalid_total=1 var=RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER value=%.2f",
			cfg.RampTreasuryMaxInflightUSDPerOrder)
		return services.TreasuryCaps{}, false
	}
	if cfg.RampTreasuryMaxInflightUSDPerChain <= 0 {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_cap_invalid_total=1 var=RAMP_TREASURY_MAX_INFLIGHT_USD_PER_CHAIN value=%.2f",
			cfg.RampTreasuryMaxInflightUSDPerChain)
		return services.TreasuryCaps{}, false
	}
	return services.TreasuryCaps{
		MaxInFlightUSDPerOrder: cfg.RampTreasuryMaxInflightUSDPerOrder,
		MaxInFlightUSDPerChain: cfg.RampTreasuryMaxInflightUSDPerChain,
	}, true
}

// registerRampConversionWorkers registers the converted-buy workers with
// scheduler ONLY when buildRampConversion actually built something. Split
// out of main() so the exact gate that keeps the flag-off path from
// registering anything is itself unit-testable, independent of the full
// server boot.
func registerRampConversionWorkers(scheduler *jobs.Scheduler, treasury *services.Treasury, orch *services.ConversionOrchestrator) {
	if orch == nil {
		return
	}
	scheduler.Register(jobs.NewConversionOrchestratorWorker(orch, 30*time.Second))
	// Registered a second time under its own metric prefix, alongside the
	// gas sponsor's own float-monitor registration: same job type, two
	// independent floats to watch.
	scheduler.Register(jobs.NewGasFloatMonitorNamed("ramp_treasury", treasury, 10*time.Minute))
}
