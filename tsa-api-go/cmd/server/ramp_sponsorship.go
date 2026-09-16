package main

import (
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

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
	if cfg.RampSponsorPrivateKey == "" || orders == nil || chains == nil {
		log.Printf("[ramp-sponsor] dormant: RAMP_SPONSOR_PRIVATE_KEY not set or Paycrest orders unavailable")
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
	for name, other := range map[string]string{
		"RELAYER_PRIVATE_KEY":     cfg.RelayerPrivateKey,
		"IT_OPERATOR_PRIVATE_KEY": cfg.ITOperatorPrivateKey,
		"PM_RELEASE_PRIVATE_KEY":  cfg.PMReleasePrivateKey,
	} {
		if other == "" {
			continue
		}
		if k, kerr := crypto.HexToECDSA(strings.TrimPrefix(other, "0x")); kerr == nil && crypto.PubkeyToAddress(k.PublicKey) == sponsorAddr {
			log.Fatalf("[config] REFUSING TO START: RAMP_SPONSOR_PRIVATE_KEY is the same key as %s", name)
		}
	}

	lanes := map[string]*services.SponsorLane{}
	for _, chain := range cfg.RampSponsorChains {
		client := chains.ClientForChain(chain)
		if client == nil {
			log.Printf("[ramp-sponsor] skipping %s: no chain client", chain)
			continue
		}
		floor, topUpMax, dailyBudget, skipReason := rampSponsorLaneLimits(cfg, chain)
		if skipReason != "" {
			log.Printf("[ramp-sponsor] ALERT ramp_sponsor_lane_skipped_total=1 chain=%s reason=%s", chain, skipReason)
			continue
		}
		tokens := map[string]string{}
		for _, symbol := range []string{"USDC", "USDT"} {
			if addr := chains.TokenAddress(chain, symbol); addr != "" {
				tokens[symbol] = addr
			}
		}
		// Ramp lanes do not rate-limit per user, so no PerUserPerWindow and
		// ReserveUser is never called on them: each sponsored tx needs a real
		// order (one per order), a user holds one open sell order at a time,
		// and top-ups are capped by how many went unused.
		lanes[chain] = &services.SponsorLane{
			ChainKey: chain,
			Chain:    services.EVMRelayChain{Client: client},
			Builder:  services.EVMRelayTxBuilder{Client: client},
			Native:   client,
			Signer:   services.NewLocalKeyReleaseSigner(key, client),
			Guard: services.NewSponsorGuard("ramp_sponsor_"+chain, services.SponsorLimits{
				DailyGasBudget: dailyBudget,
				BalanceFloor:   floor,
			}, nil),
			Tokens:      tokens,
			TopUpMaxWei: topUpMax,
		}
	}
	if len(lanes) == 0 {
		log.Printf("[ramp-sponsor] dormant: no usable chains in RAMP_SPONSOR_CHAINS")
		return nil
	}
	// 30s receipt wait keeps the top-up request under the nginx 60s proxy timeout.
	sponsor := services.NewRampSponsorship(orders, &services.GormSponsorshipStore{DB: config.DB}, lanes,
		cfg.RampSponsorTopUpUnusedDailyCap, 30*time.Second, nil)
	log.Printf("[ramp-sponsor] enabled sponsor=%s chains=%v", sponsorAddr.Hex(), cfg.RampSponsorChains)
	return sponsor
}
