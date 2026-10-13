package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// rawSignerKeyAllowed is the single source of truth for whether a raw
// private key sourced from a process env var (IT operator, relayer, ramp
// sponsor, PM release local signer) may be used to sign in THIS process.
//
// Outside production a raw key is the normal dev/staging path and is always
// allowed. In production a raw key is refused unless the operator has
// explicitly opted in with ALLOW_RAW_SIGNER_KEYS=true — the interim path
// until a KMS-backed signer exists (internal/services/release_signer.go).
// Every raw-signer-key guard must call this rather than re-deriving the
// condition inline, so the four keys never drift out of sync.
func rawSignerKeyAllowed(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return !cfg.IsProduction() || cfg.AllowRawSignerKeys
}

// alertRawSignerKeyInProduction logs the required boot-time ALERT line the
// moment a raw key is actually used to sign in production (i.e. only reached
// because ALLOW_RAW_SIGNER_KEYS is set — rawSignerKeyAllowed already refused
// the key otherwise). It never logs the key material, only which named env
// var it came from, so the alert is safe to ship to any log sink.
func alertRawSignerKeyInProduction(cfg *config.Config, keyName string) {
	if cfg != nil && cfg.IsProduction() {
		log.Printf("ALERT raw_signer_key_in_production=1 key=%s", keyName)
	}
}

// alertRawRoleKeyInProduction is the ALERT for a role signing with a raw key:
// it names the env var the key really came from (PM_RELEASE_PRIVATE_KEY when
// the role falls back to the operator), never an unset one, plus the role.
func alertRawRoleKeyInProduction(cfg *config.Config, role, keyName string) {
	if cfg != nil && cfg.IsProduction() {
		log.Printf("ALERT raw_signer_key_in_production=1 key=%s role=%s", keyName, role)
	}
}

// Signer consolidation (plan 2026-10-13; owner decisions the same day): six
// hot keys become three.
//   - OPERATOR = PM_RELEASE_PRIVATE_KEY. The IT operator and the gas sponsor
//     (Sonic + ramp lanes) sign with it whenever their own var is unset. Every
//     role on one key and chain shares that key's KeyLane, so sharing is
//     nonce-safe.
//   - RELAYER = RELAYER_PRIVATE_KEY, its own key: never the operator (a hot
//     relayer key must not be able to debit users' MCGP). Unset = dormant.
//   - TREASURY = RAMP_TREASURY_PRIVATE_KEY (conversion + pool treasury).
//
// The company wallet (RAMP_TREASURY_WALLET_PRIVATE_KEY) is its own class in
// Phase 1: it must differ from all of them.
const (
	signerClassOperator      = "operator"
	signerClassRelayer       = "relayer"
	signerClassTreasury      = "treasury"
	signerClassCompanyWallet = "company_wallet"
)

// signerKeyClass is each signer env var's class. Keys of one class may be the
// same key; keys of different classes may never be.
var signerKeyClass = map[string]string{
	"PM_RELEASE_PRIVATE_KEY":           signerClassOperator,
	"IT_OPERATOR_PRIVATE_KEY":          signerClassOperator,
	"RELAYER_PRIVATE_KEY":              signerClassRelayer,
	"RAMP_SPONSOR_PRIVATE_KEY":         signerClassOperator,
	"RAMP_TREASURY_PRIVATE_KEY":        signerClassTreasury,
	"RAMP_TREASURY_WALLET_PRIVATE_KEY": signerClassCompanyWallet,
}

// Operator roles that fall back to the OPERATOR key.
const (
	roleITOperator = "it_operator"
	roleGasSponsor = "gas_sponsor"
)

// operatorRoleVar is each operator role's own env var.
var operatorRoleVar = map[string]string{
	roleITOperator: "IT_OPERATOR_PRIVATE_KEY",
	roleGasSponsor: "RAMP_SPONSOR_PRIVATE_KEY",
}

// ownRoleKey is the role's own configured key, as it came from env.
func ownRoleKey(cfg *config.Config, role string) string {
	switch role {
	case roleITOperator:
		return strings.TrimSpace(cfg.ITOperatorPrivateKey)
	case roleGasSponsor:
		return strings.TrimSpace(cfg.RampSponsorPrivateKey)
	}
	return ""
}

// operatorKey is the OPERATOR key (PM_RELEASE_PRIVATE_KEY), "" when unset or
// held by KMS.
func operatorKey(cfg *config.Config) string {
	if cfg.PMReleaseSigner == "kms" {
		return ""
	}
	return strings.TrimSpace(cfg.PMReleasePrivateKey)
}

// roleKey is the hex key an operator role signs with and the env var it came
// from: the role's own var when set, else the OPERATOR's. It is resolved here,
// where a signer is built, and never copied into the role's config field, so
// the operator secret lives in exactly one config field. ("", "") = no key.
func roleKey(cfg *config.Config, role string) (hexKey, envVar string) {
	if own := ownRoleKey(cfg, role); own != "" {
		return strings.TrimPrefix(own, "0x"), operatorRoleVar[role]
	}
	if op := operatorKey(cfg); op != "" {
		return strings.TrimPrefix(op, "0x"), "PM_RELEASE_PRIVATE_KEY"
	}
	return "", ""
}

// signerKeysExcept is every hot signer env var with the key it resolves to —
// the operator roles through roleKey — for assertDistinctKey's "others",
// without self.
func signerKeysExcept(cfg *config.Config, self string) map[string]string {
	itKey, _ := roleKey(cfg, roleITOperator)
	sponsorKey, _ := roleKey(cfg, roleGasSponsor)
	all := map[string]string{
		"PM_RELEASE_PRIVATE_KEY":           cfg.PMReleasePrivateKey,
		"IT_OPERATOR_PRIVATE_KEY":          itKey,
		"RAMP_SPONSOR_PRIVATE_KEY":         sponsorKey,
		"RELAYER_PRIVATE_KEY":              cfg.RelayerPrivateKey,
		"RAMP_TREASURY_PRIVATE_KEY":        cfg.RampTreasuryPrivateKey,
		"RAMP_TREASURY_WALLET_PRIVATE_KEY": cfg.RampTreasuryWalletPrivateKey,
	}
	delete(all, self)
	return all
}

// signerAddress is a hex key's address, or "" when it does not parse.
func signerAddress(hexKey string) string {
	k, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(hexKey), "0x"))
	if err != nil {
		return ""
	}
	return crypto.PubkeyToAddress(k.PublicKey).Hex()
}

// logSignerRoles runs once at boot and only logs how each operator role
// resolves (roleKey): its own key, the operator's (var unset), or the same key
// as the operator. A role var that is set and differs keeps its own key with a
// DEPRECATED line. It changes no config. Never logs key material.
func logSignerRoles(cfg *config.Config) {
	opAddr := signerAddress(operatorKey(cfg))
	for _, role := range []string{roleITOperator, roleGasSponsor} {
		envVar := operatorRoleVar[role]
		own := ownRoleKey(cfg, role)
		switch {
		case own == "" && opAddr != "":
			log.Printf("[signer] role=%s signs with the operator %s (%s unset)", role, opAddr, envVar)
		case own == "":
			log.Printf("[signer] role=%s has no key: %s unset and no usable PM_RELEASE_PRIVATE_KEY", role, envVar)
		case opAddr != "" && strings.EqualFold(signerAddress(own), opAddr):
			log.Printf("[signer] role=%s signs with the operator %s (%s holds the same key)", role, opAddr, envVar)
		default:
			log.Printf("[signer] DEPRECATED role=%s key=%s address=%s — delete it to sign as the operator %s",
				role, envVar, signerAddress(own), opAddr)
		}
	}
	if strings.TrimSpace(cfg.RelayerPrivateKey) == "" {
		log.Printf("[signer] role=relayer dormant: RELAYER_PRIVATE_KEY unset — the relayer keeps its own key and never falls back to the operator")
	}
}

// logSignerSummary prints one line per hot address: its class, the roles that
// took a lane on it and the chain ids it sends on.
func logSignerSummary(cfg *config.Config, reg *services.SignerRegistry) {
	opAddr, trAddr := signerAddress(cfg.PMReleasePrivateKey), signerAddress(cfg.RampTreasuryPrivateKey)
	for _, s := range reg.Summary() {
		label := "key"
		switch {
		case opAddr != "" && strings.EqualFold(s.Address, opAddr):
			label = "operator"
		case trAddr != "" && strings.EqualFold(s.Address, trAddr):
			label = "treasury"
		}
		log.Printf("[signer] %s=%s roles=%s lanes=%v", label, s.Address, strings.Join(s.Roles, ","), s.Chains)
	}
}

// buildRelayer is the gasless relayer on its key's lane, or nil with a line
// saying why.
func buildRelayer(cfg *config.Config, lane *services.KeyLane, client *blockchain.EVMClient) *services.RelayerService {
	limits := services.RelayerLimits{
		SponsorLimits: services.SponsorLimits{
			PerUserPerWindow: cfg.RelayerPerUserPerHour,
			Window:           time.Hour,
			DailyGasBudget:   bigFromDecimal(cfg.RelayerDailyGasBudget),
			BalanceFloor:     bigFromDecimal(cfg.RelayerBalanceFloorWei),
		},
		MaxDeadline:  cfg.RelayerMaxDeadline,
		GasCeiling:   cfg.RelayerGasCeiling,
		MinSponsored: bigFromDecimal(cfg.RelayerMinSponsored),
	}
	allow := []services.AllowedCall{
		// spend(to, amount, ref) — amount is calldata word 1.
		{Target: cfg.MCGPSwapBAddress, Selector: services.SelectorOf("spend(address,uint256,bytes32)"), AmountWordIndex: 1},
	}
	relayer, err := services.NewRelayerService(lane.Signer(), services.EVMRelayChain{Client: client},
		services.EVMRelayTxBuilder{Client: client}, cfg.MCGPForwarderAddress, allow, limits, nil, nil)
	if err != nil {
		log.Printf("[config] relayer init failed; gasless dormant: %v", err)
		return nil
	}
	relayer.UseKeyLane(lane)
	return relayer
}

// operatorRoleProbe re-reads operators(<IT operator>) on the swap every
// minute: IT, bills and MCGP checkout come up once it reads true (after the
// PM's addOperator, or a boot read error), and go dormant if it turns false.
// A failing read or a missing role fails the tick, so the scheduler's
// failure-streak ALERT fires while it lasts.
type operatorRoleProbe struct{ swap *services.MCGPSwapService }

func (p operatorRoleProbe) Name() string            { return "it-operator-role-probe" }
func (p operatorRoleProbe) Interval() time.Duration { return time.Minute }
func (p operatorRoleProbe) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.swap.ProbeOperatorRole(ctx)
}
