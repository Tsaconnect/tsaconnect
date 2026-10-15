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

// assertDistinctKey refuses to start when key's address matches an entry in
// others (a name -> hex-private-key map) of a DIFFERENT signer class
// (signerKeyClass). Since the signer consolidation the operator roles — PM
// release, IT operator, gas sponsor — may share one key: every role on one key
// and chain sends through that key's one KeyLane, so sharing no longer races a
// nonce. Across classes it stays fatal: the relayer on the OPERATOR key would
// let a hot relayer compromise debit users' MCGP, and an OPERATOR or relayer
// key equal to the TREASURY key (user funds in transit) or the company wallet
// would turn one compromise into both. An unparsable entry in others is
// skipped rather than failing the boot on a malformed but irrelevant key —
// that key's own builder is responsible for validating it.
func assertDistinctKey(name string, key *ecdsa.PrivateKey, others map[string]string) error {
	addr := crypto.PubkeyToAddress(key.PublicKey)
	class, known := signerKeyClass[name]
	for otherName, other := range others {
		if other == "" {
			continue
		}
		if oc, ok := signerKeyClass[otherName]; known && ok && oc == class {
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
	if chains == nil {
		log.Printf("[sonic-gas] dormant: no chain registry")
		return nil
	}
	key, keyVar := gasSponsorKey(cfg, "[sonic-gas]")
	if key == nil {
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
	alertRawRoleKeyInProduction(cfg, roleGasSponsor, keyVar)
	sponsor := newIntentSponsorship(nil, map[string]*services.SponsorLane{"sonic": lane}, cfg.RampSponsorTopUpUnusedDailyCap)
	sponsor.SetChainRegistry(chains)
	log.Printf("[sonic-gas] lane enabled sponsor=%s floor=%s max=%s", lane.Signer.Address(), floor, topUpMax)
	return sponsor
}

// gasSponsorKey is the gas_sponsor role's key (RAMP_SPONSOR_PRIVATE_KEY, else
// the OPERATOR's), or nil with a line under tag saying why. A key equal to a
// signer of another class is fatal (assertDistinctKey). Every sponsor lane
// built on it takes its KeyLane from the signer registry, so a second lane on
// the same chain shares that key's one nonce sequence.
func gasSponsorKey(cfg *config.Config, tag string) (*ecdsa.PrivateKey, string) {
	keyHex, keyVar := roleKey(cfg, roleGasSponsor)
	if keyHex == "" {
		log.Printf("%s dormant: neither RAMP_SPONSOR_PRIVATE_KEY nor PM_RELEASE_PRIVATE_KEY set — a P2P user or buyer with no gas is told to add some", tag)
		return nil, ""
	}
	if !rawSignerKeyAllowed(cfg) {
		log.Printf("%s dormant: raw %s refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)", tag, keyVar)
		return nil, ""
	}
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		log.Printf("%s dormant: invalid %s: %v", tag, keyVar, err)
		return nil, ""
	}
	if err := assertDistinctKey("RAMP_SPONSOR_PRIVATE_KEY", key, signerKeysExcept(cfg, "RAMP_SPONSOR_PRIVATE_KEY")); err != nil {
		fatalf("[config] REFUSING TO START: %v", err)
		return nil, ""
	}
	return key, keyVar
}

// buildP2PChainGasSponsorships is the P2P gas lane on every registered P2P
// chain other than Sonic (whose lane is buildSonicGasSponsorship's, shared
// with the marketplace funding): today the direct BSC escrow (plan API-7). No
// new key or env var (signer consolidation): each lane is the gas_sponsor key
// on that chain and network, with the ramp sponsor's existing limits for the
// chain (RAMP_SPONSOR_BALANCE_FLOOR_WEI_<CHAIN> — alert-only —,
// RAMP_SPONSOR_TOPUP_MAX_WEI_<CHAIN>, RAMP_SPONSOR_DAILY_GAS_BUDGET), under its
// own guard p2p_<chain>_gas. Each sits in a sponsorship of its own holding
// only that lane, so neither the ramp nor the marketplace funding ever sees
// it; the nonce is the key's one KeyLane on that chain, shared with a ramp
// lane there. Registered chain ids only: a dormant or gate-failed escrow gets
// no lane.
func buildP2PChainGasSponsorships(cfg *config.Config, chains *services.BlockchainService, registered []int64) map[int64]services.P2PGasLane {
	out := map[int64]services.P2PGasLane{}
	if cfg == nil || chains == nil {
		return out
	}
	var todo []config.P2PChainConfig
	for _, id := range registered {
		if cc, ok := cfg.P2PChains[id]; ok && cc.ChainKey != "sonic" {
			todo = append(todo, cc)
		}
	}
	if len(todo) == 0 {
		return out
	}
	key, keyVar := gasSponsorKey(cfg, "[p2p-gas]")
	if key == nil {
		return out
	}
	for _, cc := range todo {
		tag := "[p2p-" + cc.ChainKey + "-gas]"
		floor, topUpMax, budget, skip := rampSponsorLaneLimits(cfg, cc.ChainKey)
		if skip != "" {
			log.Printf("%s ALERT p2p_gas_lane_skipped_total=1 chain=%s(%d) reason=%s", tag, cc.ChainKey, cc.ChainID, skip)
			continue
		}
		lane := buildSponsorLaneOn(chains, cc.Network, cc.ChainKey, roleGasSponsor, key, "p2p_"+cc.ChainKey+"_gas",
			services.SponsorLimits{DailyGasBudget: budget, BalanceFloor: floor}, topUpMax)
		if lane == nil {
			log.Printf("%s dormant: no %s %s client", tag, cc.Network, cc.ChainKey)
			continue
		}
		sponsor := newIntentSponsorship(nil, map[string]*services.SponsorLane{cc.ChainKey: lane}, cfg.RampSponsorTopUpUnusedDailyCap)
		sponsor.SetChainRegistry(chains)
		out[cc.ChainID] = services.P2PGasLane{ChainKey: cc.ChainKey, Sponsor: sponsor}
		log.Printf("%s lane enabled chain=%d sponsor=%s floor=%s max=%s", tag, cc.ChainID, lane.Signer.Address(), floor, topUpMax)
	}
	if len(out) > 0 {
		alertRawRoleKeyInProduction(cfg, roleGasSponsor, keyVar)
	}
	return out
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
	keyHex, keyVar := roleKey(cfg, roleGasSponsor)
	if keyHex == "" || chains == nil {
		log.Printf("[ramp-sponsor] dormant: neither RAMP_SPONSOR_PRIVATE_KEY nor PM_RELEASE_PRIVATE_KEY set")
		return nil
	}
	if !rawSignerKeyAllowed(cfg) {
		log.Printf("[ramp-sponsor] dormant: raw %s refused in production (set ALLOW_RAW_SIGNER_KEYS=true or use KMS)", keyVar)
		return nil
	}
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		log.Printf("[ramp-sponsor] dormant: invalid %s: %v", keyVar, err)
		return nil
	}
	alertRawRoleKeyInProduction(cfg, roleGasSponsor, keyVar)
	sponsorAddr := crypto.PubkeyToAddress(key.PublicKey)
	if err := assertDistinctKey("RAMP_SPONSOR_PRIVATE_KEY", key, signerKeysExcept(cfg, "RAMP_SPONSOR_PRIVATE_KEY")); err != nil {
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
