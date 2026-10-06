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
)

// Review Focus 2 / S4: two opens racing for the last stock — exactly one wins.
// Only PostgreSQL has the advisory lock, so only PostgreSQL proves it.
func TestInsertTreasuryIntent_ConcurrentOpensForTheLastStock_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "it_treasury_lock_", &models.ITIntent{})
	s := NewITService(db, nil, stubFx{ngn: 1500})
	tr, _, native, _ := newTestTreasuryWithFakes(t)
	native.tokenBalances = map[string]*big.Int{strings.ToLower(sponsorTestUSDC): big.NewInt(7_232_937)} // one order's worth
	s.SetCompanyWallet(NewCompanyWallet(CompanyWalletConfig{Treasury: tr, Decimals: map[string]map[string]int{"base": {"USDC": 6}}}))

	order := func() *models.ITIntent {
		until := time.Now().Add(15 * time.Minute)
		return &models.ITIntent{
			ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
			Provider: models.ITProviderTreasury, Asset: "USDC", AssetAmount: "7232937", Currency: "NGN",
			DestChain: "BASE", DestAddress: testRoutedEVM, LockedRate: "138257", DisplayRate: "138257", RateExpiresAt: &until,
			NgnKobo: "1000000", AmountMinor: "1000000", RateKobo: "138257", RateMinor: "138257",
			McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1000000", PlatformKeptKobo: "0", ReceivedAmount: "0",
		}
	}
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
