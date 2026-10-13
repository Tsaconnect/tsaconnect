package main

import (
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fatalf terminates boot. It is a package-level indirection — rather than a
// direct log.Fatalf call — purely so a test can intercept a boot-time
// refusal (assertDistinctKey's callers) without exec'ing a subprocess.
// Production always runs the real log.Fatalf, which never returns.
//
// WARNING: tests that swap this var (see refusesToStart) are NOT safe under
// t.Parallel() — two parallel tests would race on the same package-level
// variable and could each restore the other's swap mid-test.
var fatalf = log.Fatalf

// assertDistinctKey refuses to start when key's address matches any entry in
// others (a name -> hex-private-key map). It is the ONE collision check every
// privileged signer goes through — the relayer, the IT operator, the PM
// release key, the ramp sponsor and the ramp treasury are all hot keys that
// broadcast from this server, so sharing any two of them turns a single
// compromise into a compromise of both. An unparsable entry in others is
// skipped rather than failing the boot on a malformed but irrelevant key —
// that key's own builder is responsible for validating it.
func assertDistinctKey(name string, key *ecdsa.PrivateKey, others map[string]string) error {
	addr := crypto.PubkeyToAddress(key.PublicKey)
	for otherName, other := range others {
		if other == "" {
			continue
		}
		k, err := crypto.HexToECDSA(strings.TrimPrefix(other, "0x"))
		if err != nil {
			continue
		}
		if crypto.PubkeyToAddress(k.PublicKey) == addr {
			return fmt.Errorf("%s is the same key as %s", name, otherName)
		}
	}
	return nil
}

// rampLaneTokens resolves the mainnet stablecoin addresses configured for
// chain — the only tokens either a sponsor lane or a treasury lane ever moves.
func rampLaneTokens(chains *services.BlockchainService, chain string) map[string]string {
	return rampLaneTokensOn(chains, "mainnet", chain)
}

// rampLaneTokensOn is rampLaneTokens on a named network (the pool chain may
// run on testnet).
func rampLaneTokensOn(chains *services.BlockchainService, network, chain string) map[string]string {
	tokens := map[string]string{}
	for _, symbol := range []string{"USDC", "USDT"} {
		if addr := chains.TokenAddressForNetwork(network, chain, symbol); addr != "" {
			tokens[symbol] = addr
		}
	}
	return tokens
}

// buildSponsorLane is buildSponsorLaneOn on mainnet, where every sponsor lane
// and the conversion treasury's lanes live.
func buildSponsorLane(chains *services.BlockchainService, chain, role string, key *ecdsa.PrivateKey, guardName string, limits services.SponsorLimits, topUpMaxWei *big.Int) *services.SponsorLane {
	return buildSponsorLaneOn(chains, "mainnet", chain, role, key, guardName, limits, topUpMaxWei)
}

// buildSponsorLaneOn builds one SponsorLane for chain on network — the shape
// both the gas sponsor and the ramp treasury need (chain client,
// EVMRelayChain, EVMRelayTxBuilder, NewLocalKeyReleaseSigner,
// NewSponsorGuard). Returns nil when chain has no configured RPC client on
// that network. Its send lane comes from the process's SignerRegistry
// (chains.Signers()), so role shares one nonce lock and floor with every other
// role on the same key and chain. guardName namespaces the guard's own kill switch/daily
// counter (e.g. "ramp_sponsor_base" vs "ramp_treasury_base") so the two lane
// types never share one.
func buildSponsorLaneOn(chains *services.BlockchainService, network, chain, role string, key *ecdsa.PrivateKey, guardName string, limits services.SponsorLimits, topUpMaxWei *big.Int) *services.SponsorLane {
	client := chains.ClientForNetwork(network, chain)
	if client == nil {
		return nil
	}
	keyLane := chains.Signers().Lane(role, key, client)
	if keyLane == nil {
		return nil
	}
	// Ramp lanes do not rate-limit per user, so no PerUserPerWindow and
	// ReserveUser is never called on them: each sponsored tx needs a real
	// order (one per order), a user holds one open sell order at a time,
	// and top-ups are capped by how many went unused.
	return &services.SponsorLane{
		ChainKey:    chain,
		Chain:       services.EVMRelayChain{Client: client},
		Builder:     services.EVMRelayTxBuilder{Client: client},
		Native:      client,
		Signer:      keyLane.Signer(),
		KeyLane:     keyLane,
		Guard:       services.NewSponsorGuard(guardName, limits, nil),
		Tokens:      rampLaneTokensOn(chains, network, chain),
		TopUpMaxWei: topUpMaxWei,
	}
}

// newIntentSponsorship builds a RampSponsorship. Without Paycrest orders it
// serves intent top-ups only (the marketplace funding's), and the Paycrest
// order routes are never registered, so the order reader is never called.
// Branching rather than passing `orders` through: a nil *PaycrestOrderService
// in the sponsorOrderReader interface is a NON-nil interface holding a nil
// pointer, and any order path reaching it would panic instead of refusing.
func newIntentSponsorship(orders *services.PaycrestOrderService, lanes map[string]*services.SponsorLane, unusedCap int) *services.RampSponsorship {
	store := &services.GormSponsorshipStore{DB: config.DB}
	if orders != nil {
		return services.NewRampSponsorship(orders, store, lanes, unusedCap, 30*time.Second, nil)
	}
	return services.NewRampSponsorship(nil, store, lanes, unusedCap, 30*time.Second, nil)
}

// buildSonicGasSponsorship builds THE Sonic gas lane on the ramp sponsor key,
// or nil with a line saying why. There is exactly one: P2P top-ups use it
// whenever it exists and the marketplace funding while MARKETPLACE_SONIC_GAS
// is on (marketplaceSonicGas). A second lane on the same key would hold its
// own guard; the nonce lock is the key's KeyLane either way. It is a separate
// sponsorship holding ONLY the Sonic lane, so it can never give a sale or a
// TSA Pay intent from Sonic a source-chain top-up. Floor and ceiling are
// SONIC_GAS_FLOOR_WEI / SONIC_GAS_TOPUP_MAX_WEI (the old MARKETPLACE_SONIC_GAS_*
// names still read); guard and metrics are named sonic_gas.
func buildSonicGasSponsorship(cfg *config.Config, chains *services.BlockchainService) *services.RampSponsorship {
	if cfg.RampSponsorPrivateKey == "" || chains == nil {
		log.Printf("[sonic-gas] dormant: RAMP_SPONSOR_PRIVATE_KEY not set — a P2P user or buyer with no S is told to add some")
		return nil
	}
	if !rawSignerKeyAllowed(cfg) {
		log.Printf("[sonic-gas] dormant: raw RAMP_SPONSOR_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)")
		return nil
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.RampSponsorPrivateKey, "0x"))
	if err != nil {
		log.Printf("[sonic-gas] dormant: invalid RAMP_SPONSOR_PRIVATE_KEY: %v", err)
		return nil
	}
	if err := assertDistinctKey("RAMP_SPONSOR_PRIVATE_KEY", key, map[string]string{
		"RELAYER_PRIVATE_KEY":              cfg.RelayerPrivateKey,
		"IT_OPERATOR_PRIVATE_KEY":          cfg.ITOperatorPrivateKey,
		"PM_RELEASE_PRIVATE_KEY":           cfg.PMReleasePrivateKey,
		"RAMP_TREASURY_PRIVATE_KEY":        cfg.RampTreasuryPrivateKey,
		"RAMP_TREASURY_WALLET_PRIVATE_KEY": cfg.RampTreasuryWalletPrivateKey,
	}); err != nil {
		fatalf("[config] REFUSING TO START: %v", err)
		return nil
	}
	floor := bigFromDecimal(cfg.SonicGasFloorWei)
	topUpMax := bigFromDecimal(cfg.SonicGasTopUpMaxWei)
	budget := bigFromDecimal(cfg.RampSponsorDailyGasBudget)
	if floor == nil || topUpMax == nil || budget == nil {
		log.Printf("[sonic-gas] ALERT sonic_gas_skipped_total=1 reason=invalid SONIC_GAS_FLOOR_WEI / SONIC_GAS_TOPUP_MAX_WEI / RAMP_SPONSOR_DAILY_GAS_BUDGET")
		return nil
	}
	lane := buildSponsorLane(chains, "sonic", "gas_sponsor", key, "sonic_gas", services.SponsorLimits{
		DailyGasBudget: budget, BalanceFloor: floor,
	}, topUpMax)
	if lane == nil {
		log.Printf("[sonic-gas] dormant: no sonic mainnet client")
		return nil
	}
	alertRawSignerKeyInProduction(cfg, "RAMP_SPONSOR_PRIVATE_KEY")
	sponsor := newIntentSponsorship(nil, map[string]*services.SponsorLane{"sonic": lane}, cfg.RampSponsorTopUpUnusedDailyCap)
	sponsor.SetChainRegistry(chains)
	log.Printf("[sonic-gas] lane enabled sponsor=%s floor=%s max=%s", lane.Signer.Address(), floor, topUpMax)
	return sponsor
}

// marketplaceSonicGas is the shared Sonic lane as the marketplace funding sees
// it: there only while MARKETPLACE_SONIC_GAS is on, exactly as when the
// funding built a lane of its own.
func marketplaceSonicGas(cfg *config.Config, shared *services.RampSponsorship) *services.RampSponsorship {
	if !cfg.MarketplaceSonicGasEnabled {
		log.Printf("[marketplace-funding] sonic gas dormant: MARKETPLACE_SONIC_GAS is off — a buyer with no S is refused before signing")
		return nil
	}
	if shared == nil {
		log.Printf("[marketplace-funding] sonic gas dormant: no shared sonic gas lane (see [sonic-gas])")
		return nil
	}
	return shared
}

// rampSponsorLaneLimits validates a chain's eligibility for a sponsor lane and
// returns its parsed limits. A lane MUST NOT be built with a nil BalanceFloor,
// DailyGasBudget or TopUpMaxWei: nil means "no limit" downstream (unbounded
// top-ups in gas_topup.go, sponsoring that never stops in sponsor_guard.go, no
// daily cap in sponsor_guard.go), so a missing or malformed override must skip
// the chain rather than silently sponsor it without a limit. Also enforces the
// Phase 1 allowlist — chains without a floor/top-up default (e.g. ethereum)
// must never get a lane, even if a chain client happens to exist for them.
func rampSponsorLaneLimits(cfg *config.Config, chain string) (floor, topUpMax, dailyBudget *big.Int, skipReason string) {
	allowed := map[string]bool{"base": true, "arbitrum": true, "polygon": true, "bsc": true}
	if !allowed[chain] {
		return nil, nil, nil, "not a phase 1 chain"
	}
	up := strings.ToUpper(chain)
	floor = bigFromDecimal(cfg.RampSponsorBalanceFloorWei[chain])
	if floor == nil {
		return nil, nil, nil, "invalid or missing RAMP_SPONSOR_BALANCE_FLOOR_WEI_" + up
	}
	topUpMax = bigFromDecimal(cfg.RampSponsorTopUpMaxWei[chain])
	if topUpMax == nil {
		return nil, nil, nil, "invalid or missing RAMP_SPONSOR_TOPUP_MAX_WEI_" + up
	}
	dailyBudget = bigFromDecimal(cfg.RampSponsorDailyGasBudget)
	if dailyBudget == nil {
		return nil, nil, nil, "invalid or missing RAMP_SPONSOR_DAILY_GAS_BUDGET"
	}
	return floor, topUpMax, dailyBudget, ""
}

// buildRampSponsorship wires one sponsor lane per configured chain, or returns
// nil (dormant) with a log line saying why. A key shared with any privileged
// signer is fatal: the sponsor key is hot and broadcasts on every sale, so a
// compromise of it must never be a compromise of the relayer, IT operator or
// PM release key.
func buildRampSponsorship(cfg *config.Config, chains *services.BlockchainService, orders *services.PaycrestOrderService) *services.RampSponsorship {
	if !cfg.RampGasSponsorshipEnabled {
		log.Printf("[ramp-sponsor] dormant: RAMP_GAS_SPONSORSHIP is off")
		return nil
	}
	if cfg.RampSponsorPrivateKey == "" || chains == nil {
		log.Printf("[ramp-sponsor] dormant: RAMP_SPONSOR_PRIVATE_KEY not set")
		return nil
	}
	if !rawSignerKeyAllowed(cfg) {
		log.Printf("[ramp-sponsor] dormant: raw RAMP_SPONSOR_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)")
		return nil
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.RampSponsorPrivateKey, "0x"))
	if err != nil {
		log.Printf("[ramp-sponsor] dormant: invalid RAMP_SPONSOR_PRIVATE_KEY: %v", err)
		return nil
	}
	alertRawSignerKeyInProduction(cfg, "RAMP_SPONSOR_PRIVATE_KEY")
	sponsorAddr := crypto.PubkeyToAddress(key.PublicKey)
	if err := assertDistinctKey("RAMP_SPONSOR_PRIVATE_KEY", key, map[string]string{
		"RELAYER_PRIVATE_KEY":              cfg.RelayerPrivateKey,
		"IT_OPERATOR_PRIVATE_KEY":          cfg.ITOperatorPrivateKey,
		"PM_RELEASE_PRIVATE_KEY":           cfg.PMReleasePrivateKey,
		"RAMP_TREASURY_PRIVATE_KEY":        cfg.RampTreasuryPrivateKey,
		"RAMP_TREASURY_WALLET_PRIVATE_KEY": cfg.RampTreasuryWalletPrivateKey,
	}); err != nil {
		fatalf("[config] REFUSING TO START: %v", err)
		return nil
	}

	lanes := map[string]*services.SponsorLane{}
	for _, chain := range cfg.RampSponsorChains {
		floor, topUpMax, dailyBudget, skipReason := rampSponsorLaneLimits(cfg, chain)
		if skipReason != "" {
			log.Printf("[ramp-sponsor] ALERT ramp_sponsor_lane_skipped_total=1 chain=%s reason=%s", chain, skipReason)
			continue
		}
		lane := buildSponsorLane(chains, chain, "gas_sponsor", key, "ramp_sponsor_"+chain, services.SponsorLimits{
			DailyGasBudget: dailyBudget,
			BalanceFloor:   floor,
		}, topUpMax)
		if lane == nil {
			log.Printf("[ramp-sponsor] skipping %s: no chain client", chain)
			continue
		}
		lanes[chain] = lane
	}
	if len(lanes) == 0 {
		log.Printf("[ramp-sponsor] dormant: no usable chains in RAMP_SPONSOR_CHAINS")
		return nil
	}
	// 30s receipt wait keeps the top-up request under the nginx 60s proxy timeout.
	sponsor := newIntentSponsorship(orders, lanes, cfg.RampSponsorTopUpUnusedDailyCap)
	// A sell intent names its source chain by id; lanes are keyed by name.
	sponsor.SetChainRegistry(chains)
	log.Printf("[ramp-sponsor] enabled sponsor=%s chains=%v", sponsorAddr.Hex(), cfg.RampSponsorChains)
	return sponsor
}
