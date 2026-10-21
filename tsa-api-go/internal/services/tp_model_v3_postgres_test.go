package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The TP model v3 lifecycle on real PostgreSQL (skipped without DATABASE_URL):
// a fee settles on a referral chain, a replay and a zero-fee event are handled,
// the monthly payout runs (twice at once), and every user's share lands. The
// SQLite tests cover the arithmetic; this covers what only Postgres does — the
// real DDL from AutoMigrate, the recursive upline query, row locks.
func TestTPModelV3_LifecycleOnPostgres(t *testing.T) {
	db := newITPostgresDB(t, &models.User{}, &models.TPEarning{}, &models.CashbackEarning{},
		&models.FeeDistribution{}, &models.GlobalTPPool{}, &models.GlobalTPAccrual{}, &models.GlobalTPEpoch{},
		&models.TPProgramConfig{}, &models.FeeSplitConfig{})
	ctx := context.Background()
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 100).
		WithTPProgram(NewTPProgramConfigService(db)).
		WithSplitConfigs(NewFeeSplitConfigService(db))

	chain := buildFeeChain(t, db, 3) // chain[0] transacts, chain[1] is the direct upline
	var bystanders []models.User
	for i := 0; i < 3; i++ {
		bystanders = append(bystanders, mkFeeUser(t, db, "by"+uuidShort(), nil, models.AccountStatusActive))
	}
	suspended := mkFeeUser(t, db, "sus"+uuidShort(), nil, models.AccountStatusSuspended)

	// 1. A $100 product fee: buyer 15 / upline 15 cashback, 70 kept, 12% of it TP.
	src := uuid.New()
	ev := FeeEvent{Feature: models.FeatureProductMerchant, Token: "USDT", FeeUSD: 100,
		UserID: chain[0].ID, SourceType: "checkout", SourceID: src}
	if err := svc.OnFeeSettled(ctx, ev); err != nil {
		t.Fatalf("settle: %v", err)
	}
	for name, tc := range map[string]struct{ got, want float64 }{
		"buyer TP (3.5 USD = 350 TP)": {tpBalance(t, db, chain[0].ID), 350},
		"upline TP":                   {tpBalance(t, db, chain[1].ID), 350},
		"grand-upline earns nothing":  {tpBalance(t, db, chain[2].ID), 0},
		"buyer cashback":              {cashbackBalance(t, db, chain[0].ID), 15},
		"upline cashback":             {cashbackBalance(t, db, chain[1].ID), 15},
	} {
		if !approx(tc.got, tc.want) {
			t.Fatalf("%s = %v, want %v", name, tc.got, tc.want)
		}
	}
	pool, _ := svc.GlobalPool(ctx)
	if !approx(pool.AccruedUSD, 1.4) || !approx(pool.AccruedTP, 140) {
		t.Fatalf("pool = %v USD / %v TP, want 1.4 / 140", pool.AccruedUSD, pool.AccruedTP)
	}

	// 2. Replaying the same source changes nothing.
	if err := svc.OnFeeSettled(ctx, ev); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := tpBalance(t, db, chain[0].ID); !approx(got, 350) {
		t.Fatalf("replay credited again: %v", got)
	}

	// 3. A zero-fee transaction: 0.001 TP to the user only, once.
	zero := FeeEvent{Feature: models.FeatureWithdrawal, Token: "MCGP", FeeUSD: 0,
		UserID: chain[0].ID, SourceType: "checkout", SourceID: uuid.New()}
	for i := 0; i < 2; i++ {
		if err := svc.OnFeeSettled(ctx, zero); err != nil {
			t.Fatalf("zero-fee: %v", err)
		}
	}
	if got := tpBalance(t, db, chain[0].ID); !approx(got, 350.001) {
		t.Fatalf("after zero-fee = %v, want 350.001", got)
	}
	if got := tpBalance(t, db, chain[1].ID); !approx(got, 350) {
		t.Fatalf("zero-fee reached the upline: %v", got)
	}

	// 4. Monthly payout. The first tick only stamps the window; the next one after
	// the payout day folds. Two instances ticking together must not double-pay.
	at := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 9, 0, 0, 0, PayoutLocation) }
	if res, err := svc.SettleGlobalPoolIfDue(ctx, at(time.October, 21)); err != nil || res.Settled {
		t.Fatalf("rollout tick: %+v %v", res, err)
	}
	var wg sync.WaitGroup
	results := make([]GlobalSettlementResult, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = svc.SettleGlobalPoolIfDue(ctx, at(time.October, 24))
		}(i)
	}
	wg.Wait()
	folded := 0
	for _, r := range results {
		if r.Settled {
			folded++
		}
	}
	if folded != 1 {
		t.Fatalf("two simultaneous ticks folded %d times, want exactly 1: %+v", folded, results)
	}
	var epochs int64
	db.Model(&models.GlobalTPEpoch{}).Count(&epochs)
	if epochs != 1 {
		t.Fatalf("epochs = %d, want 1", epochs)
	}

	// 5. Every eligible user's share lands once; the suspended account gets none.
	eligible := append([]models.User{chain[0], chain[1], chain[2]}, bystanders...)
	var credited float64
	for _, u := range eligible {
		got, err := svc.SettleUserGlobalShare(ctx, u.ID)
		if err != nil {
			t.Fatalf("settle share: %v", err)
		}
		if !approx(got, 140.0/6.0) && (got < 140.0/6.0-1e-6 || got > 140.0/6.0) {
			t.Fatalf("share = %v, want 140/6 = %v", got, 140.0/6.0)
		}
		credited += got
		if again, _ := svc.SettleUserGlobalShare(ctx, u.ID); again != 0 {
			t.Fatalf("a second settle credited %v", again)
		}
	}
	if got, _ := svc.SettleUserGlobalShare(ctx, suspended.ID); got != 0 {
		t.Fatalf("suspended account was paid %v", got)
	}
	if credited > 140+1e-9 || credited < 140-1e-3 {
		t.Fatalf("paid out %v of a 140 TP pool", credited)
	}

	// 6. Nothing is paid twice in the same month, and next month's accrual pays.
	more := FeeEvent{Feature: models.FeatureSpot, Token: "USDT", FeeUSD: 10, UserID: bystanders[0].ID,
		SourceType: "swap", SourceID: uuid.New()}
	if err := svc.OnFeeSettled(ctx, more); err != nil {
		t.Fatal(err)
	}
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, at(time.October, 30)); res.Settled {
		t.Fatal("a second payout in October")
	}
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, at(time.November, 25)); !res.Settled {
		t.Fatal("November's payout did not run")
	}
	assertGuardRowsSumToFee(t, db)
}

// Every settled fee's recorded parts must add back to the fee on Postgres too.
func assertGuardRowsSumToFee(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []models.FeeDistribution
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.FeeUSD == 0 {
			continue
		}
		sum := r.SystemUSD + r.BuyerCashbackUSD + r.UplineCashbackUSD + r.ProviderCashbackUSD + r.TPPoolUSD
		if !approx(sum, r.FeeUSD) {
			t.Fatalf("%s/%s: parts sum to %v, fee %v", r.SourceType, r.SourceID, sum, r.FeeUSD)
		}
	}
}
