package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// TestReconcileNoDriftWhenClean: an on-chain rate that matches the replica for
// every checked key is a no-op (no drift, no refresh).
func TestReconcileNoDriftWhenClean(t *testing.T) {
	svc, _ := newSeededChainService(t)

	// Seed the chain-specific replica rows for Sonic equal to the canonical
	// seeded wildcard rates, then point the stub on-chain reader at the same.
	reader := newStubReader()
	for _, fc := range DefaultFeeConfigs() {
		// Write a Sonic chain row matching the seed and the on-chain value.
		if err := svc.RefreshFromConfirmedRate(fc.Feature, fc.TokenSymbol, i64p(sonicC),
			uint16(fc.BPS), fc.FlatAmountUSD, fc.MCGPExempt, fc.Active); err != nil {
			t.Fatalf("seed sonic replica %s: %v", fc.Feature, err)
		}
		reader.set(sonicC, fc.Feature, fc.TokenSymbol, FeeManagerRate{
			BPS:        uint16(fc.BPS),
			FlatUSD:    flatUSDToUnits(fc.FlatAmountUSD),
			MCGPExempt: fc.MCGPExempt,
			Active:     fc.Active,
		})
	}

	res, err := svc.ReconcileChains(context.Background(), cfgWithChains(sonicC), reader)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.DriftCount != 0 {
		t.Fatalf("drift = %d, want 0 (replica == chain)", res.DriftCount)
	}
	if res.Refreshed != 0 {
		t.Fatalf("refreshed = %d, want 0 (nothing to fix)", res.Refreshed)
	}
}

// TestReconcileRefreshesOnDrift: on-chain rate differs from the replica → drift
// detected and the replica is REFRESHED from chain (chain wins).
func TestReconcileRefreshesOnDrift(t *testing.T) {
	svc, _ := newSeededChainService(t)

	// Replica Sonic withdrawal = 50; on-chain says 90. The reconciler must adopt 90.
	if err := svc.RefreshFromConfirmedRate(models.FeatureWithdrawal, models.FeeTokenAny, i64p(sonicC), 50, 0, false, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reader := newStubReader()
	// Configure on-chain rates: withdrawal drifted to 90; every other canonical
	// feature matches whatever the replica currently resolves (we mark them active
	// at their seed so they don't all count as drift).
	for _, fc := range DefaultFeeConfigs() {
		bps := uint16(fc.BPS)
		if fc.Feature == models.FeatureWithdrawal && fc.TokenSymbol == models.FeeTokenAny {
			bps = 90
		}
		reader.set(sonicC, fc.Feature, fc.TokenSymbol, FeeManagerRate{
			BPS:        bps,
			FlatUSD:    flatUSDToUnits(fc.FlatAmountUSD),
			MCGPExempt: fc.MCGPExempt,
			Active:     fc.Active,
		})
		// Seed matching chain replica rows for the non-drifted features so only
		// withdrawal is a mismatch.
		if !(fc.Feature == models.FeatureWithdrawal && fc.TokenSymbol == models.FeeTokenAny) {
			_ = svc.RefreshFromConfirmedRate(fc.Feature, fc.TokenSymbol, i64p(sonicC),
				uint16(fc.BPS), fc.FlatAmountUSD, fc.MCGPExempt, fc.Active)
		}
	}

	res, err := svc.ReconcileChains(context.Background(), cfgWithChains(sonicC), reader)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.DriftCount < 1 {
		t.Fatalf("expected ≥1 drift (withdrawal 50→90), got %d", res.DriftCount)
	}
	if res.Refreshed < 1 {
		t.Fatalf("expected ≥1 refresh, got %d", res.Refreshed)
	}
	// The replica now resolves the chain value 90 for Sonic withdrawal.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", i64p(sonicC)); bps != 90 {
		t.Fatalf("post-reconcile Sonic withdrawal = %d, want 90 (chain won)", bps)
	}
}

// TestReconcileKeepsLastKnownGoodOnReadError: a failing on-chain read must NOT
// erase the replica — it keeps last-known-good and counts a read error.
func TestReconcileKeepsLastKnownGoodOnReadError(t *testing.T) {
	svc, _ := newSeededChainService(t)
	if err := svc.RefreshFromConfirmedRate(models.FeatureWithdrawal, models.FeeTokenAny, i64p(sonicC), 50, 0, false, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader := newStubReader()
	reader.rateErr = errors.New("rpc down")

	res, err := svc.ReconcileChains(context.Background(), cfgWithChains(sonicC), reader)
	if err != nil {
		t.Fatalf("reconcile should not abort the whole pass on a read error: %v", err)
	}
	if res.ReadErrors == 0 {
		t.Fatalf("expected read errors to be counted, got 0")
	}
	if res.Refreshed != 0 {
		t.Fatalf("refreshed = %d, want 0 (must keep last-known-good on RPC failure)", res.Refreshed)
	}
	// The replica is untouched — still resolves the seeded 50.
	if bps, _, _, _ := svc.RateFor(models.FeatureWithdrawal, "BTC", i64p(sonicC)); bps != 50 {
		t.Fatalf("Sonic withdrawal = %d after RPC-down reconcile, want untouched 50", bps)
	}
}

// TestReconcileNoOpWhenUnset: with no FeeManager chains configured (dormant) the
// reconciler is a clean no-op.
func TestReconcileNoOpWhenUnset(t *testing.T) {
	svc, _ := newSeededChainService(t)
	reader := newStubReader()

	res, err := svc.ReconcileChains(context.Background(), cfgWithChains(), reader)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.ChainsChecked != 0 || res.DriftCount != 0 || res.Refreshed != 0 {
		t.Fatalf("expected a clean no-op when unset, got %+v", res)
	}

	// nil reader is also a no-op.
	res, err = svc.ReconcileChains(context.Background(), cfgWithChains(sonicC), nil)
	if err != nil {
		t.Fatalf("reconcile nil reader: %v", err)
	}
	if res.ChainsChecked != 0 {
		t.Fatalf("nil reader should no-op, got chainsChecked=%d", res.ChainsChecked)
	}
}
