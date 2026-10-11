package services

import (
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolEscrowFake is a pool escrow with one balance per LP wallet, cancelled
// vouchers, and a read counter (the 30 s cache must be observable).
type poolEscrowFake struct {
	*mockEscrow
	mu        sync.Mutex
	balances  map[string]OnChainBalance // lowercase wallet → balance (chain units)
	cancelled map[[32]byte]bool
	reads     int
}

func newPoolEscrowFake() *poolEscrowFake {
	m := newMockEscrow()
	m.contractAddress = poolTestEscrow
	return &poolEscrowFake{mockEscrow: m, balances: map[string]OnChainBalance{}, cancelled: map[[32]byte]bool{}}
}

func (f *poolEscrowFake) ReadBalance(seller, _ string) (*OnChainBalance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	b, ok := f.balances[strings.ToLower(seller)]
	if !ok {
		return &OnChainBalance{Available: new(big.Int), Locked: new(big.Int)}, nil
	}
	return &OnChainBalance{Available: new(big.Int).Set(b.Available), Locked: new(big.Int).Set(b.Locked)}, nil
}

func (f *poolEscrowFake) ReadVoucherCancelled(h [32]byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled[h], nil
}

func (f *poolEscrowFake) setBalance(wallet string, avail, locked *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balances[strings.ToLower(wallet)] = OnChainBalance{Available: avail, Locked: locked}
}

// deskRig is a pool desk on BSC testnet (97, USDT 18 dp): enough for free
// stock and allocation. Task 10 builds its open rig on it with a chain fake.
type deskRig struct {
	db     *gorm.DB
	esc    *poolEscrowFake  // balances and cancelled vouchers
	escrow P2PEscrowAdapter // what the registry and desk call (esc, or a fake wrapping it)
	trades *P2PTradeService
	desk   *PoolDesk
}

func newDeskRig(t *testing.T) *deskRig {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	return buildDeskRig(t, db, newPoolEscrowFake(), nil)
}

// buildDeskRig wires a desk over db (SQLite raw DDL, or a temp Postgres with
// the models migrated). adapter, when non-nil, is the escrow the registry and
// the desk call; it must read balances from esc.
func buildDeskRig(t *testing.T, db *gorm.DB, esc *poolEscrowFake, adapter P2PEscrowAdapter) *deskRig {
	t.Helper()
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil // LP limits $5 / $5,000
	if adapter == nil {
		adapter = esc
	}
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		poolTestChain: {Escrow: adapter, ChainKey: "bsc", Purpose: config.P2PPurposePool, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 18}},
	}, "2")
	trades := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	trades.SetTokenResolver(poolTestTokenAddr)
	trades.SetPMService(NewPMService(db))
	stock := NewPoolStock(reg, poolTestTokenAddr)
	tok, ok := stock.Token()
	if !ok {
		t.Fatal("no pool token")
	}
	return &deskRig{db: db, esc: esc, escrow: adapter, trades: trades, desk: &PoolDesk{
		c:      PoolDeskConfig{DB: db, PM: NewPMService(db), Trades: trades, Stock: stock, Token: tok, Escrow: adapter, EscrowAddr: poolTestEscrow, Now: time.Now},
		totals: newCachedReader[*big.Int](poolTotalsTTL, poolBalanceFailTTL),
	}}
}

// addLP is an active LP with a signed, active stock ad (voucher 500,000 USDT,
// 90 days) and freeWhole USDT available in escrow.
func (r *deskRig) addLP(t *testing.T, freeWhole int64) (*models.PaymentMerchant, *models.P2PAd) {
	t.Helper()
	id := uuid.New()
	wallet := common.BytesToAddress(id[:]).Hex()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, wallet)
	ad := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", usdt18(500_000).String())
	deadline := time.Now().Add(90 * 24 * time.Hour)
	hex32 := func() string {
		u := uuid.New()
		return "0x" + strings.Repeat("0", 32) + strings.ReplaceAll(u.String(), "-", "")
	}
	r.db.Model(ad).Updates(map[string]any{
		"seller_address": wallet, "voucher_deadline": deadline,
		"voucher_nonce": hex32(), "voucher_hash": hex32(), // unique per ad: one cancelled voucher is one ad
		"voucher_signature": "0x" + strings.Repeat("11", 65),
	})
	r.db.First(ad, "id = ?", ad.ID)
	r.esc.setBalance(wallet, usdt18(freeWhole), new(big.Int))
	return lp, ad
}

// canonical is whole × 10^6.
func canonical(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), big.NewInt(1_000_000))
}
