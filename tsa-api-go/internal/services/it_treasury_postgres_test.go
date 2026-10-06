package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

// Review Focus 2 / S4: two opens racing for the last stock — exactly one wins.
// Only PostgreSQL has the advisory lock, so only PostgreSQL proves it.
func TestInsertTreasuryIntent_ConcurrentOpensForTheLastStock_Postgres(t *testing.T) {
	db, s := newLockedStockService(t, 7_232_937) // one order's worth
	order := newUSDCOrder
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.insertTreasuryIntent(context.Background(), order())
		}(i)
	}
	close(start)
	wg.Wait()
	won, short := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrTreasuryStockShort):
			short++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	var n int64
	db.Model(&models.ITIntent{}).Count(&n)
	if won != 1 || short != 1 || n != 1 {
		t.Fatalf("won=%d short=%d rows=%d, want exactly one order for the last stock", won, short, n)
	}
}

// newLockedStockService is a Postgres-backed service with a company wallet
// holding stockUnits of USDC on Base. Production always has the conversion
// records table: live pricing reads it for the converted ceiling (N4).
func newLockedStockService(t *testing.T, stockUnits int64) (*gorm.DB, *ITService) {
	t.Helper()
	db := testutil.TempPostgres(t, "it_treasury_lock_", &models.ITIntent{}, &models.PaycrestOrderRecord{})
	s := NewITService(db, nil, stubFx{ngn: 1500})
	tr, _, native, _ := newTestTreasuryWithFakes(t)
	native.tokenBalances = map[string]*big.Int{strings.ToLower(sponsorTestUSDC): big.NewInt(stockUnits)}
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{Treasury: tr, Decimals: map[string]map[string]int{"base": {"USDC": 6}}}))
	return db, s
}

func newUSDCOrder() *models.ITIntent {
	until := time.Now().Add(15 * time.Minute)
	return &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
		Provider: models.ITProviderTreasury, Asset: "USDC", AssetAmount: "7232937", Currency: "NGN",
		DestChain: "BASE", DestAddress: testRoutedEVM, LockedRate: "138257", DisplayRate: "138257", RateExpiresAt: &until,
		NgnKobo: "1000000", AmountMinor: "1000000", RateKobo: "138257", RateMinor: "138257",
		McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1000000", PlatformKeptKobo: "0", ReceivedAmount: "0",
	}
}

// holdLock takes the advisory lock for key in an open transaction and returns
// the function that releases it.
func holdLock(t *testing.T, db *gorm.DB, key string) (release func()) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
		t.Fatal(err)
	}
	return func() { tx.Commit() }
}

func startOpen(s *ITService) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.insertTreasuryIntent(context.Background(), newUSDCOrder()) }()
	return done
}

// Review Focus 2, deterministically: an open for (base, USDC) waits while
// another transaction holds that exact lock, and is not blocked by a lock on
// another token.
func TestInsertTreasuryIntent_WaitsOnItsOwnStockLockOnly_Postgres(t *testing.T) {
	db, s := newLockedStockService(t, 50_000_000)

	release := holdLock(t, db, "ramp_treasury:base:USDC")
	done := startOpen(s)
	select {
	case err := <-done:
		release()
		t.Fatalf("open returned while the stock lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("open after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open did not proceed after the lock was released")
	}

	releaseOther := holdLock(t, db, "ramp_treasury:base:USDT")
	defer releaseOther()
	select {
	case err := <-startOpen(s):
		if err != nil {
			t.Fatalf("open with another token's lock held: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lock on another token blocked a USDC open")
	}
}

// Final review F3 (carried from Task 8): a live-priced company-wallet claim
// reads the free stock and writes its new amount inside the same (chain,
// token) lock an open takes, so a claim and an open can never both spend the
// last stock. Deterministically: the claim waits while another transaction
// holds that lock, and completes once it is released.
func TestClaimLivePricedTreasuryBuy_WaitsOnTheStockLock_Postgres(t *testing.T) {
	db, s := newLockedStockService(t, 50_000_000)
	s.SetTreasuryMarketRate(&fixedTreasuryRate{rate: treasuryMarket})
	candidate := newUSDCOrder()
	if err := db.Create(candidate).Error; err != nil {
		t.Fatal(err)
	}

	release := holdLock(t, db, "ramp_treasury:base:USDC")
	done := make(chan error, 1)
	go func() {
		// ₦9,000 against a ₦10,000 quote: priced live, so it needs the stock.
		claimed, err := s.claimAndPriceStableBuy(context.Background(), candidate, "chg_pg_live", "900000", time.Now(), true)
		if err == nil && !claimed {
			err = errors.New("the claim was lost")
		}
		done <- err
	}()
	select {
	case err := <-done:
		release()
		t.Fatalf("the live-priced claim returned while the stock lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("claim after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the claim did not proceed after the lock was released")
	}
	var got models.ITIntent
	if err := db.First(&got, "id = ?", candidate.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != models.ITStatusPaymentReceived || !got.Repriced || got.AssetAmount == candidate.AssetAmount {
		t.Fatalf("status=%s repriced=%v asset=%s, want claimed at the live price", got.Status, got.Repriced, got.AssetAmount)
	}
}

// Review Focus 5: two converted opens for the last stock — one intent, one record.
func TestOpenTreasuryConvertedBuy_ConcurrentOpensForTheLastStock_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "it_treasury_conv_lock_", &models.ITIntent{}, &models.PaycrestOrderRecord{})
	s := NewITService(db, nil, stubFx{ngn: 1500})
	tr, _, native, _ := newTestTreasuryWithFakes(t)
	native.tokenBalances = map[string]*big.Int{strings.ToLower(sponsorTestUSDC): big.NewInt(7_232_937)}
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{Treasury: tr, Decimals: map[string]map[string]int{"base": {"USDC": 6}}}))
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			order := newUSDCOrder()
			errs[i] = s.insertTreasuryIntentWith(context.Background(), order, func(tx *gorm.DB, intent *models.ITIntent) error {
				return tx.Create(newConversionRecord(intent, ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM},
					models.HolderCompanyWallet)).Error
			})
		}(i)
	}
	close(start)
	wg.Wait()
	var intents, records int64
	db.Model(&models.ITIntent{}).Count(&intents)
	db.Model(&models.PaycrestOrderRecord{}).Count(&records)
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrTreasuryStockShort) {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if won != 1 || intents != 1 || records != 1 {
		t.Fatalf("won=%d intents=%d records=%d, want exactly one of each", won, intents, records)
	}
}

// Review Focus 3 on the real database: a refused handoff rolls the leg's
// confirmation and the settle back with it; the next tick hands off and
// settles together.
func TestSettleTreasuryBuy_HandoffCommitsOrRollsBackWithTheSettle_Postgres(t *testing.T) {
	ctx := context.Background()
	s, intent, _ := newDeliverPostgres(t)
	rec := newConversionRecord(intent, ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM},
		models.HolderCompanyWallet)
	rec.Status = models.PaycrestStatusRecoveryPending // an operator took it
	if err := s.db.Create(rec).Error; err != nil {
		t.Fatal(err)
	}
	leg := &models.RampLeg{OrderID: intent.ID, Kind: models.RampLegKindSale, Attempt: 1, ChainKey: "base", Token: "USDC",
		FromAddr: s.wallet.Address(), ToAddr: intent.DestAddress, Amount: "7232937", TxHash: "0xsale",
		Status: models.RampLegStatusPending, CreatedAt: time.Now().UTC()}
	if err := s.db.Create(leg).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.settleTreasuryBuy(ctx, intent, leg, "7232937"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("settle over a taken record: %v", err)
	}
	var gotLeg models.RampLeg
	s.db.First(&gotLeg, "id = ?", leg.ID)
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusDelivering || gotLeg.Status != models.RampLegStatusPending {
		t.Fatalf("intent=%s leg=%s, want both untouched", got.Status, gotLeg.Status)
	}
	s.db.Model(rec).Update("status", models.PaycrestStatusPending)
	if err := s.settleTreasuryBuy(ctx, intent, leg, "7232937"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	s.db.First(&gotLeg, "id = ?", leg.ID)
	got, after := reloadIntent(t, s, intent.ID), conversionRecord(t, s, intent.ID)
	if got.Status != models.ITStatusSettled || gotLeg.Status != models.RampLegStatusConfirmed ||
		after.Status != models.PaycrestStatusConverting || after.TxHash != "0xsale" || after.QuotedAmount != "7196772" {
		t.Fatalf("intent=%s leg=%s record=%s %s %s", got.Status, gotLeg.Status, after.Status, after.TxHash, after.QuotedAmount)
	}
}
