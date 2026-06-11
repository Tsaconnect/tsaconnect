// Operator command: live money-flow smoke test + demo-data seed.
//
// Drives synthetic FeeEvents through the v2 engine (the SAME OnFeeSettled the
// fee hooks use) for a loginnable demo account, then runs the reward processor,
// and reports the resulting cashback / TP / level (named) / claimable for the
// demo user and its uplines. Validates the live wiring end-to-end AND seeds a
// populated demo so a PM test build shows the feature instead of empty states.
//
// Run (from tsa-api-go/, with FEE_MODEL_V2_ENABLED=true):
//   go run ./cmd/feetp-smoke
//
// Idempotent-ish: the demo user is created once (reused after); each run adds a
// fresh batch of fee events (unique SourceIDs) so balances grow per run — run
// once for a clean demo.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

const (
	demoEmail    = "demo@tsaconnect.app"
	demoUsername = "tsa_demo"
	demoPassword = "DemoTest#2026"
)

func main() {
	_ = godotenv.Load()
	cfg := config.Load()
	db, err := config.ConnectDB(cfg)
	if err != nil {
		log.Fatalf("[smoke] db: %v", err)
	}
	if !cfg.IsFeeModelV2Enabled() {
		log.Printf("[smoke] WARNING: FEE_MODEL_V2_ENABLED is OFF — accrual still writes rows but the engine isn't the live charge path.")
	}
	ctx := context.Background()

	graph := services.NewReferralGraphService(db)
	fds := services.NewFeeDistributionService(db, graph, cfg.TPPerUSDOrDefault())
	bus := events.NewBus()
	// Fixed demo MCGP price ($1.00 → claimable MCGP ≈ claimable USD, clean demo
	// numbers). Deterministic for the seed; the live reward worker may later
	// recompute claimable at the real OTC price (idempotent).
	price := func(ctx context.Context) (float64, error) { return 1.0, nil }
	rewardSvc := services.NewRewardService(db, bus, fds, price)

	// --- 1. demo user (idempotent) ---
	var demo models.User
	if err := db.Where("email = ?", demoEmail).First(&demo).Error; err != nil {
		hash, herr := models.HashPassword(demoPassword)
		if herr != nil {
			log.Fatalf("[smoke] hash: %v", herr)
		}
		var upline models.User // give the demo a real upline chain so gen1+ TP flows up
		if err := db.Where("referred_by IS NOT NULL AND email <> ?", demoEmail).First(&upline).Error; err != nil {
			log.Fatalf("[smoke] no upline candidate: %v", err)
		}
		demo = models.User{
			Name:               "TSA Demo",
			Username:           demoUsername,
			Email:              demoEmail,
			Password:           hash,
			Role:               models.RoleUser,
			VerificationStatus: models.VerificationStatusVerified, // claim-eligible
			AccountStatus:      models.AccountStatusActive,
			EmailVerified:      true,
			ReferredBy:         &upline.ID,
		}
		if err := db.Create(&demo).Error; err != nil {
			log.Fatalf("[smoke] create demo: %v", err)
		}
		fmt.Printf("[smoke] created demo user %s (upline=%s)\n", demo.ID, upline.Username)
	} else {
		fmt.Printf("[smoke] demo user exists %s\n", demo.ID)
	}

	// --- 2. drive synthetic fee events (gen0 = demo) ---
	// Sized so the demo's gen0 TP (= fee * 0.20 * 0.354) crosses several levels.
	batch := []struct {
		feature string
		fee     float64
	}{
		{models.FeatureProductMerchant, 6000}, // product sales
		{models.FeatureProductMerchant, 6000},
		{models.FeatureP2PPlatform, 3000},
		{models.FeatureWithdrawal, 2000},
		{models.FeatureSpot, 1500},
	}
	var totalFee float64
	for i, e := range batch {
		ev := services.FeeEvent{
			Feature:    e.feature,
			Token:      "USDT",
			FeeUSD:     e.fee,
			UserID:     demo.ID,
			SourceType: "demo_seed",
			SourceID:   uuid.New(),
		}
		if err := fds.OnFeeSettled(ctx, ev); err != nil {
			log.Printf("[smoke] event %d (%s $%.0f): %v", i, e.feature, e.fee, err)
		}
		totalFee += e.fee
	}
	fmt.Printf("[smoke] drove %d fee events, total fee $%.0f\n", len(batch), totalFee)

	// --- 3. process rewards: demo + its uplines ---
	targets := []uuid.UUID{demo.ID}
	if ups, err := graph.Uplines(ctx, demo.ID, 10); err == nil {
		for _, a := range ups {
			targets = append(targets, a.UserID)
		}
	}
	for _, id := range targets {
		if res, err := rewardSvc.ProcessUserRewards(ctx, id); err != nil {
			log.Printf("[smoke] process %s: %v", id, err)
		} else if id == demo.ID {
			fmt.Printf("[smoke] demo rewards: level=%d levelsAwarded=%d cashbacksConverted=%d prizes=%d\n",
				res.CurrentLevel, res.LevelsAwarded, res.CashbacksConverted, res.PrizesRecorded)
		}
	}

	// --- 4. report demo + uplines ---
	fmt.Println("\n=== RESULT (demo + uplines) ===")
	report(db, "DEMO  "+demoUsername, demo.ID)
	if ups, err := graph.Uplines(ctx, demo.ID, 5); err == nil {
		for i, a := range ups {
			report(db, fmt.Sprintf("gen%d  upline", i+1), a.UserID)
		}
	}
	fmt.Printf("\n[smoke] demo login → email=%s  password=%s\n", demoEmail, demoPassword)
}

func report(db *gorm.DB, label string, id uuid.UUID) {
	var u struct {
		Username        string
		TPBalance       float64
		CashbackBalance float64
	}
	db.Table("users").Select("username, tp_balance, cashback_balance").Where("id = ?", id).Scan(&u)
	var claimable float64
	db.Table("reward_claimables").Select("COALESCE(claimable_mcgp,0)").Where("user_id = ?", id).Scan(&claimable)
	level := models.LevelFor(u.TPBalance)
	name := models.NameForLevel(level)
	if name == "" {
		name = "—"
	}
	fmt.Printf("  %-14s @%-12s  TP=%-10.2f  L%-2d %-11s  cashback=$%-8.2f  claimableMCGP=%.4f\n",
		label, u.Username, u.TPBalance, level, name, u.CashbackBalance, claimable)
}
