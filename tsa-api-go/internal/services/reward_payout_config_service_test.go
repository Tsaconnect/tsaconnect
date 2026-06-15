package services

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupRewardPayoutDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Raw DDL (avoids the Postgres gen_random_uuid default on SQLite).
	if err := db.Exec(`CREATE TABLE reward_payout_configs (
		id TEXT PRIMARY KEY, reward_kind TEXT NOT NULL, payment_token TEXT NOT NULL,
		payout_token TEXT NOT NULL, chain TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME, updated_at DATETIME,
		UNIQUE(reward_kind, payment_token))`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// TestRewardPayoutResolve_LaunchDefaults locks the PM launch mapping: cashback
// follows the payment token (USDC→USDC, USDT/MCGP→MCGP), TP-level → MCGP, all Sonic.
func TestRewardPayoutResolve_LaunchDefaults(t *testing.T) {
	db := setupRewardPayoutDB(t)
	svc, err := NewRewardPayoutConfigService(db)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	cases := []struct {
		kind, payToken string
		wantToken      string
		wantChain      string
	}{
		{models.RewardKindCashback, "USDC", "USDC", "sonic"},
		{models.RewardKindCashback, "USDT", "MCGP", "sonic"},
		{models.RewardKindCashback, "MCGP", "MCGP", "sonic"},
		{models.RewardKindCashback, "DAI", "MCGP", "sonic"},  // unknown token → kind fallback (*)
		{models.RewardKindLevel, "USDC", "MCGP", "sonic"},    // TP level ignores payment token
		{models.RewardKindLevel, "anything", "MCGP", "sonic"},
	}
	for _, tc := range cases {
		got := svc.Resolve(tc.kind, tc.payToken)
		if got.PayoutToken != tc.wantToken || got.Chain != tc.wantChain {
			t.Errorf("Resolve(%s, %s) = %s/%s, want %s/%s",
				tc.kind, tc.payToken, got.PayoutToken, got.Chain, tc.wantToken, tc.wantChain)
		}
	}
}

// TestRewardPayoutUpsert_RepointsAndPersists confirms an admin edit changes the
// resolution and survives a cache reload.
func TestRewardPayoutUpsert_RepointsAndPersists(t *testing.T) {
	db := setupRewardPayoutDB(t)
	svc, err := NewRewardPayoutConfigService(db)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	// Admin repoints USDC cashback to MCGP.
	if _, err := svc.Upsert(models.RewardKindCashback, "USDC", "MCGP", "sonic", true); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := svc.Resolve(models.RewardKindCashback, "USDC"); got.PayoutToken != "MCGP" {
		t.Fatalf("after repoint: USDC cashback = %s, want MCGP", got.PayoutToken)
	}

	// Survives reload from the table (no stale cache).
	if err := svc.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := svc.Resolve(models.RewardKindCashback, "USDC"); got.PayoutToken != "MCGP" {
		t.Fatalf("after reload: USDC cashback = %s, want MCGP", got.PayoutToken)
	}

	// Invalid payout token is rejected.
	if _, err := svc.Upsert(models.RewardKindCashback, "USDC", "ETH", "sonic", true); err == nil {
		t.Fatal("expected error for unsupported payout token ETH")
	}
}
