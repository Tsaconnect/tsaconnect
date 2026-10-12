package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	poolTestChain  int64 = 97
	poolTestUSDT         = "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7" // Plan A Task 12 mock, 18 dp
	poolTestEscrow       = "0x2B19e2e2194880A1a8AF9135923e3AeE0bD1adE5"
)

// usdt18 is whole × 10^18 (the pool token's base units).
func usdt18(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

// poolTestRegistry: Sonic 146 (ordinary, USDT 6 dp) and BSC testnet 97 (pool,
// USDT 18 dp), each with its own mock escrow.
func poolTestRegistry() (*P2PEscrowRegistry, *mockEscrow, *mockEscrow) {
	sonic, pool := newMockEscrow(), newMockEscrow()
	pool.contractAddress = poolTestEscrow
	return NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146:           {Escrow: sonic, ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 6}},
		poolTestChain: {Escrow: pool, ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2"), sonic, pool
}

func poolTestTokenAddr(chainID int64, sym string) string {
	switch {
	case chainID == 146 && sym == "USDT":
		return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
	case chainID == poolTestChain && sym == "USDT":
		return poolTestUSDT
	}
	return ""
}

func TestRegistry_PoolChainInfo(t *testing.T) {
	reg, _, _ := poolTestRegistry()
	p, ok := reg.PoolChain()
	if !ok || p.ChainID != poolTestChain || p.ChainKey != "bsc" || !p.HasAsset("usdt") || p.HasAsset("USDC") || p.Decimals["USDT"] != 18 {
		t.Fatalf("pool chain = %+v %v", p, ok)
	}
	plain := NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: newMockEscrow()}, "2")
	if _, ok := plain.PoolChain(); ok {
		t.Fatal("a registry without a pool row has no pool chain (production today)")
	}
}

// Roadmap risk 2 / Review Focus 2: stock above the max is invisible to the pool.
func TestPoolStock_StakeCappedAtLPMax(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil // limits $5 / $5,000

	stake, above := lpStake(usdt18(5800), usdt18(200), usdt18(5000))
	if stake.Cmp(usdt18(5000)) != 0 || above.Cmp(usdt18(1000)) != 0 {
		t.Fatalf("stake=%s above=%s, want 5000/1000 (locked counts toward the max)", stake, above)
	}
	stake, above = lpStake(usdt18(40), usdt18(0), usdt18(5000))
	if stake.Cmp(usdt18(40)) != 0 || above.Sign() != 0 {
		t.Fatalf("under the max: stake=%s above=%s", stake, above)
	}

	db := newSQLiteForPM(t)
	reg, _, pool := poolTestRegistry()
	pool.balance = OnChainBalance{Available: usdt18(5800), Locked: usdt18(200)}
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000c1")
	pos, err := NewPoolStock(reg, poolTestTokenAddr).Position(context.Background(), lp, true)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if pos.Stake.Cmp(usdt18(5000)) != 0 || pos.AboveMax.Cmp(usdt18(1000)) != 0 || pos.Max.Cmp(usdt18(5000)) != 0 ||
		pos.Min.Cmp(usdt18(5)) != 0 || pos.Token.Decimals != 18 || pos.Token.Address != poolTestUSDT {
		t.Fatalf("position = %+v", pos)
	}
}

func TestPoolStock_PositionRefusesNonLPOrWrongChain(t *testing.T) {
	db := newSQLiteForPM(t)
	reg, _, _ := poolTestRegistry()
	stock := NewPoolStock(reg, poolTestTokenAddr)
	merchant := seedPMRow(t, db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000c2")
	if _, err := stock.Position(context.Background(), merchant, false); !errors.Is(err, ErrNotPoolLP) {
		t.Fatalf("merchant: %v", err)
	}
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000c3")
	other := int64(56)
	lp.PoolChainID = &other
	if _, err := stock.Position(context.Background(), lp, false); !errors.Is(err, ErrNotPoolLP) {
		t.Fatalf("LP on another chain: %v", err)
	}
}

func TestPoolStock_CachedThenFresh(t *testing.T) {
	db := newSQLiteForPM(t)
	reg, _, pool := poolTestRegistry()
	stock := NewPoolStock(reg, poolTestTokenAddr)
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	stock.now = func() time.Time { return clock }
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000c4")
	pool.balance = OnChainBalance{Available: usdt18(10), Locked: usdt18(0)}
	ctx := context.Background()
	if p, _ := stock.Position(ctx, lp, false); p.Available.Cmp(usdt18(10)) != 0 {
		t.Fatal("first read")
	}
	pool.balance = OnChainBalance{Available: usdt18(3), Locked: usdt18(0)} // the LP withdrew
	if p, _ := stock.Position(ctx, lp, false); p.Available.Cmp(usdt18(10)) != 0 {
		t.Fatal("public reads go through the 30s cache")
	}
	if p, _ := stock.Position(ctx, lp, true); p.Available.Cmp(usdt18(3)) != 0 {
		t.Fatal("fresh reads ask the chain")
	}
}
