package services

import (
	"context"
	"math/big"
	"strings"
	"testing"
)

// fakeRewardChainReader is an in-memory RewardChainReader: claimed maps a
// lowercased account → on-chain claimed base units; balance is the distributor's
// MCGP balanceOf.
type fakeRewardChainReader struct {
	claimed map[string]*big.Int
	balance *big.Int
}

func (f *fakeRewardChainReader) ClaimedOf(_ context.Context, _, account string) (*big.Int, error) {
	if v, ok := f.claimed[strings.ToLower(account)]; ok {
		return new(big.Int).Set(v), nil
	}
	return big.NewInt(0), nil
}

func (f *fakeRewardChainReader) MCGPBalanceOf(_ context.Context, _ string) (*big.Int, error) {
	if f.balance == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(f.balance), nil
}

// buildPublishedEpoch builds a distribution and marks it published so the
// reconciler (which reads only PUBLISHED epochs) sees it.
func buildPublishedEpoch(t *testing.T, svc *RewardMerkleService) *BuildResult {
	t.Helper()
	b, err := svc.BuildDistribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := svc.MarkPublished(context.Background(), b.Distribution.Epoch, "0xtx"); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	return b
}

// TestReconcile_NoDrift_TreasuryOK: a fully-funded distributor with claims at or
// below the published cumulative reports no drift and no shortfall.
func TestReconcile_NoDrift_TreasuryOK(t *testing.T) {
	db := setupMerkleTestDB(t)
	mkMerkleUser(t, db, walletA, 100) // 100e18
	mkMerkleUser(t, db, walletB, 250) // 250e18
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	buildPublishedEpoch(t, svc)

	e18 := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), baseUnitScale()) }
	reader := &fakeRewardChainReader{
		// A claimed 50 of 100; B claimed nothing. outstanding = (100-50)+(250-0)=300.
		claimed: map[string]*big.Int{strings.ToLower(walletA): e18(50)},
		balance: e18(1000), // amply funded
	}

	res, err := svc.Reconcile(context.Background(), reader, "0xmcgp")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.DriftCount != 0 {
		t.Fatalf("drift = %d, want 0", res.DriftCount)
	}
	if res.OutstandingBaseUnits.Cmp(e18(300)) != 0 {
		t.Fatalf("outstanding = %s, want 300e18", res.OutstandingBaseUnits)
	}
	if res.TreasuryShort {
		t.Fatalf("treasury should be sufficient (1000e18 >= 300e18)")
	}
}

// TestReconcile_TreasuryShort: outstanding exceeds the distributor's MCGP balance
// → TreasuryShort with the exact shortfall (the §9 alert-don't-silently-fail case).
func TestReconcile_TreasuryShort(t *testing.T) {
	db := setupMerkleTestDB(t)
	mkMerkleUser(t, db, walletA, 100)
	mkMerkleUser(t, db, walletB, 250)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	buildPublishedEpoch(t, svc)

	e18 := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), baseUnitScale()) }
	reader := &fakeRewardChainReader{
		claimed: map[string]*big.Int{}, // nobody claimed → outstanding = 350e18
		balance: e18(200),              // only 200e18 on hand → short 150e18
	}

	res, err := svc.Reconcile(context.Background(), reader, "0xmcgp")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.OutstandingBaseUnits.Cmp(e18(350)) != 0 {
		t.Fatalf("outstanding = %s, want 350e18", res.OutstandingBaseUnits)
	}
	if !res.TreasuryShort {
		t.Fatalf("expected treasury short (200e18 < 350e18)")
	}
	if res.ShortfallBaseUnits.Cmp(e18(150)) != 0 {
		t.Fatalf("shortfall = %s, want 150e18", res.ShortfallBaseUnits)
	}
}

// TestReconcile_DriftWhenLedgerBelowChain: a published cumulative BELOW on-chain
// claimed (the regression the exclusion-gap bug would cause) is flagged as drift.
func TestReconcile_DriftWhenLedgerBelowChain(t *testing.T) {
	db := setupMerkleTestDB(t)
	mkMerkleUser(t, db, walletA, 100) // published 100e18
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	buildPublishedEpoch(t, svc)

	e18 := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), baseUnitScale()) }
	reader := &fakeRewardChainReader{
		// On-chain already paid 120 but the ledger only published 100 — impossible
		// under a correct monotonic clamp; the reconciler must catch it.
		claimed: map[string]*big.Int{strings.ToLower(walletA): e18(120)},
		balance: e18(1000),
	}

	res, err := svc.Reconcile(context.Background(), reader, "0xmcgp")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.DriftCount != 1 {
		t.Fatalf("drift = %d, want 1 (ledger 100e18 < claimed 120e18)", res.DriftCount)
	}
	// Outstanding clamps the negative account at 0 (no double-count of a paid-over
	// account into the treasury need).
	if res.OutstandingBaseUnits.Sign() != 0 {
		t.Fatalf("outstanding = %s, want 0 (over-claimed account clamped)", res.OutstandingBaseUnits)
	}
}

// TestReconcile_NoPublishedEpoch_NoOp: with only a pending (unconfirmed) epoch,
// the reconciler is a clean no-op (epoch 0, no reads against a non-live root).
func TestReconcile_NoPublishedEpoch_NoOp(t *testing.T) {
	db := setupMerkleTestDB(t)
	mkMerkleUser(t, db, walletA, 100)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	// Build but DO NOT publish.
	if _, err := svc.BuildDistribution(context.Background(), nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	reader := &fakeRewardChainReader{claimed: map[string]*big.Int{}, balance: big.NewInt(0)}

	res, err := svc.Reconcile(context.Background(), reader, "0xmcgp")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Epoch != 0 || res.Accounts != 0 || res.DriftCount != 0 || res.TreasuryShort {
		t.Fatalf("expected clean no-op for an unpublished epoch, got %+v", res)
	}
}

// TestReconcile_DormantWhenUnconfigured: no distributor / no reader → no-op.
func TestReconcile_DormantWhenUnconfigured(t *testing.T) {
	db := setupMerkleTestDB(t)
	svc := NewRewardMerkleService(db, &fakeEligibility{}, "", 146) // dormant
	res, err := svc.Reconcile(context.Background(), &fakeRewardChainReader{}, "0xmcgp")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Epoch != 0 || res.TreasuryShort {
		t.Fatalf("dormant service should no-op, got %+v", res)
	}

	// Configured but nil reader is also a no-op (not wired).
	svc2 := NewRewardMerkleService(db, &fakeEligibility{}, "0xdist0000000000000000000000000000000000a1", 146)
	res2, err := svc2.Reconcile(context.Background(), nil, "0xmcgp")
	if err != nil {
		t.Fatalf("reconcile (nil reader): %v", err)
	}
	if res2.Epoch != 0 {
		t.Fatalf("nil reader should no-op, got %+v", res2)
	}
}
