package services

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func setupTPProgramDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupFeeDistTestDB(t)
	if err := db.Exec(`CREATE TABLE tp_program_configs (
		id TEXT PRIMARY KEY,
		user_pct REAL NOT NULL, upline_pct REAL NOT NULL, pool_pct REAL NOT NULL,
		zero_fee_tp REAL NOT NULL, payout_day INTEGER NOT NULL,
		signup_bonus_tp REAL NOT NULL DEFAULT 1, onboarding_bonus_tp REAL NOT NULL DEFAULT 10,
		updated_by TEXT, created_at DATETIME, updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return db
}

func TestTPProgram_DefaultsWhenNoRow(t *testing.T) {
	svc := NewTPProgramConfigService(setupTPProgramDB(t))
	if got := svc.Current(); got.UserPct != 0.05 || got.UplinePct != 0.05 || got.PoolPct != 0.02 ||
		got.ZeroFeeTP != 0.001 || got.PayoutDay != 24 {
		t.Fatalf("defaults = %+v, want 5/5/2, 0.001 TP, day 24", got)
	}
	var nilSvc *TPProgramConfigService
	if nilSvc.Current().PayoutDay != 24 {
		t.Fatal("a nil service must serve the defaults")
	}
}

func TestTPProgram_SaveTakesEffectAndRejectsBadInput(t *testing.T) {
	svc := NewTPProgramConfigService(setupTPProgramDB(t))
	saved, err := svc.Save(models.TPProgramConfig{UserPct: 0.04, UplinePct: 0.03, PoolPct: 0.01, ZeroFeeTP: 0.002, PayoutDay: 20})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.UserPct != 0.04 || svc.Current().PayoutDay != 20 {
		t.Fatalf("edit not live: %+v", svc.Current())
	}
	// A second save updates the single row rather than adding another.
	if _, err := svc.Save(models.TPProgramConfig{UserPct: 0.06, UplinePct: 0.03, PoolPct: 0.01, ZeroFeeTP: 0.002, PayoutDay: 20}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	var n int64
	svc.db.Model(&models.TPProgramConfig{}).Count(&n)
	if n != 1 || svc.Current().UserPct != 0.06 {
		t.Fatalf("want one updated row, got %d rows, user pct %v", n, svc.Current().UserPct)
	}

	for name, bad := range map[string]models.TPProgramConfig{
		"negative pct": {UserPct: -0.01, UplinePct: 0.05, PoolPct: 0.02, PayoutDay: 24},
		"total over 1": {UserPct: 0.6, UplinePct: 0.3, PoolPct: 0.2, PayoutDay: 24},
		"day 29":       {UserPct: 0.05, UplinePct: 0.05, PoolPct: 0.02, PayoutDay: 29},
		"negative tp":  {UserPct: 0.05, UplinePct: 0.05, PoolPct: 0.02, ZeroFeeTP: -1, PayoutDay: 24},
	} {
		if _, err := svc.Save(bad); err == nil {
			t.Fatalf("%s: want a validation error", name)
		}
	}
	if svc.Current().UserPct != 0.06 {
		t.Fatal("a rejected save must leave the program untouched")
	}
}

func TestTPProgram_InvalidStoredRowFallsBackToDefaults(t *testing.T) {
	db := setupTPProgramDB(t)
	if err := db.Exec(`INSERT INTO tp_program_configs (id, user_pct, upline_pct, pool_pct, zero_fee_tp, payout_day)
		VALUES ('x', 0.9, 0.9, 0.9, 0, 24)`).Error; err != nil {
		t.Fatal(err)
	}
	if got := NewTPProgramConfigService(db).Current(); got.UserPct != 0.05 {
		t.Fatalf("a hand-edited bad row must not be applied, got %+v", got)
	}
}

func TestLastPayoutInstant(t *testing.T) {
	wat := PayoutLocation
	cases := []struct {
		now, want time.Time
	}{
		// the 24th, one minute before and after 00:00 WAT (= 23:00 UTC the day before)
		{time.Date(2026, 10, 23, 22, 59, 0, 0, time.UTC), time.Date(2026, 9, 24, 0, 0, 0, 0, wat)},
		{time.Date(2026, 10, 23, 23, 1, 0, 0, time.UTC), time.Date(2026, 10, 24, 0, 0, 0, 0, wat)},
		{time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC), time.Date(2025, 12, 24, 0, 0, 0, 0, wat)}, // year wrap
	}
	for _, c := range cases {
		if got := LastPayoutInstant(c.now, 24); !got.Equal(c.want) {
			t.Fatalf("LastPayoutInstant(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}

func TestSettleGlobalPoolIfDue_MonthlyAndCarriesRolloutBalance(t *testing.T) {
	db := setupTPProgramDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	mkFeeUser(t, db, "a"+uuidShort(), nil, models.AccountStatusActive)
	mkFeeUser(t, db, "b"+uuidShort(), nil, models.AccountStatusActive)
	ctx := context.Background()
	seedGlobalPool(t, db, 10) // balance that exists at rollout

	day := func(d int, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, PayoutLocation) }

	// First tick after rollout stamps the window and does NOT pay out.
	if res, err := svc.SettleGlobalPoolIfDue(ctx, day(21, 9)); err != nil || res.Settled {
		t.Fatalf("rollout tick: settled=%v err=%v, want a stamp only", res.Settled, err)
	}
	if countEpochs(t, db) != 0 {
		t.Fatal("deploying must not pay out the pool")
	}
	// Later the same month: not due.
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, day(23, 23)); res.Settled {
		t.Fatal("must not pay before the 24th")
	}
	// The 24th: pays once, and the carried-over balance is included.
	res, err := svc.SettleGlobalPoolIfDue(ctx, day(24, 0))
	if err != nil || !res.Settled || res.DeltaTP != 10 || res.UserCount != 2 {
		t.Fatalf("payout: %+v err=%v, want 10 TP over 2 users", res, err)
	}
	// Hourly ticks afterwards do nothing, even with fresh accrual.
	addGlobalAccrual(t, db, 4)
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, day(24, 5)); res.Settled {
		t.Fatal("a second payout in the same month")
	}
	// An outage across the 24th is made up on the next tick, once.
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, time.Date(2026, 11, 26, 9, 0, 0, 0, PayoutLocation)); !res.Settled || res.DeltaTP != 4 {
		t.Fatalf("catch-up payout: %+v, want 4 TP", res)
	}
	if countEpochs(t, db) != 2 {
		t.Fatalf("epochs = %d, want 2", countEpochs(t, db))
	}
}

func TestSettleGlobalPoolEpoch_SkipsSuspendedUsers(t *testing.T) {
	db := setupTPProgramDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()
	active := mkFeeUser(t, db, "act"+uuidShort(), nil, models.AccountStatusActive)
	suspended := mkFeeUser(t, db, "sus"+uuidShort(), nil, models.AccountStatusSuspended)
	seedGlobalPool(t, db, 10)

	res, err := svc.SettleGlobalPoolEpoch(ctx)
	if err != nil || res.UserCount != 1 {
		t.Fatalf("epoch: %+v err=%v, want the pool split over the 1 active user", res, err)
	}
	gotActive, err := svc.PendingUserGlobalShare(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotSuspended, err := svc.PendingUserGlobalShare(ctx, suspended.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(gotActive, 10) || gotSuspended != 0 {
		t.Fatalf("active owed %v (want 10), suspended owed %v (want 0)", gotActive, gotSuspended)
	}
}

func TestRewardRatesFor_FollowsLiveSplitsAndProgram(t *testing.T) {
	db := setupTPProgramDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 100).
		WithTPProgram(NewTPProgramConfigService(db))

	// Product sale: buyer 8% of the fee back; TP = 5% of the 84% the platform keeps.
	got := svc.RewardRatesFor(models.FeatureProductMerchant)
	if !approx(got.BuyerCashback, 0.08) || !approx(got.UplineCashback, 0.08) ||
		!approx(got.BuyerTP, 0.84*0.05*100) || !approx(got.USDPerTP, 0.01) {
		t.Fatalf("default rates = %+v", got)
	}

	// An admin edit shows up for the next reader.
	if _, err := svc.tpProgram.Save(models.TPProgramConfig{UserPct: 0.10, UplinePct: 0.05, PoolPct: 0.02, ZeroFeeTP: 0.001, PayoutDay: 24}); err != nil {
		t.Fatal(err)
	}
	if got := svc.RewardRatesFor(models.FeatureProductMerchant); !approx(got.BuyerTP, 0.84*0.10*100) {
		t.Fatalf("after the edit BuyerTP = %v, want %v", got.BuyerTP, 0.84*0.10*100)
	}

	var nilSvc *FeeDistributionService
	if (nilSvc.RewardRatesFor(models.FeatureProductMerchant) != RewardRates{}) {
		t.Fatal("a nil engine must report no rewards")
	}
}

// Moving the payout day after the month has been paid must not pay a second time.
func TestSettleGlobalPoolIfDue_OnePayoutPerMonthWhateverTheDay(t *testing.T) {
	db := setupTPProgramDB(t)
	prog := NewTPProgramConfigService(db)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0).WithTPProgram(prog)
	mkFeeUser(t, db, "a"+uuidShort(), nil, models.AccountStatusActive)
	ctx := context.Background()
	seedGlobalPool(t, db, 10)
	at := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 9, 0, 0, 0, PayoutLocation) }

	svc.SettleGlobalPoolIfDue(ctx, at(time.October, 21)) // stamp
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, at(time.October, 24)); !res.Settled {
		t.Fatal("the 24th must pay")
	}
	addGlobalAccrual(t, db, 4)
	if _, err := prog.Save(models.TPProgramConfig{UserPct: 0.05, UplinePct: 0.05, PoolPct: 0.02, ZeroFeeTP: 0.001, PayoutDay: 28}); err != nil {
		t.Fatal(err)
	}
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, at(time.October, 29)); res.Settled {
		t.Fatal("moving the day to the 28th after the 24th paid must not pay October again")
	}
	if res, _ := svc.SettleGlobalPoolIfDue(ctx, at(time.November, 29)); !res.Settled || res.DeltaTP != 4 {
		t.Fatalf("November must pay on the new day: %+v", res)
	}
}
