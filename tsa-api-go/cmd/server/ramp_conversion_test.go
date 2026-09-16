package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// testKeyA and testKeyB are two distinct, well-formed (but obviously fake)
// secp256k1 private keys — 64 hex chars (32 bytes), each well under the
// curve order — used across these boot-wiring tests.
const (
	testKeyA = "1111111111111111111111111111111111111111111111111111111111111111"
	testKeyB = "2222222222222222222222222222222222222222222222222222222222222222"
)

// testConfig returns a minimal, non-production Config safe to mutate per
// test — every field these tests touch is set explicitly by the caller.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{Env: "test"}
}

// refusesToStart runs fn and reports whether it triggered a boot-time fatal
// refusal. It hooks the same `fatalf` package-level indirection production
// code calls instead of log.Fatalf, so this proves the real refusal path
// fired rather than merely that fn returned some error.
func refusesToStart(t *testing.T, fn func()) bool {
	t.Helper()
	orig := fatalf
	defer func() { fatalf = orig }()
	triggered := false
	fatalf = func(format string, args ...any) {
		triggered = true
		panic("refusesToStart: boot refused")
	}
	func() {
		defer func() { _ = recover() }()
		fn()
	}()
	return triggered
}

// Same key as the ramp sponsor: the treasury holds user funds, so sharing
// its key with a hot signer is the one thing that must never boot.
func TestBuildRampConversion_RefusesAKeySharedWithAnotherSigner(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampGasSponsorshipEnabled = true
	cfg.RampSponsorPrivateKey = testKeyA
	cfg.RampConversionBuyEnabled = true
	cfg.RampTreasuryPrivateKey = testKeyA
	if !refusesToStart(t, func() { buildRampConversion(cfg, nil, nil, nil) }) {
		t.Fatal("a shared treasury key must refuse to start")
	}
}

func TestBuildRampConversion_DormantWithoutTheFlag(t *testing.T) {
	cfg := testConfig(t)
	cfg.RampConversionBuyEnabled = false
	cfg.RampTreasuryPrivateKey = testKeyB
	treasury, orch := buildRampConversion(cfg, nil, nil, nil)
	if treasury != nil || orch != nil {
		t.Fatal("off means dormant")
	}
}
