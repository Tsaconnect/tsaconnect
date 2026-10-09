package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolRig: a trade service over Sonic (146) and the BSC testnet pool (97,
// USDT 18 dp), a PM service, and the pool open. Fees is nil: limits $5/$5,000.
type poolRig struct {
	db          *gorm.DB
	svc         *P2PTradeService
	sonic, pool *mockEscrow
	bus         *mockBus
}

func newPoolRig(t *testing.T) *poolRig {
	t.Helper()
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	reg, sonic, pool := poolTestRegistry()
	bus := &mockBus{}
	svc := NewP2PTradeService(db, reg, &mockRates{}, bus, 146).
		WithClock(fixedClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(poolTestTokenAddr)
	svc.SetPMService(NewPMService(db))
	svc.SetPoolGate(fakePoolOpen(true))
	return &poolRig{db: db, svc: svc, sonic: sonic, pool: pool, bus: bus}
}

func (r *poolRig) deposit(lp *models.PaymentMerchant, amount *big.Int) error {
	_, err := r.svc.PrepareDeposit(context.Background(), lp.UserID, lp.OperatorWalletAddress, "USDT", amount.String(), poolTestChain)
	return err
}

const lpWallet = "0x00000000000000000000000000000000000000d1"

// Review Focus 3: only an active LP reaches the pool chain.
func TestPoolChain_NonLPRefused(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	for name, who := range map[string]*models.PaymentMerchant{
		"approved LP": seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusApproved, lpWallet),
		"paused LP":   seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, lpWallet),
		"merchant":    seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, lpWallet),
		"no row":      {UserID: uuid.New(), OperatorWalletAddress: lpWallet},
	} {
		if err := r.deposit(who, usdt18(10)); !errors.Is(err, ErrAssetNotTradeable) {
			t.Errorf("%s deposit: %v, want ErrAssetNotTradeable", name, err)
		}
		if _, err := r.svc.PrepareApprove(ctx, who.UserID, lpWallet, "USDT", usdt18(10).String(), poolTestChain); !errors.Is(err, ErrAssetNotTradeable) {
			t.Errorf("%s approve: %v", name, err)
		}
	}
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000d2")
	if _, err := r.svc.PrepareDeposit(ctx, lp.UserID, lp.OperatorWalletAddress, "USDC", "1", poolTestChain); !errors.Is(err, ErrAssetNotTradeable) && !errors.Is(err, ErrUnsupportedToken) {
		t.Errorf("non-pool token: %v", err)
	}
}

// Roadmap risk 2 / Review Focus 2: the max counts locked stock, on a fresh read.
func TestLPDeposit_AboveMaxRefusedOnFreshRead(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.balance = OnChainBalance{Available: usdt18(4000), Locked: usdt18(900)}

	err := r.deposit(lp, usdt18(200))
	var lim *LiquidityLimitError
	if !errors.As(err, &lim) || lim.Code != LiquidityAboveMaxCode || lim.Headroom.Cmp(usdt18(100)) != 0 || lim.Decimals != 18 {
		t.Fatalf("err = %v (%+v), want above max with 100 USDT headroom", err, lim)
	}
	if err := r.deposit(lp, usdt18(100)); err != nil {
		t.Fatalf("exactly to the max: %v", err)
	}
	// The chain moved (a slice released 900): the next decision reads it fresh.
	r.pool.balance = OnChainBalance{Available: usdt18(4000), Locked: usdt18(0)}
	if err := r.deposit(lp, usdt18(1000)); err != nil {
		t.Fatalf("after the lock freed: %v", err)
	}
	if got := r.pool.lastDepositArgs.Amount; got.Cmp(usdt18(1000)) != 0 {
		t.Fatalf("prepared amount = %s (18-dp base units, unscaled)", got)
	}
}

func TestLPDeposit_FirstBelowMinRefused(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	var lim *LiquidityLimitError
	if err := r.deposit(lp, usdt18(4)); !errors.As(err, &lim) || lim.Code != LiquidityBelowMinCode {
		t.Fatalf("first $4 deposit: %v", err)
	}
	if err := r.deposit(lp, usdt18(5)); err != nil {
		t.Fatalf("first $5 deposit: %v", err)
	}
	r.pool.balance = OnChainBalance{Available: usdt18(10), Locked: usdt18(0)}
	if err := r.deposit(lp, usdt18(1)); err != nil {
		t.Fatalf("a $1 top-up after the first deposit: %v", err)
	}
}

func TestLPDeposit_FromUnregisteredWalletRefused(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	_, err := r.svc.PrepareDeposit(context.Background(), lp.UserID, "0x00000000000000000000000000000000000000ee", "USDT", usdt18(10).String(), poolTestChain)
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("other wallet: %v, want ErrInvalidParams", err)
	}
}

func TestLPDeposit_RefusedWhilePoolClosed(t *testing.T) {
	r := newPoolRig(t)
	r.svc.SetPoolGate(fakePoolOpen(false))
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	if err := r.deposit(lp, usdt18(10)); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("closed pool: %v", err)
	}
	r.svc.SetPoolGate(nil)
	if err := r.deposit(lp, usdt18(10)); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("nil gate must read closed: %v", err)
	}
}

// Review Focus 4: the exits are never gated.
func TestLPWithdraw_AllowedWhilePoolClosedAndLPPaused(t *testing.T) {
	r := newPoolRig(t)
	r.svc.SetPoolGate(fakePoolOpen(false))
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, lpWallet)
	r.pool.balance = OnChainBalance{Available: usdt18(50), Locked: usdt18(20)}
	ctx := context.Background()
	if _, err := r.svc.PrepareWithdraw(ctx, lp.UserID, lpWallet, "USDT", usdt18(50).String(), poolTestChain); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	r.pool.withdrawnEvent = &P2PWithdrawnEvent{Amount: usdt18(50)}
	if _, err := r.svc.ConfirmWithdraw(ctx, lp.UserID, lpWallet, "USDT", usdt18(50).String(), "0xabc", poolTestChain); err != nil {
		t.Fatalf("confirm withdraw: %v", err)
	}
	r.pool.depositedEvent = &P2PDepositedEvent{Amount: usdt18(7)}
	if _, err := r.svc.ConfirmDeposit(ctx, lp.UserID, lpWallet, "USDT", usdt18(7).String(), "0xdef", poolTestChain); err != nil {
		t.Fatalf("confirming a deposit already on-chain: %v", err)
	}
	if b, err := r.svc.GetPoolBalance(ctx, lpWallet, "USDT", poolTestChain); err != nil || b.Locked.Cmp(usdt18(20)) != 0 {
		t.Fatalf("balance: %v %v", b, err)
	}
}

func TestChainsFor_PoolChainForLPNotOrdinaryUser(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	has := func(cs []P2PChainInfo) (P2PChainInfo, bool) {
		for _, c := range cs {
			if c.ChainID == poolTestChain {
				return c, true
			}
		}
		return P2PChainInfo{}, false
	}
	if _, ok := has(r.svc.ChainsFor(ctx, uuid.New())); ok {
		t.Fatal("an ordinary user sees the pool chain")
	}
	c, ok := has(r.svc.ChainsFor(ctx, lp.UserID))
	if !ok || c.Purpose != "pool" || c.Decimals["USDT"] != 18 {
		t.Fatalf("LP chains = %+v", c)
	}
}

// Review I1: exits are never gated, so a paused (or not yet active) LP is
// still offered the pool chain to read and withdraw its balance. New money
// stays active-only (TestPoolChain_NonLPRefused).
func TestChainsFor_PoolChainForAnyLPBoundToIt(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	onPool := func(cs []P2PChainInfo) bool {
		for _, c := range cs {
			if c.ChainID == poolTestChain && c.Purpose == config.P2PPurposePool {
				return true
			}
		}
		return false
	}
	for _, status := range []string{models.PaymentMerchantStatusPaused, models.PaymentMerchantStatusApproved, models.PaymentMerchantStatusRejected} {
		lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, status, lpWallet)
		if !onPool(r.svc.ChainsFor(ctx, lp.UserID)) {
			t.Errorf("%s LP is not offered the pool chain to withdraw from", status)
		}
	}
	merchant := seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusPaused, lpWallet)
	if onPool(r.svc.ChainsFor(ctx, merchant.UserID)) {
		t.Error("a payment merchant is offered the pool chain")
	}
	elsewhere := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, lpWallet)
	other := int64(56)
	r.db.Model(elsewhere).Update("pool_chain_id", other)
	if onPool(r.svc.ChainsFor(ctx, elsewhere.UserID)) {
		t.Error("an LP bound to another chain is offered this pool chain")
	}
}

func TestConfirmDeposit_AuditCarriesChainID(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.depositedEvent = &P2PDepositedEvent{Amount: usdt18(7)}
	if _, err := r.svc.ConfirmDeposit(context.Background(), lp.UserID, lpWallet, "USDT", usdt18(7).String(), "0xdef", poolTestChain); err != nil {
		t.Fatal(err)
	}
	var row models.P2PAuditLog
	r.db.Where("action = ?", models.P2PAuditDepositConfirmed).First(&row)
	var meta map[string]any
	_ = json.Unmarshal(row.Metadata, &meta)
	if meta["chainId"] != float64(poolTestChain) || meta["amount"] != usdt18(7).String() {
		t.Fatalf("audit metadata = %v", meta)
	}
}

// Money rule: a deposit made outside the app is never refused. It is recorded,
// the stake counts only up to the max, and the next in-app deposit has no
// headroom.
func TestLPDeposit_OutsideDepositAboveMaxOnlyCapsStake(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.balance = OnChainBalance{Available: usdt18(5500), Locked: usdt18(500)}
	r.pool.depositedEvent = &P2PDepositedEvent{Amount: usdt18(1000)}
	if _, err := r.svc.ConfirmDeposit(ctx, lp.UserID, lpWallet, "USDT", usdt18(1000).String(), "0xfee", poolTestChain); err != nil {
		t.Fatalf("an outside deposit must still be recorded: %v", err)
	}
	var lim *LiquidityLimitError
	if err := r.deposit(lp, big.NewInt(1)); !errors.As(err, &lim) || lim.Code != LiquidityAboveMaxCode || lim.Headroom.Sign() != 0 {
		t.Fatalf("in-app deposit above the max: %v (%+v), want zero headroom", err, lim)
	}
	pos, err := NewPoolStock(r.svc.escrows, poolTestTokenAddr).Position(ctx, lp, true)
	if err != nil {
		t.Fatal(err)
	}
	if pos.Stake.Cmp(usdt18(5000)) != 0 {
		t.Fatalf("stake = %s, want capped at the 5,000 USDT max", pos.Stake)
	}
}

// Money rule: only unlocked stock leaves. The contract enforces it; the API's
// pre-check refuses the locked part up front and allows the available part.
func TestLPWithdraw_OnlyAvailableStock(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.pool.balance = OnChainBalance{Available: usdt18(50), Locked: usdt18(20)}
	if _, err := r.svc.PrepareWithdraw(ctx, lp.UserID, lpWallet, "USDT", usdt18(51).String(), poolTestChain); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("withdrawing into locked stock: %v, want ErrInvalidParams", err)
	}
	if _, err := r.svc.PrepareWithdraw(ctx, lp.UserID, lpWallet, "USDT", usdt18(50).String(), poolTestChain); err != nil {
		t.Fatalf("withdrawing all available stock: %v", err)
	}
	if got := r.pool.lastWithdrawArgs.Amount; got.Cmp(usdt18(50)) != 0 {
		t.Fatalf("prepared withdraw = %s", got)
	}
}

// Money rule: ordinary P2P keeps Plan A's gate. On Sonic nobody needs an LP
// row or an open pool, no LP limit or wallet rule applies (an LP included),
// and a delisted asset is still refused.
func TestOrdinaryChain_ApproveAndDepositUnchangedByLPRoute(t *testing.T) {
	r := newPoolRig(t)
	r.svc.SetPoolGate(nil)
	r.svc.SetTokenResolver(func(chainID int64, sym string) string {
		if chainID == 146 && sym == "MCGP" { // resolvable, but not on Sonic's P2P list
			return "0x00000000000000000000000000000000000000c9"
		}
		return poolTestTokenAddr(chainID, sym)
	})
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	big6 := new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000)) // 1,000,000 USDT at 6 dp
	other := "0x00000000000000000000000000000000000000ee"
	for name, user := range map[string]uuid.UUID{"ordinary user": uuid.New(), "LP": lp.UserID} {
		if _, err := r.svc.PrepareApprove(ctx, user, other, "USDT", big6.String(), 146); err != nil {
			t.Errorf("%s approve on Sonic: %v", name, err)
		}
		if _, err := r.svc.PrepareDeposit(ctx, user, other, "USDT", big6.String(), 146); err != nil {
			t.Errorf("%s deposit on Sonic: %v", name, err)
		}
		if _, err := r.svc.PrepareDeposit(ctx, user, other, "MCGP", "1", 146); !errors.Is(err, ErrAssetNotTradeable) {
			t.Errorf("%s delisted asset on Sonic: %v", name, err)
		}
	}
	if r.pool.lastDepositArgs.Amount != nil {
		t.Fatal("a Sonic deposit reached the pool escrow")
	}
}

// Production today: no pool row in the registry. An active LP row cannot reach
// chain 97 and is never offered it.
func TestLPRoute_DormantWithoutPoolChain(t *testing.T) {
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	sonic := newMockEscrow()
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146: {Escrow: sonic, ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 6}},
	}, "2")
	svc := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	svc.SetTokenResolver(poolTestTokenAddr)
	svc.SetPMService(NewPMService(db))
	svc.SetPoolGate(fakePoolOpen(true))
	lp := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	ctx := context.Background()

	if _, err := svc.PrepareDeposit(ctx, lp.UserID, lpWallet, "USDT", usdt18(10).String(), poolTestChain); err == nil {
		t.Fatal("deposit on an unconfigured pool chain was prepared")
	}
	if _, err := svc.PrepareApprove(ctx, lp.UserID, lpWallet, "USDT", usdt18(10).String(), poolTestChain); err == nil {
		t.Fatal("approve on an unconfigured pool chain was prepared")
	}
	chains := svc.ChainsFor(ctx, lp.UserID)
	if len(chains) != 1 || chains[0].ChainID != 146 {
		t.Fatalf("chains = %+v, want Sonic only", chains)
	}
}

// The approve step takes the same LP route as the deposit: an active LP is
// let through on the pool chain, and refused while the pool is closed.
func TestLPApprove_ActiveLPOnPoolChainOnlyWhileOpen(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	if _, err := r.svc.PrepareApprove(ctx, lp.UserID, lpWallet, "USDT", usdt18(10).String(), poolTestChain); err != nil {
		t.Fatalf("active LP approve: %v", err)
	}
	r.svc.SetPoolGate(fakePoolOpen(false))
	if _, err := r.svc.PrepareApprove(ctx, lp.UserID, lpWallet, "USDT", usdt18(10).String(), poolTestChain); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("approve while closed: %v, want ErrPoolClosed", err)
	}
}
