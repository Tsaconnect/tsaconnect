package main

import (
	"log"

	"github.com/ojimcy/tsa-api-go/internal/config"
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
