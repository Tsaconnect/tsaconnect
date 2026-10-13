package main

import (
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
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

// Signer consolidation (plan 2026-10-13): six hot keys become two.
//   - OPERATOR = PM_RELEASE_PRIVATE_KEY. The IT operator, gasless relayer and
//     gas sponsor (Sonic + ramp lanes) sign with it whenever their own var is
//     unset. Every role on one key and chain shares that key's KeyLane, so
//     sharing is nonce-safe.
//   - TREASURY = RAMP_TREASURY_PRIVATE_KEY (conversion + pool treasury).
//
// The company wallet (RAMP_TREASURY_WALLET_PRIVATE_KEY) is its own class in
// Phase 1: it must differ from both.
const (
	signerClassOperator      = "operator"
	signerClassTreasury      = "treasury"
	signerClassCompanyWallet = "company_wallet"
)

// signerKeyClass is each signer env var's class. Keys of one class may be the
// same key; keys of different classes may never be.
var signerKeyClass = map[string]string{
	"PM_RELEASE_PRIVATE_KEY":           signerClassOperator,
	"IT_OPERATOR_PRIVATE_KEY":          signerClassOperator,
	"RELAYER_PRIVATE_KEY":              signerClassOperator,
	"RAMP_SPONSOR_PRIVATE_KEY":         signerClassOperator,
	"RAMP_TREASURY_PRIVATE_KEY":        signerClassTreasury,
	"RAMP_TREASURY_WALLET_PRIVATE_KEY": signerClassCompanyWallet,
}

// operatorRole is a role that falls back to the OPERATOR key.
type operatorRole struct {
	role, envVar string
	key          *string
}

func operatorRoles(cfg *config.Config) []operatorRole {
	return []operatorRole{
		{"it_operator", "IT_OPERATOR_PRIVATE_KEY", &cfg.ITOperatorPrivateKey},
		{"relayer", "RELAYER_PRIVATE_KEY", &cfg.RelayerPrivateKey},
		{"gas_sponsor", "RAMP_SPONSOR_PRIVATE_KEY", &cfg.RampSponsorPrivateKey},
	}
}

// signerAddress is a hex key's address, or "" when it does not parse.
func signerAddress(hexKey string) string {
	k, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(hexKey), "0x"))
	if err != nil {
		return ""
	}
	return crypto.PubkeyToAddress(k.PublicKey).Hex()
}

// resolveSignerRoles runs once, right after config load and before any signer
// is built: each operator role whose own var is unset signs with the OPERATOR
// key from here on. A role var that is set and differs from the OPERATOR keeps
// its own key (today's behaviour) with a DEPRECATED line naming it; one equal
// to the OPERATOR is fine. Never logs key material, only addresses.
func resolveSignerRoles(cfg *config.Config) {
	opAddr := ""
	if cfg.PMReleaseSigner != "kms" {
		opAddr = signerAddress(cfg.PMReleasePrivateKey)
	}
	for _, r := range operatorRoles(cfg) {
		own := strings.TrimSpace(*r.key)
		switch {
		case own == "" && opAddr != "":
			*r.key = strings.TrimPrefix(strings.TrimSpace(cfg.PMReleasePrivateKey), "0x")
			log.Printf("[signer] role=%s signs with the operator %s (%s unset)", r.role, opAddr, r.envVar)
		case own == "":
			log.Printf("[signer] role=%s has no key: %s unset and no usable PM_RELEASE_PRIVATE_KEY", r.role, r.envVar)
		case opAddr != "" && strings.EqualFold(signerAddress(own), opAddr):
			log.Printf("[signer] role=%s signs with the operator %s (%s holds the same key)", r.role, opAddr, r.envVar)
		default:
			log.Printf("[signer] DEPRECATED role=%s key=%s address=%s — delete it to sign as the operator %s",
				r.role, r.envVar, signerAddress(own), opAddr)
		}
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
