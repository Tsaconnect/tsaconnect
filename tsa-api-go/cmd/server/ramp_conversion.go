package main

import (
	"context"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	chainreg "github.com/ojimcy/tsa-api-go/internal/chains"
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
func buildRampConversion(cfg *config.Config, chains *services.BlockchainService, reg *chainreg.Registry, orders *services.PaycrestOrderService, quotes *services.PaycrestQuoteService) (*services.Treasury, *services.ConversionOrchestrator) {
	if !cfg.RampConversionBuyEnabled {
		log.Printf("[ramp-conversion] dormant: RAMP_CONVERSION_BUY is off")
		return nil, nil
	}
	if orders != nil && !orders.ConversionPriced() {
		log.Printf("[ramp-conversion] dormant: conversion is not priced (need 0 < B < F)")
		return nil, nil
	}
	if cfg.RampTreasuryPrivateKey == "" {
		log.Printf("[ramp-conversion] dormant: RAMP_TREASURY_PRIVATE_KEY not set")
		return nil, nil
	}
	// Routed through the one guard every raw-env-key signer uses, rather
	// than comparing cfg.Env here: an inline check ignores
	// ALLOW_RAW_SIGNER_KEYS, so the documented production rollout
	// (ENV=production + ALLOW_RAW_SIGNER_KEYS=true, see tsa-api-go/CLAUDE.md)
	// would keep every other signer alive and silently take THIS one dormant.
	if !rawSignerKeyAllowed(cfg) {
		log.Printf("[ramp-conversion] dormant: raw RAMP_TREASURY_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true, or use KMS)")
		return nil, nil
	}
	alertRawSignerKeyInProduction(cfg, "RAMP_TREASURY_PRIVATE_KEY")
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
		routers, skipReason := rampTreasuryLaneRouters(cfg, chain, lane.Tokens)
		if skipReason != "" {
			log.Printf("[ramp-conversion] ALERT ramp_treasury_lane_skipped_total=1 chain=%s reason=%s", chain, skipReason)
			missing = append(missing, chain)
			continue
		}
		lanes[chain] = &services.TreasuryLane{SponsorLane: lane, ChainID: chainID, Routers: routers}
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

	// Every configured EVM chain gets a verifier, not only treasury lanes: a
	// converted buy's DESTINATION can be a chain the treasury never sources
	// funds from directly, and that destination still needs an on-chain
	// delivery proof.
	evmChains := make([]string, 0, len(cfg.Networks["mainnet"].Chains))
	for chain := range cfg.Networks["mainnet"].Chains {
		evmChains = append(evmChains, chain)
	}
	transfers := rampTransferVerifiers(chains, evmChains)

	orchestrator := services.NewConversionOrchestrator(
		config.DB, treasury, &services.GormRampLegStore{DB: config.DB},
		lifiQuoteClient, statusClient, transfers, chains,
		cfg.RampConversionRetryWindow, cfg.RampConversionStallWindow, cfg.RampConversionLegCeiling,
		cfg.RampConversionMaxShortfallUSD, nil,
	)
	wireSolanaDestinations(reg, orchestrator, quotes)

	// The same B the quote and order services are priced on (pushed there by
	// SetRoundTripBudgetBps in main.go): a route is executed against exactly
	// the budget the order was quoted on, never a second copy of the number.
	orchestrator.SetRoundTripBudgetBps(cfg.RampRoundTripBudgetBps)

	if quotes != nil {
		quotes.SetLiFiQuoter(lifiQuoteClient)
		quotes.SetChainRegistry(chains)
		quotes.SetTreasuryAddress(addr)
		// The REAL gate on which requested chains a converted buy may be
		// offered on: quotable chains and treasury lanes are independent
		// sets (RAMP_TREASURY_CHAINS can be a strict subset of what
		// config.Networks knows — missing floor, no chain client, no chain
		// id), and ConvertedVenues asks `treasury` directly per request
		// rather than trusting a filtered minimums map (a minimums map with
		// no entry for a chain is NOT a refusal — MeetsMinimumOrder falls
		// back to the default floor, which makes an unlaned chain MORE
		// quotable, not less).
		quotes.SetTreasuryLanes(treasury)
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

// rampTransferVerifiers builds an ERC20TransferVerifier for every chain key
// that has a client, skipping any that don't (e.g. a non-EVM chain, or a
// chain whose RPC client failed to connect at boot). Called with every
// configured EVM chain key — not only treasury lanes — so any ERC-20
// destination a converted buy can be sent to still gets an on-chain delivery
// proof, even one the treasury never sources funds from directly.
func rampTransferVerifiers(chains *services.BlockchainService, keys []string) map[string]services.ERC20TransferVerifier {
	transfers := map[string]services.ERC20TransferVerifier{}
	for _, chain := range keys {
		if client := chains.ClientForChain(chain); client != nil {
			transfers[chain] = services.NewLogTransferVerifier(client.Client)
		}
	}
	return transfers
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

// rampTreasuryLaneRouters validates a chain's router allowlist and parses it
// once, with the same fail-closed shape as a missing native floor. ExecuteRoute
// refuses a route whose `to` or spender is off the list, so a lane whose list
// is empty — or holds a mistyped address that can never match — would be
// quotable but unexecutable; skipping it at boot keeps the chain off the menu.
//
// tokens are the lane's own token contracts. One of them on the list would
// reopen the exact drain the list exists to close (`to` = the token,
// calldata = transfer(attacker, balance)), so an override naming one is
// refused rather than trusted. The zero address is refused for the same
// reason a typo is: it can only be a mistake.
func rampTreasuryLaneRouters(cfg *config.Config, chain string, tokens map[string]string) (routers []common.Address, skipReason string) {
	envVar := "RAMP_TREASURY_ROUTERS_" + strings.ToUpper(chain)
	raw := cfg.RampTreasuryRouters[chain]
	if len(raw) == 0 {
		return nil, "no router allowlist (set " + envVar + ")"
	}
	for _, addr := range raw {
		if !common.IsHexAddress(addr) {
			return nil, "invalid address " + addr + " in " + envVar
		}
		router := common.HexToAddress(addr)
		if router == (common.Address{}) {
			return nil, "zero address in " + envVar
		}
		for symbol, token := range tokens {
			if common.IsHexAddress(token) && common.HexToAddress(token) == router {
				return nil, "token contract " + symbol + " (" + addr + ") in " + envVar
			}
		}
		routers = append(routers, router)
	}
	return routers, ""
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
//
// keptUSDCAlert is cfg.RampTreasuryKeptUSDCAlert: only this (treasury)
// registration of the monitor carries it — the gas sponsor lane has no
// kept-token float to sweep.
func registerRampConversionWorkers(scheduler *jobs.Scheduler, treasury *services.Treasury, orch *services.ConversionOrchestrator, keptUSDCAlert float64) {
	if orch == nil {
		return
	}
	scheduler.Register(jobs.NewConversionOrchestratorWorker(orch, 30*time.Second), jobs.AlertOnFailureStreak())
	// Registered a second time under its own metric prefix, alongside the
	// gas sponsor's own float-monitor registration: same job type, two
	// independent floats to watch.
	scheduler.Register(jobs.NewGasFloatMonitorNamed("ramp_treasury", treasury, 10*time.Minute).WithKeptUSDCAlert(keptUSDCAlert))
}

// rampNonEVMNetwork is which registry network a converted buy's non-EVM
// destination is read from. Mainnet, to match the EVM side: the treasury's
// own chains come from cfg.Networks["mainnet"], so proving a landing against
// a testnet client would prove it on a chain no converted buy ever runs on.
const rampNonEVMNetwork = "mainnet"

// solanaLandingProver is the one thing the converted-buy state machine needs
// from a Solana chain client: what a transaction actually credited an address.
// Declared here (rather than exported from services) so the wiring, not the
// state machine, owns the type assertion against the registry's ChainClient.
type solanaLandingProver interface {
	ReceivedBy(ctx context.Context, txHash, owner, mint string) (*big.Int, error)
}

// wireSolanaDestinations turns Solana on as a converted-buy destination, or
// leaves it off with a line saying why. Both halves come from the SAME client:
// the address check that decides whether a Solana asset can be quoted at all,
// and the balance read that proves its landing. Wiring one without the other
// would either offer a destination nothing can prove, or refuse to quote a
// chain we can in fact settle on.
func wireSolanaDestinations(reg *chainreg.Registry, orch *services.ConversionOrchestrator, quotes *services.PaycrestQuoteService) {
	if reg == nil {
		log.Printf("[ramp-conversion] solana destinations dormant: no non-EVM chain registry")
		return
	}
	client, ok := reg.ForKey(rampNonEVMNetwork, "solana")
	if !ok {
		log.Printf("[ramp-conversion] solana destinations dormant: no %s solana chain client", rampNonEVMNetwork)
		return
	}
	land, ok := client.(solanaLandingProver)
	if !ok {
		// A Solana client that cannot prove a landing must not make Solana
		// quotable either — an order it can never settle is worse than one
		// that was never offered.
		log.Printf("[ramp-conversion] ALERT ramp_solana_no_landing_proof_total=1 — solana destinations stay off")
		return
	}
	orch.SetSolanaLanding(land)
	if quotes != nil {
		quotes.SetSolanaValidator(client.ValidateAddress)
	}
	log.Printf("[ramp-conversion] solana destinations enabled (landing proof + address validation from the %s solana client)", rampNonEVMNetwork)
}

// buildRampSellQuoter wires the sell half of the round trip (design §3.2), or
// returns nil — dormant, route never registered — with a line saying why.
//
// It needs no treasury: a swapped sell runs entirely in the user's own
// wallet, and the only money we spend is gas. What it does need is the same
// budget B the buy is priced from, so an unpriced conversion leaves it off.
func buildRampSellQuoter(cfg *config.Config, chains *services.BlockchainService, orders *services.PaycrestOrderService, quotes *services.PaycrestQuoteService) *services.RampSellQuoter {
	if !cfg.RampConversionSellEnabled {
		log.Printf("[ramp-sell] dormant: RAMP_CONVERSION_SELL is off")
		return nil
	}
	if orders == nil || !orders.ConversionPriced() {
		log.Printf("[ramp-sell] dormant: conversion is not priced (need 0 < B < F)")
		return nil
	}
	if chains == nil || quotes == nil {
		log.Printf("[ramp-sell] dormant: no chain registry or quote service")
		return nil
	}
	q := services.NewRampSellQuoter(
		services.NewLiFiQuoteClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey, cfg.LiFiIntegratorID),
		quotes, chains, cfg.RampRoundTripBudgetBps, orders.ConvertedFeePercent(), cfg.RampSellSponsoredGasUSD)
	// Whether she can pay her own gas on the source chain decides whether a
	// top-up is priced into the budget. A chain we cannot read is treated as
	// "she has none" by the quoter, which over-prices rather than strands.
	q.SetNativeBalanceReader(func(_ context.Context, chainID int64, addr string) (*big.Int, bool) {
		client, _ := chains.ClientForChainID(chainID)
		if client == nil {
			return nil, false
		}
		bal, err := client.GetBalance(addr)
		if err != nil {
			return nil, false
		}
		return bal, true
	})
	// Where her signature may go: the same pinned, boot-validated router
	// allowlist the treasury signs against. A chain missing from it cannot be
	// sold from — refused at the quote, never handed over unchecked.
	q.SetRouterAllowlist(rampSellRouters(cfg, chains))
	log.Printf("[ramp-sell] swapped sells enabled (B=%d bps, sponsored relay $%.4f, offramp fee %s%%)",
		cfg.RampRoundTripBudgetBps, cfg.RampSellSponsoredGasUSD, orders.ConvertedFeePercent())
	return q
}

// rampSellRouters is the router allowlist per source chain id, for the sell
// quote. It is the treasury's own list (RAMP_TREASURY_ROUTERS_<CHAIN>, LiFi's
// pinned diamond by default) run through the treasury's own validation, so a
// chain the treasury would refuse to route through is refused for her too.
func rampSellRouters(cfg *config.Config, chains *services.BlockchainService) map[int64][]common.Address {
	out := map[int64][]common.Address{}
	for chain := range cfg.RampTreasuryRouters {
		id, ok := chains.ChainIDForName(chain)
		if !ok {
			continue
		}
		routers, skipReason := rampTreasuryLaneRouters(cfg, chain, rampLaneTokens(chains, chain))
		if skipReason != "" {
			log.Printf("[ramp-sell] ALERT ramp_sell_routers_skipped_total=1 chain=%s reason=%s — no sells from it", chain, skipReason)
			continue
		}
		out[id] = routers
	}
	return out
}

// buildSellIntentService builds the service that drives one swapped sale to
// its payout. Nil (and no routes) whenever the sell quote itself is off, or
// when the two things a §5.3 binding cannot be checked without are missing:
// a Base RPC to prove the landing, and the settlement token's own address.
//
// The USDC address is read with the SAME call the quoter uses
// (TokenAddressForChainID on the settlement chain id), not a chain-key
// lookup: the binding compares what landed against this string, so a second
// spelling of "USDC on Base" would make every honest sale unclaimable.
func buildSellIntentService(quoter *services.RampSellQuoter, cfg *config.Config,
	chains *services.BlockchainService, orders *services.PaycrestOrderService,
	fees *services.FeeDistributionService) *services.SellIntentService {
	if quoter == nil || chains == nil || orders == nil {
		return nil
	}
	base := chains.ClientForChain(rampSettlementChainKey)
	if base == nil {
		log.Printf("[ramp-sell] sales dormant: no %s client to prove a landing on", rampSettlementChainKey)
		return nil
	}
	usdc := chains.TokenAddressForChainID(rampSettlementChainID, rampSettlementToken)
	if usdc == "" {
		log.Printf("[ramp-sell] sales dormant: no %s address configured on chain %d",
			rampSettlementToken, rampSettlementChainID)
		return nil
	}
	svc := services.NewSellIntentService(config.DB, quoter,
		services.NewLiFiStatusClient(cfg.LiFiBaseURL, cfg.LiFiAPIKey),
		services.NewLogTransferVerifier(base.Client), orders, usdc, time.Now)
	if fees != nil {
		svc.SetFeeService(fees)
	}
	log.Printf("[ramp-sell] sales enabled (settling %s on chain %d)", rampSettlementToken, rampSettlementChainID)
	return svc
}

// rampSettlementChainID / rampSettlementChainKey / rampSettlementToken name
// the one leg a swapped sell lands on, mirroring the service constants
// (design §3.2). Stated here rather than exported from services because they
// are wiring, not behaviour.
const (
	rampSettlementChainID  int64 = 8453
	rampSettlementChainKey       = "base"
	rampSettlementToken          = "USDC"
)
