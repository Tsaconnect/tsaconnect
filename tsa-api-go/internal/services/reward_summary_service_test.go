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

// addTPEarningGen inserts a TPEarning row at a given generation depth so the
// summary's per-generation grouping has data to fold.
func addTPEarningGen(t *testing.T, db *gorm.DB, userID uuid.UUID, generation int, tp float64) {
	t.Helper()
	e := models.TPEarning{
		ID:           uuid.New(),
		UserID:       userID,
		SourceUserID: uuid.New(),
		SourceType:   "checkout",
		SourceID:     uuid.New(),
		Generation:   generation,
		FeeAmountUSD: tp / 0.354,
		Percentage:   0.354,
		TPEarned:     tp,
		CreatedAt:    time.Now(),
	}
	if err := db.Create(&e).Error; err != nil {
		t.Fatalf("create tp_earning gen=%d: %v", generation, err)
	}
}

// staticClaimed is a claimedMCGPReader that always reports a fixed claimed total.
type staticClaimed struct{ mcgp float64 }

func (s staticClaimed) ClaimedMCGP(_ context.Context, _ uuid.UUID) (float64, error) {
	return s.mcgp, nil
}

// findLadderLevel returns the LadderLevel for level n from a summary (0-indexed
// by Level-1 since the ladder is dense 1..15).
func findLadderLevel(levels []LadderLevel, n int) LadderLevel {
	return levels[n-1]
}

// fakeSummaryClaimedReader implements the claimedOfReader the OnChainClaimedReader
// adapter wraps: claimed[account] base units keyed by account, + optional error.
type fakeSummaryClaimedReader struct {
	byAccount map[string]*big.Int
	err       error
}

func (f *fakeSummaryClaimedReader) ClaimedOf(_ context.Context, _ string, account string) (*big.Int, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.byAccount[account]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}

// TestOnChainClaimedReader covers the SP8 claimed adapter: it resolves the user's
// wallet, reads claimed[account] (18-dec base units), and returns it as float
// MCGP — the wiring the critical finding flagged as nil. Also: nil construction
// when deps are missing, fail-open on read error, and 0 for a wallet-less user.
func TestOnChainClaimedReader(t *testing.T) {
	db := setupRewardTestDB(t)
	ctx := context.Background()
	const dist = "0xdist0000000000000000000000000000000000a1"
	const wallet = "0x1111111111111111111111111111111111111111"

	// User WITH a wallet that has claimed 2.5 MCGP (2.5e18 base units).
	withWallet := mkRewardUser(t, db, 0)
	if err := db.Model(&models.User{}).Where("id = ?", withWallet.ID).
		Update("wallet_address", wallet).Error; err != nil {
		t.Fatalf("set wallet: %v", err)
	}
	claimed25, _ := new(big.Int).SetString("2500000000000000000", 10) // 2.5 MCGP

	reader := &fakeSummaryClaimedReader{byAccount: map[string]*big.Int{wallet: claimed25}}
	adapter := NewOnChainClaimedReader(db, reader, dist)
	if adapter == nil {
		t.Fatal("adapter must be non-nil when db + reader + distributor are present")
	}

	got, err := adapter.ClaimedMCGP(ctx, withWallet.ID)
	if err != nil {
		t.Fatalf("ClaimedMCGP: %v", err)
	}
	if !approxEq(got, 2.5) {
		t.Fatalf("claimed = %v MCGP, want 2.5", got)
	}

	// User WITHOUT a wallet ⇒ 0 claimed (no on-chain identity).
	noWallet := mkRewardUser(t, db, 0)
	got, err = adapter.ClaimedMCGP(ctx, noWallet.ID)
	if err != nil || got != 0 {
		t.Fatalf("wallet-less user: got=%v err=%v, want 0/nil", got, err)
	}

	// Read error ⇒ fail-open to 0 (never hides accrual / never errors the summary).
	failAdapter := NewOnChainClaimedReader(db, &fakeSummaryClaimedReader{err: errors.New("rpc down")}, dist)
	got, err = failAdapter.ClaimedMCGP(ctx, withWallet.ID)
	if err != nil || got != 0 {
		t.Fatalf("read-error must fail open: got=%v err=%v, want 0/nil", got, err)
	}

	// Missing deps ⇒ nil interface (summary falls back to gross cumulative).
	if NewOnChainClaimedReader(nil, reader, dist) != nil {
		t.Fatal("nil db must yield a nil adapter")
	}
	if NewOnChainClaimedReader(db, nil, dist) != nil {
		t.Fatal("nil reader must yield a nil adapter")
	}
	if NewOnChainClaimedReader(db, reader, "") != nil {
		t.Fatal("empty distributor must yield a nil adapter")
	}
}

// TestRewardSummary_FullShape_EarningsLevelCrossedPendingClaim covers the
// headline case: a user with TPEarning across multiple generations, tp_balance
// that has crossed level 3, and a pending (claimable) MCGP balance. It asserts
// the complete summary shape the app depends on.
func TestRewardSummary_FullShape_EarningsLevelCrossedPendingClaim(t *testing.T) {
	db := setupRewardTestDB(t)

	// tp_balance 150 ⇒ crossed L1(1),L2(10),L3(100); next is L4(1000).
	user := mkRewardUser(t, db, 150)

	// Earnings across generations 0,1,2 (+ a global-pool gen=-1 that must NOT
	// appear in per-generation).
	addTPEarningGen(t, db, user.ID, 0, 60)
	addTPEarningGen(t, db, user.ID, 1, 50)
	addTPEarningGen(t, db, user.ID, 2, 40)
	// gen=-1 global-pool credit — excluded from perGeneration.
	addTPEarningGen(t, db, user.ID, -1, 999)

	// A real RewardService refresher with a live price so the level rewards for
	// L1..L3 get awarded and a claimable MCGP balance accrues (the "pending claim").
	rs := NewRewardService(db, nil, &noopSettler{}, fixedPrice(0.5))

	// Pending global-share reader: a real FeeDistributionService over the same DB
	// (no pool rows ⇒ pending share 0, which is fine for this case).
	fee := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	svc := NewRewardSummaryService(db, rs, fee, nil)

	sum, err := svc.Summary(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	// tp_balance unchanged by refresh (no pending share to fold here).
	if !approxEq(sum.TPBalance, 150) {
		t.Errorf("tpBalance = %v, want 150", sum.TPBalance)
	}

	// Level + progress: at L3, next threshold L4 = 1000, progress from 100→1000.
	if sum.CurrentLevel != 3 {
		t.Errorf("currentLevel = %d, want 3", sum.CurrentLevel)
	}
	// Hero rank name reflects CurrentLevel (L3 == "Trader").
	if sum.CurrentLevelName != "Trader" {
		t.Errorf("currentLevelName = %q, want Trader", sum.CurrentLevelName)
	}
	if !approxEq(sum.NextLevelThresholdTP, 1000) {
		t.Errorf("nextLevelThresholdTP = %v, want 1000", sum.NextLevelThresholdTP)
	}
	wantProgress := (150.0 - 100.0) / (1000.0 - 100.0)
	if !approxEq(sum.ProgressToNext, wantProgress) {
		t.Errorf("progressToNext = %v, want %v", sum.ProgressToNext, wantProgress)
	}

	// Claimable: L1($0.01)+L2($0.10)+L3($1.00) = $1.11 at 0.5 USD/MCGP = 2.22 MCGP.
	wantClaimable := (0.01 + 0.10 + 1.00) / 0.5
	if !approxEq(sum.ClaimableMCGP, wantClaimable) {
		t.Errorf("claimableMcgp = %v, want %v", sum.ClaimableMCGP, wantClaimable)
	}
	if sum.TotalClaimedMCGP != 0 {
		t.Errorf("totalClaimedMcgp = %v, want 0 (no claimed reader)", sum.TotalClaimedMCGP)
	}

	// Ladder: 15 rungs, L1..L3 earned, L4 in_progress, L5+ locked. Thresholds +
	// prizes come straight from tp_level (reused).
	if len(sum.Levels) != models.TPLevelCount {
		t.Fatalf("levels len = %d, want %d", len(sum.Levels), models.TPLevelCount)
	}
	for _, n := range []int{1, 2, 3} {
		if got := findLadderLevel(sum.Levels, n).Status; got != LevelStatusEarned {
			t.Errorf("level %d status = %q, want earned", n, got)
		}
	}
	if got := findLadderLevel(sum.Levels, 4).Status; got != LevelStatusInProgress {
		t.Errorf("level 4 status = %q, want in_progress", got)
	}
	if got := findLadderLevel(sum.Levels, 5).Status; got != LevelStatusLocked {
		t.Errorf("level 5 status = %q, want locked", got)
	}
	// Threshold + reward + prize reuse tp_level: L6 carries a physical prize.
	l6 := findLadderLevel(sum.Levels, 6)
	if !approxEq(l6.ThresholdTP, models.ThresholdTPForLevel(6)) {
		t.Errorf("L6 thresholdTP = %v, want %v", l6.ThresholdTP, models.ThresholdTPForLevel(6))
	}
	if l6.Prize != models.PrizeForLevel(6) || l6.Prize == "" {
		t.Errorf("L6 prize = %q, want %q", l6.Prize, models.PrizeForLevel(6))
	}
	// Every rung carries the PM rank name (spot-check L1/L6/L15 exact values).
	if got := findLadderLevel(sum.Levels, 1).Name; got != "Beginner" {
		t.Errorf("L1 name = %q, want Beginner", got)
	}
	if l6.Name != "Ambassador" {
		t.Errorf("L6 name = %q, want Ambassador", l6.Name)
	}
	if got := findLadderLevel(sum.Levels, 15).Name; got != "Icon" {
		t.Errorf("L15 name = %q, want Icon", got)
	}

	// Per-generation: 11 rows (0..10), gen0=60, gen1=50, gen2=40, rest 0; gen=-1
	// excluded.
	if len(sum.PerGeneration) != 11 {
		t.Fatalf("perGeneration len = %d, want 11", len(sum.PerGeneration))
	}
	for i, pg := range sum.PerGeneration {
		if pg.Generation != i {
			t.Errorf("perGeneration[%d].generation = %d, want %d", i, pg.Generation, i)
		}
	}
	if !approxEq(sum.PerGeneration[0].EarnedTP, 60) {
		t.Errorf("gen0 earnedTp = %v, want 60", sum.PerGeneration[0].EarnedTP)
	}
	if !approxEq(sum.PerGeneration[1].EarnedTP, 50) {
		t.Errorf("gen1 earnedTp = %v, want 50", sum.PerGeneration[1].EarnedTP)
	}
	if !approxEq(sum.PerGeneration[2].EarnedTP, 40) {
		t.Errorf("gen2 earnedTp = %v, want 40", sum.PerGeneration[2].EarnedTP)
	}
	for g := 3; g <= 10; g++ {
		if !approxEq(sum.PerGeneration[g].EarnedTP, 0) {
			t.Errorf("gen%d earnedTp = %v, want 0", g, sum.PerGeneration[g].EarnedTP)
		}
	}

	// No L6+ crossed ⇒ no prizes due.
	if len(sum.PrizesDue) != 0 {
		t.Errorf("prizesDue len = %d, want 0", len(sum.PrizesDue))
	}
}

// TestRewardSummary_PerGenerationGrouping verifies multiple earnings at the same
// generation are summed, every gen 0..10 is present, and gen=-1 (global pool) is
// excluded.
func TestRewardSummary_PerGenerationGrouping(t *testing.T) {
	db := setupRewardTestDB(t)
	user := mkRewardUser(t, db, 0)

	// Two rows at gen0, one at gen3, one at gen10, one global-pool gen=-1.
	addTPEarningGen(t, db, user.ID, 0, 1.5)
	addTPEarningGen(t, db, user.ID, 0, 2.5) // same gen ⇒ summed to 4.0
	addTPEarningGen(t, db, user.ID, 3, 7.0)
	addTPEarningGen(t, db, user.ID, 10, 0.25)
	addTPEarningGen(t, db, user.ID, -1, 100) // excluded

	// No refresher / pending reader needed for pure grouping.
	svc := NewRewardSummaryService(db, nil, nil, nil)

	sum, err := svc.Summary(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	if len(sum.PerGeneration) != 11 {
		t.Fatalf("perGeneration len = %d, want 11", len(sum.PerGeneration))
	}
	want := map[int]float64{0: 4.0, 3: 7.0, 10: 0.25}
	for g := 0; g <= 10; g++ {
		got := sum.PerGeneration[g].EarnedTP
		if !approxEq(got, want[g]) {
			t.Errorf("gen%d earnedTp = %v, want %v", g, got, want[g])
		}
	}
}

// TestRewardSummary_IneligibleSeesAccrualClaimableGated covers the eligibility
// gate: a user who has accrued TP (and even a claimable MCGP balance) but has
// NOT yet claimed on-chain still sees the full accrual view (level, ladder,
// per-generation, claimable), while a claimed reader correctly gates the
// still-claimable figure down. It also asserts an ineligible-ish user (zero
// claimable, low TP) gets a coherent accrual-only summary.
func TestRewardSummary_IneligibleSeesAccrualClaimableGated(t *testing.T) {
	db := setupRewardTestDB(t)

	// User below L1 (tp 0.5) with some downline earnings but no level crossed.
	user := mkRewardUser(t, db, 0.5)
	addTPEarningGen(t, db, user.ID, 0, 0.3)
	addTPEarningGen(t, db, user.ID, 1, 0.2)

	// Seed a claimable balance directly (simulating an already-processed reward)
	// so we can prove the claimed reader gates it.
	if err := db.Create(&models.RewardClaimable{
		UserID:        user.ID,
		ClaimableMCGP: 10.0,
		UpdatedAt:     time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed claimable: %v", err)
	}

	// Claimed reader says 4 MCGP already claimed ⇒ still-claimable = 6.
	svc := NewRewardSummaryService(db, nil, nil, staticClaimed{mcgp: 4.0})

	sum, err := svc.Summary(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	// Accrual is fully visible even below any level.
	if sum.CurrentLevel != 0 {
		t.Errorf("currentLevel = %d, want 0 (below L1)", sum.CurrentLevel)
	}
	// Below L1 there is no rank yet, so the hero name is empty.
	if sum.CurrentLevelName != "" {
		t.Errorf("currentLevelName = %q, want empty below L1", sum.CurrentLevelName)
	}
	if !approxEq(sum.NextLevelThresholdTP, 1) {
		t.Errorf("nextLevelThresholdTP = %v, want 1 (L1)", sum.NextLevelThresholdTP)
	}
	// Progress from 0 → L1(1) at tp 0.5 = 0.5.
	if !approxEq(sum.ProgressToNext, 0.5) {
		t.Errorf("progressToNext = %v, want 0.5", sum.ProgressToNext)
	}
	if len(sum.Levels) != models.TPLevelCount {
		t.Errorf("levels len = %d, want %d", len(sum.Levels), models.TPLevelCount)
	}
	// L1 is in_progress, none earned.
	if got := findLadderLevel(sum.Levels, 1).Status; got != LevelStatusInProgress {
		t.Errorf("L1 status = %q, want in_progress", got)
	}
	if !approxEq(sum.PerGeneration[0].EarnedTP, 0.3) || !approxEq(sum.PerGeneration[1].EarnedTP, 0.2) {
		t.Errorf("perGeneration accrual not visible: gen0=%v gen1=%v",
			sum.PerGeneration[0].EarnedTP, sum.PerGeneration[1].EarnedTP)
	}

	// Claimable is gated by the claimed reader: gross 10 − claimed 4 = 6.
	if !approxEq(sum.TotalClaimedMCGP, 4.0) {
		t.Errorf("totalClaimedMcgp = %v, want 4", sum.TotalClaimedMCGP)
	}
	if !approxEq(sum.ClaimableMCGP, 6.0) {
		t.Errorf("claimableMcgp = %v, want 6 (gross 10 − claimed 4)", sum.ClaimableMCGP)
	}
}

// TestRewardSummary_PrizesDueAndTopOfLadder asserts L6+ prizes_due surface in the
// summary and the top-of-ladder progress/threshold edge is handled.
func TestRewardSummary_PrizesDueAndTopOfLadder(t *testing.T) {
	db := setupRewardTestDB(t)

	// tp_balance at the very top threshold (L15 = 1e14).
	user := mkRewardUser(t, db, models.ThresholdTPForLevel(models.TPLevelCount))

	// A pending L6 prize obligation.
	if err := db.Create(&models.PrizeDue{
		ID:        uuid.New(),
		UserID:    user.ID,
		Level:     6,
		Prize:     models.PrizeForLevel(6),
		Status:    models.PrizeDueStatusPending,
		CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed prize: %v", err)
	}

	svc := NewRewardSummaryService(db, nil, nil, nil)
	sum, err := svc.Summary(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	if sum.CurrentLevel != models.TPLevelCount {
		t.Errorf("currentLevel = %d, want %d", sum.CurrentLevel, models.TPLevelCount)
	}
	// Top of ladder rank name is the PM L15 name "Icon".
	if sum.CurrentLevelName != "Icon" {
		t.Errorf("currentLevelName = %q, want Icon at top of ladder", sum.CurrentLevelName)
	}
	// Top of ladder: no next threshold, progress saturated to 1.0.
	if sum.NextLevelThresholdTP != 0 {
		t.Errorf("nextLevelThresholdTP = %v, want 0 at top of ladder", sum.NextLevelThresholdTP)
	}
	if !approxEq(sum.ProgressToNext, 1.0) {
		t.Errorf("progressToNext = %v, want 1.0 at top of ladder", sum.ProgressToNext)
	}
	// Every level earned; none in_progress.
	for _, l := range sum.Levels {
		if l.Status != LevelStatusEarned {
			t.Errorf("level %d status = %q, want all earned at top", l.Level, l.Status)
		}
	}
	// Prize surfaced.
	if len(sum.PrizesDue) != 1 {
		t.Fatalf("prizesDue len = %d, want 1", len(sum.PrizesDue))
	}
	if sum.PrizesDue[0].Level != 6 || sum.PrizesDue[0].Status != models.PrizeDueStatusPending {
		t.Errorf("prizeDue = %+v, want L6 pending", sum.PrizesDue[0])
	}
	if sum.PrizesDue[0].Prize != models.PrizeForLevel(6) {
		t.Errorf("prizeDue prize = %q, want %q", sum.PrizesDue[0].Prize, models.PrizeForLevel(6))
	}
	// The prize row carries the L6 rank name "Ambassador".
	if sum.PrizesDue[0].Name != "Ambassador" {
		t.Errorf("prizeDue name = %q, want Ambassador", sum.PrizesDue[0].Name)
	}
}
