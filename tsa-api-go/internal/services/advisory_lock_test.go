package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func TestPoolLockKey_ShapeMatchesTheCompanyWallets(t *testing.T) {
	if got := poolLockKey(" BSC ", "usdt"); got != "pool:bsc:USDT" {
		t.Fatalf("pool key = %q", got)
	}
}

// One transaction around fn; fn's error rolls it back (SQLite has no advisory
// lock, so the lock itself is pinned by the Postgres test).
func TestWithAdvisoryLock_RollsBackOnError(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	boom := errors.New("boom")
	err := withAdvisoryLock(context.Background(), s.db, poolLockKey("bsc", "USDT"), "liquidity pool: order lock", func(tx *gorm.DB) error {
		seedProviderIntentTx(t, tx, userID)
		return boom
	})
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if !errors.Is(err, boom) || n != 0 {
		t.Fatalf("err=%v rows=%d, want the write rolled back", err, n)
	}
}

// The generalised cap is the company wallet's, byte-for-byte, for a scope.
func TestCheckDailyBuyCap_CountsOnlyItsScopeAndExcludesItself(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	mine := seedTreasuryIntent(t, s, userID, models.ITStatusPaymentReceived) // 7.27815 USDC
	seedProviderIntent(t, s, userID, models.ITProviderFlutterwave, models.ITStatusPaymentReceived)
	reached := errors.New("cap")
	c := dailyBuyCap{scope: scopeCompanyWalletBuys, limit: big.NewInt(10_000_000), reached: reached, metric: "test"}
	if err := s.checkDailyBuyCap(ctx, s.db, userID, big.NewInt(2_000_000), uuid.Nil, c); err != nil {
		t.Fatalf("7.28 + 2 ≤ 10 (Flutterwave rows are out of scope): %v", err)
	}
	if err := s.checkDailyBuyCap(ctx, s.db, userID, big.NewInt(3_000_000), uuid.Nil, c); !errors.Is(err, reached) {
		t.Fatalf("7.28 + 3 > 10: %v", err)
	}
	if err := s.checkDailyBuyCap(ctx, s.db, userID, big.NewInt(9_000_000), mine.ID, c); err != nil {
		t.Fatalf("a live re-price excludes its own row: %v", err)
	}
	// Cancelled and refunded buys never count, and nor does yesterday.
	s.db.Model(mine).Update("status", models.ITStatusCancelled)
	old := seedTreasuryIntent(t, s, userID, models.ITStatusPaymentReceived)
	s.db.Model(old).Update("created_at", time.Now().Add(-25*time.Hour))
	if err := s.checkDailyBuyCap(ctx, s.db, userID, big.NewInt(9_000_000), uuid.Nil, c); err != nil {
		t.Fatalf("cancelled/old rows counted: %v", err)
	}
}

// seedProviderIntentTx is seedProviderIntent inside a caller's transaction.
func seedProviderIntentTx(t *testing.T, tx *gorm.DB, userID uuid.UUID) {
	t.Helper()
	until := time.Now().Add(15 * time.Minute)
	if err := tx.Create(&models.ITIntent{ID: uuid.New(), UserID: userID, Direction: models.ITDirectionBuy,
		Status: models.ITStatusPendingPayment, Provider: models.ITProviderTreasury, Asset: "USDC", AssetAmount: "1",
		Currency: "NGN", DestChain: "BASE", LockedRate: "1", DisplayRate: "1", RateExpiresAt: &until,
		NgnKobo: "1", AmountMinor: "1", RateKobo: "1", RateMinor: "1", McgpAmount: "0", FeeNgnKobo: "0",
		NetNgnKobo: "1", PlatformKeptKobo: "0", ReceivedAmount: "0"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
}
