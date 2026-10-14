package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The fill's pre-broadcast hook and a refund serialize on the intent's row
// lock (PostgreSQL): a refund that holds the lock and moves the order wins,
// and the hook — blocked until it commits — then records nothing, so the
// allocation rolls back and no fill is signed.
func TestVaultFillHook_WaitsForARefundHoldingTheRowPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "pool_fill_hook_", &models.ITIntent{}, &models.PoolOrder{})
	id := uuid.New()
	fill := "1000"
	if err := db.Create(&models.ITIntent{ID: id, UserID: uuid.New(), Direction: models.ITDirectionBuy, Provider: models.ITProviderPool,
		Asset: "USDT", Currency: "NGN", Status: models.ITStatusDelivering, AssetAmount: "1",
		McgpAmount: "0", NgnKobo: "0", RateKobo: "0", NetNgnKobo: "0", NetMinor: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.PoolOrder{IntentID: id, Generation: 1, ChainID: poolTestChain, Token: "USDT", TokenDecimals: 18,
		MidMinor: "1", LPRateMinor: "1", DisplayRateMinor: "1", SliceCount: 1, OpenedUnits: "1", SurplusUnits: "0", FillUnits: &fill}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	refund := db.WithContext(ctx).Begin()
	defer refund.Rollback() // a failed assertion must never leave the lock held (the schema drop would wait)
	var locked models.ITIntent
	if err := refund.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	hookErr := make(chan error, 1)
	go func() {
		hookErr <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return recordVaultFill(tx, id, "0xabc", "0")
		})
	}()
	select {
	case err := <-hookErr:
		refund.Rollback()
		t.Fatalf("the hook did not wait for the refund's row lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := refund.Model(&models.ITIntent{}).Where("id = ?", id).Update("status", models.ITStatusRefunded).Error; err != nil {
		t.Fatal(err)
	}
	if err := refund.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-hookErr; err == nil {
		t.Fatal("the hook recorded a fill for a refunded order")
	}
	var o models.PoolOrder
	if err := db.First(&o, "intent_id = ?", id).Error; err != nil || o.FillTxHash != "" {
		t.Fatalf("fill hash = %q %v, want none", o.FillTxHash, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return recordVaultFill(tx, uuid.New(), "0xabc", "0") }); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unknown intent: %v", err)
	}
}

// The new ledger and ad SQL on PostgreSQL: the indexed DepositorSet replay
// (latest wins) and the public ad read's NOT EXISTS on uuid columns.
func TestVaultLP_LedgerAndAdSQLPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "pool_sql_", &models.PaymentMerchant{}, &models.LiquidityLedgerEntry{}, &models.P2PAd{})
	ctx := context.Background()
	svc, err := NewVaultLPService(VaultLPConfig{DB: db, PM: NewPMService(db), Vault: newFakeLPVault(), Tokens: &fakeAllowances{allowance: new(big.Int)},
		Sender: &lpSender{}, Token: PoolToken{ChainID: poolTestChain, ChainKey: "bsc", Symbol: "USDT", Address: poolTestUSDT, Decimals: 18}})
	if err != nil {
		t.Fatal(err)
	}
	wallet := newLPWallet()
	for i, allowed := range []bool{true, false} {
		a := allowed
		if err := db.Create(&models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: poolTestChain, Vault: strings.ToLower(testVaultAddr.Hex()),
			TxHash: fmt.Sprintf("0x%064x", i+1), Block: uint64(10 + i), Kind: VaultEventDepositorSet, Account: wallet, Allowed: &a, BlockTime: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.indexedDepositors(ctx)
	if v, ok := got[strings.ToLower(wallet)]; err != nil || !ok || v {
		t.Fatalf("indexed = %v %v, want the latest (revoked)", got, err)
	}
	chain := int64(poolTestChain)
	lp := &models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "lp", Status: models.PaymentMerchantStatusActive,
		Kind: models.PaymentMerchantKindLP, PoolChainID: &chain, OperatorWalletAddress: wallet}
	if err := db.Create(lp).Error; err != nil {
		t.Fatal(err)
	}
	p2p := &P2PTradeService{db: db, pm: NewPMService(db)}
	q, err := p2p.excludePoolAds(ctx, db.Model(&models.P2PAd{}))
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("excludePoolAds on PostgreSQL: %v", err)
	}
}
