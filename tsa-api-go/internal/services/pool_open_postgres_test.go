package services

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

func newPoolOpenRigPG(t *testing.T) *poolOpenRig {
	t.Helper()
	db := testutil.TempPostgres(t, "liq_c1_open_", &models.PaymentMerchant{}, &models.P2PAd{}, &models.P2PTrade{},
		&models.P2PAuditLog{}, &models.ITIntent{}, &models.PoolOrder{}, &models.PaycrestOrderRecord{})
	if err := config.MigratePoolSliceIndexes(db); err != nil {
		t.Fatal(err)
	}
	if err := config.MigrateOpenStableBuyIndex(db); err != nil {
		t.Fatal(err)
	}
	return buildPoolOpenRig(t, db)
}

func poolIntentFor(p *PoolPrice) *models.ITIntent {
	return &models.ITIntent{ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionBuy, Status: models.ITStatusOpening,
		Provider: models.ITProviderPool, Asset: "USDT", AssetAmount: p.Quote.AssetUnits.String(), Currency: "NGN", DestChain: "BSC",
		DestAddress: testRoutedEVM, LockedRate: p.Quote.RatePerUnitMinor.String(), DisplayRate: p.Quote.DisplayRateMinor.String(),
		NgnKobo: "500000", AmountMinor: "500000", RateKobo: "1", RateMinor: "1", McgpAmount: "0", FeeNgnKobo: "0",
		NetNgnKobo: "500000", PlatformKeptKobo: "0", ReceivedAmount: "0"}
}

// Roadmap risk 5: 20 concurrent opens against two LPs never promise more than
// either holds.
func TestPoolOpen_ConcurrentOpensNeverOverAllocate_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	a, _ := r.addLP(t, 30)
	b, _ := r.addLP(t, 30)
	p := r.price(t, "5000") // 3.62098 each
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var out []models.P2PTrade
			_ = r.desk.insertOrder(context.Background(), poolIntentFor(p), p, nil, &out)
		}()
	}
	close(start)
	wg.Wait()
	for _, lp := range []*models.PaymentMerchant{a, b} {
		var amounts []string
		r.db.Table("p2p_trades").Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
			Where("p2p_ads.pm_id = ? AND p2p_trades.it_intent_id IS NOT NULL", lp.ID).Pluck("p2p_trades.token_amount", &amounts)
		sum := new(big.Int)
		for _, s := range amounts {
			v, _ := new(big.Int).SetString(s, 10)
			sum.Add(sum, v)
		}
		if sum.Cmp(usdt18(30)) > 0 {
			t.Fatalf("LP %s promised %s > its 30 USDT", lp.ID, sum)
		}
	}
}

// The ad's remaining is debited with the guarded CAS under the lock: of two
// opens for the last capacity, one wins.
func TestPoolOpen_AdRemainingCASUnderLock_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	_, ad := r.addLP(t, 100)
	p := r.price(t, "5000")
	chainUnits, _ := CanonicalToChainUnits(p.Quote.AssetUnits, 18)
	r.db.Model(ad).Update("remaining", chainUnits.String()) // capacity for exactly one
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out []models.P2PTrade
			errs[i] = r.desk.insertOrder(context.Background(), poolIntentFor(p), p, nil, &out)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrPoolCannotFill):
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("won = %d, want exactly one", won)
	}
}

// S10: the system opener is never limited per ad (two orders, one LP).
// Postgres only (review I6): the second order re-reads the ad's remaining
// after the first one's CAS debit, and SQLite turns that 18-dp arithmetic
// above int64 into a REAL ("4.99e+23") the allocator cannot parse.
func TestPoolSlices_SystemOpenerNotLimitedPerAd_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	r.addLP(t, 50)
	p := r.price(t, "5000")
	for i := 0; i < 2; i++ {
		var out []models.P2PTrade
		if err := r.desk.insertOrder(context.Background(), poolIntentFor(p), p, nil, &out); err != nil {
			t.Fatalf("order %d on the same LP ad: %v", i+1, err)
		}
	}
	var n int64
	r.db.Model(&models.P2PTrade{}).Where("it_intent_id IS NOT NULL AND opener_id IS NULL").Count(&n)
	if n != 2 {
		t.Fatalf("slices with a NULL opener = %d", n)
	}
}

// Pre-merge review I-1 (2) on Postgres: the failed-open clause of the pool's
// daily cap (an EXISTS over the order's slices) is valid SQL there and counts
// only a failed open whose slice was sent, never one reclaimed unsent.
func TestPoolDailyCap_CountsFailedOpensThatSentSlices_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	ctx := context.Background()
	set := specPoolSettings()
	user := uuid.New()
	failedOpen := func(units string, nonce *int64, reason string) {
		t.Helper()
		in := &models.ITIntent{ID: uuid.New(), UserID: user, Direction: models.ITDirectionBuy, Status: models.ITStatusCancelled,
			FailureReason: models.ITFailurePoolOpenFailed, Provider: models.ITProviderPool, Asset: "USDT", AssetAmount: units,
			Currency: "NGN", DestChain: "BSC", DestAddress: testRoutedEVM, LockedRate: "1", DisplayRate: "1", NgnKobo: "1",
			AmountMinor: "1", RateKobo: "1", RateMinor: "1", McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1",
			PlatformKeptKobo: "0", ReceivedAmount: "0"}
		if err := r.db.Create(in).Error; err != nil {
			t.Fatal(err)
		}
		id := in.ID
		sl := models.P2PTrade{ID: uuid.New(), AdID: uuid.New(), BuyerID: r.buyer, SellerID: uuid.New(), Token: "USDT", ChainID: 97,
			Funding: models.P2PTradeFundingPooled, TokenAmount: "1", FiatAmount: "1", FiatCurrency: "NGN", Rate: "1",
			Status: models.P2PTradeStatusCancelled, CancelReason: reason, ITIntentID: &id, OpenNonce: nonce}
		sl.OnChainTradeID = HashIDHex(sl.ID)
		if err := r.db.Create(&sl).Error; err != nil {
			t.Fatal(err)
		}
	}
	n := int64(4)
	failedOpen("500000000", &n, models.ITFailurePoolOpenFailed) // reclaimed unsent: not counted
	failedOpen("600000000", nil, "")                            // never reached a nonce: not counted
	if err := r.it.CheckPoolDailyCap(ctx, user, set.UserDailyMaxUnits, set); err != nil {
		t.Fatalf("unsent failed opens counted: %v", err)
	}
	failedOpen("700000000", &n, "") // sent
	if err := r.it.CheckPoolDailyCap(ctx, user, big.NewInt(1_300_000_001), set); !errors.Is(err, ErrPoolDailyCapReached) {
		t.Fatalf("700 sent + 1300.000001 = %v, want the cap reached", err)
	}
	if err := r.it.CheckPoolDailyCap(ctx, user, big.NewInt(1_300_000_000), set); err != nil {
		t.Fatalf("700 sent + 1300 = %v, want allowed", err)
	}
}
