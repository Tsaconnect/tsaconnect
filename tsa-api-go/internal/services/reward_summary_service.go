package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// RewardSummaryService is the read-aggregation behind GET /api/rewards/summary —
// the single payload the app's TP screen renders (fee/TP overhaul SP8). It does
// NOT compute or move rewards; it composes the already-computed SP4/SP5/SP6 state
// into one view:
//
//   - tp_balance (made current by first folding the user's pending SP4
//     global-pool share — see RefreshBeforeRead),
//   - the SP5 cumulative claimable-MCGP balance + total already claimed,
//   - the current reward level + progress to the next (from the SP5 tp_level
//     ladder — REUSED, never duplicated),
//   - the full 15-level ladder with per-level earned/in_progress/locked status,
//   - the user's TPEarning grouped by Generation (0..10) — the image's
//     "user + downline 10-generation earnings",
//   - the global-pool share TP, and the L6+ prizes due.
//
// Every dependency beyond db is OPTIONAL and nil-safe so the service degrades
// gracefully (a dev env without the fee engine / reward processor still returns a
// coherent accrual view; the claimable figures just read 0). This mirrors the
// codebase's dormant-by-default integrations.
type RewardSummaryService struct {
	db *gorm.DB

	// refresher makes tp_balance current before the level is computed. In
	// production this is *RewardService (ProcessUserRewards settles the pending
	// SP4 global share AND awards newly-crossed levels). When only a lighter
	// surface is available, a *FeeDistributionService can be adapted (settle-only).
	// nil ⇒ the read uses the stored tp_balance as-is.
	refresher rewardRefresher

	// pending reports the user's not-yet-settled SP4 global-pool TP (read-only),
	// surfaced as globalPoolShareTP. nil ⇒ reported as 0.
	pending pendingGlobalShareReader

	// claimedReader optionally reports the user's total already-claimed MCGP from
	// on-chain (base units → MCGP). nil ⇒ totalClaimedMCGP is 0 and claimableMCGP
	// is reported as the gross cumulative owed.
	claimedReader claimedMCGPReader
}

// rewardRefresher folds a user's pending accruals into tp_balance before the
// summary reads it, so the level/progress reflect everything owed. Satisfied by
// *RewardService.ProcessUserRewards (preferred: also awards crossed levels) and,
// as a fallback, any settle-only adapter.
type rewardRefresher interface {
	ProcessUserRewards(ctx context.Context, userID uuid.UUID) (RewardRunResult, error)
}

// pendingGlobalShareReader is the SP4 read surface for the not-yet-settled
// global-pool TP a user is owed. Satisfied by *FeeDistributionService.
type pendingGlobalShareReader interface {
	PendingUserGlobalShare(ctx context.Context, userID uuid.UUID) (float64, error)
}

// claimedMCGPReader reports a user's total already-claimed MCGP. Optional; the
// production wiring can adapt the on-chain claimed[] reader, but the summary is
// fully functional (claimed=0) without it.
type claimedMCGPReader interface {
	ClaimedMCGP(ctx context.Context, userID uuid.UUID) (float64, error)
}

// NewRewardSummaryService builds the SP8 read-aggregator. db is required; the
// rest are optional and nil-safe (see the type doc). Production wires refresher =
// *RewardService and pending = *FeeDistributionService.
func NewRewardSummaryService(db *gorm.DB, refresher rewardRefresher, pending pendingGlobalShareReader, claimed claimedMCGPReader) *RewardSummaryService {
	return &RewardSummaryService{db: db, refresher: refresher, pending: pending, claimedReader: claimed}
}

// ─── Response shape (the exact JSON the app's TP screen consumes) ────────────

// LevelStatus is a per-level ladder state.
const (
	// LevelStatusEarned means the user has crossed this level (tp_balance >=
	// threshold) — its reward has been or will be awarded.
	LevelStatusEarned = "earned"
	// LevelStatusInProgress is the single level the user is currently working
	// toward (the next level above their current one).
	LevelStatusInProgress = "in_progress"
	// LevelStatusLocked is a level above in_progress, not yet reachable.
	LevelStatusLocked = "locked"
)

// LadderLevel is one rung of the 15-level reward ladder in the summary.
type LadderLevel struct {
	Level       int     `json:"level"`
	Name        string  `json:"name"`
	ThresholdTP float64 `json:"thresholdTP"`
	RewardUSD   float64 `json:"rewardUsd"`
	Prize       string  `json:"prize"`
	Status      string  `json:"status"`
}

// GenerationEarning is the user's total TP earned at one generation depth
// (0 = self, 1..10 = downline). Always present for every gen 0..10 (0 when the
// user has no earnings at that depth) so the app can render a fixed 11-row chart.
type GenerationEarning struct {
	Generation int     `json:"generation"`
	EarnedTP   float64 `json:"earnedTp"`
}

// PrizeDueItem is one L6+ physical-prize obligation in the summary.
type PrizeDueItem struct {
	Level  int    `json:"level"`
	Name   string `json:"name"`
	Prize  string `json:"prize"`
	Status string `json:"status"`
}

// RewardSummary is the full GET /api/rewards/summary payload.
type RewardSummary struct {
	// TPBalance is the user's TP after folding their pending global share.
	TPBalance float64 `json:"tpBalance"`

	// ClaimableMCGP is the cumulative MCGP owed (SP5 RewardClaimable). When a
	// claimed reader is wired this is the NET still-claimable (cumulative −
	// claimed); otherwise it is the gross cumulative.
	ClaimableMCGP float64 `json:"claimableMcgp"`

	// TotalClaimedMCGP is the MCGP the user has already claimed on-chain (0 when
	// no claimed reader is wired).
	TotalClaimedMCGP float64 `json:"totalClaimedMcgp"`

	// CurrentLevel is the highest level whose threshold tp_balance meets (0 below
	// L1).
	CurrentLevel int `json:"currentLevel"`

	// CurrentLevelName is the PM-defined rank name for CurrentLevel (e.g.
	// "Ambassador" at L6), empty when CurrentLevel == 0 (below L1). Lets the app
	// hero show the rank name alongside the number.
	CurrentLevelName string `json:"currentLevelName"`

	// NextLevelThresholdTP is the threshold of the next level up (0 when already
	// at the top of the ladder).
	NextLevelThresholdTP float64 `json:"nextLevelThresholdTP"`

	// ProgressToNext is the fraction [0,1] of the way from the current level's
	// threshold to the next level's threshold. 1.0 at the top of the ladder.
	ProgressToNext float64 `json:"progressToNext"`

	// Levels is the full 15-level ladder with per-level status.
	Levels []LadderLevel `json:"levels"`

	// PerGeneration is the user's TP earned grouped by Generation, gen 0..10
	// (always all 11 rows).
	PerGeneration []GenerationEarning `json:"perGeneration"`

	// GlobalPoolShareTP is the not-yet-settled SP4 global-pool TP still owed (0
	// when none / no reader). Note: RefreshBeforeRead settles this into tp_balance
	// first, so on a refreshed read this is typically 0 — it's surfaced for the
	// app to show "global pool share" even when settlement is deferred.
	GlobalPoolShareTP float64 `json:"globalPoolShareTp"`

	// PrizesDue are the user's L6+ physical-prize obligations.
	PrizesDue []PrizeDueItem `json:"prizesDue"`
}

// ─── Build ───────────────────────────────────────────────────────────────────

// Summary builds the full reward summary for the user. It FIRST refreshes the
// user (folds pending global share + awards crossed levels) so tp_balance and the
// claimable are current, THEN reads the composed view. A refresh failure is
// non-fatal — the summary still returns from stored state (accrual is always
// visible), matching "ineligible user still sees accrual".
func (s *RewardSummaryService) Summary(ctx context.Context, userID uuid.UUID) (*RewardSummary, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reward summary service not initialized")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("reward summary: nil user id")
	}

	// Make tp_balance current before reading the level. Best-effort: a failure
	// (e.g. OTC price unavailable deferring conversions) must not hide accrual.
	if s.refresher != nil {
		if _, err := s.refresher.ProcessUserRewards(ctx, userID); err != nil {
			// Non-fatal: fall through and read whatever is stored.
			_ = err
		}
	}

	var user models.User
	if err := s.db.WithContext(ctx).
		Select("id, tp_balance").
		First(&user, "id = ?", userID).Error; err != nil {
		return nil, fmt.Errorf("load user %s: %w", userID, err)
	}

	out := &RewardSummary{TPBalance: user.TPBalance}

	// Claimable (gross cumulative owed) + already-claimed.
	claimable, err := s.claimableMCGP(ctx, userID)
	if err != nil {
		return nil, err
	}
	if s.claimedReader != nil {
		claimed, cerr := s.claimedReader.ClaimedMCGP(ctx, userID)
		if cerr != nil {
			return nil, fmt.Errorf("read claimed for %s: %w", userID, cerr)
		}
		out.TotalClaimedMCGP = claimed
		net := claimable - claimed
		if net < 0 {
			net = 0 // defensive: never report negative still-claimable
		}
		out.ClaimableMCGP = net
	} else {
		out.ClaimableMCGP = claimable
	}

	// Level + progress, from the REUSED tp_level ladder.
	out.CurrentLevel = models.LevelFor(user.TPBalance)
	out.CurrentLevelName = models.NameForLevel(out.CurrentLevel) // "" when CurrentLevel == 0
	out.NextLevelThresholdTP = nextLevelThreshold(out.CurrentLevel)
	out.ProgressToNext = progressToNext(user.TPBalance, out.CurrentLevel)
	out.Levels = buildLadder(out.CurrentLevel)

	// Per-generation grouping (always gen 0..10).
	perGen, err := s.perGeneration(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.PerGeneration = perGen

	// Pending global-pool share (read-only; nil reader ⇒ 0).
	if s.pending != nil {
		share, perr := s.pending.PendingUserGlobalShare(ctx, userID)
		if perr != nil {
			return nil, fmt.Errorf("read pending global share for %s: %w", userID, perr)
		}
		out.GlobalPoolShareTP = share
	}

	// Prizes due.
	prizes, err := s.prizesDue(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.PrizesDue = prizes

	return out, nil
}

// claimableMCGP reads the user's cumulative claimable-MCGP balance (0 when no
// reward entries exist yet).
func (s *RewardSummaryService) claimableMCGP(ctx context.Context, userID uuid.UUID) (float64, error) {
	var row models.RewardClaimable
	err := s.db.WithContext(ctx).First(&row, "user_id = ?", userID).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load claimable for %s: %w", userID, err)
	}
	return row.ClaimableMCGP, nil
}

// perGeneration groups the user's TPEarning rows by Generation and returns a row
// for EVERY generation 0..10 (0 when none), so the app renders a fixed 11-bar
// chart. Generation -1 rows (global-pool credits) are intentionally excluded —
// they are not a downline-generation earning (surfaced via GlobalPoolShareTP /
// tp_balance instead).
func (s *RewardSummaryService) perGeneration(ctx context.Context, userID uuid.UUID) ([]GenerationEarning, error) {
	type genRow struct {
		Generation int
		Total      float64
	}
	var rows []genRow
	if err := s.db.WithContext(ctx).
		Model(&models.TPEarning{}).
		Select("generation, COALESCE(SUM(tp_earned), 0) as total").
		Where("user_id = ? AND generation >= 0", userID).
		Group("generation").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("group tp earnings by generation for %s: %w", userID, err)
	}

	byGen := make(map[int]float64, len(rows))
	for _, r := range rows {
		byGen[r.Generation] += r.Total
	}

	const maxGen = 10 // gen 0 = self, 1..10 = downline (models.GenerationPercentages)
	out := make([]GenerationEarning, 0, maxGen+1)
	for g := 0; g <= maxGen; g++ {
		out = append(out, GenerationEarning{Generation: g, EarnedTP: byGen[g]})
	}
	return out, nil
}

// prizesDue returns the user's L6+ physical-prize obligations, level-ascending.
func (s *RewardSummaryService) prizesDue(ctx context.Context, userID uuid.UUID) ([]PrizeDueItem, error) {
	var rows []models.PrizeDue
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("level ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load prizes due for %s: %w", userID, err)
	}
	out := make([]PrizeDueItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, PrizeDueItem{Level: r.Level, Name: models.NameForLevel(r.Level), Prize: r.Prize, Status: r.Status})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out, nil
}

// ─── Ladder / progress helpers (all REUSE models.tp_level — no duplication) ───

// buildLadder materializes the full 15-level ladder with per-level status given
// the user's current level. Levels <= current are "earned"; current+1 is
// "in_progress"; the rest are "locked". At the top of the ladder no level is
// in_progress.
func buildLadder(currentLevel int) []LadderLevel {
	src := models.TPLevels() // defensive copy from the single source of truth
	out := make([]LadderLevel, 0, len(src))
	for _, l := range src {
		status := LevelStatusLocked
		switch {
		case l.Level <= currentLevel:
			status = LevelStatusEarned
		case l.Level == currentLevel+1:
			status = LevelStatusInProgress
		}
		out = append(out, LadderLevel{
			Level:       l.Level,
			Name:        l.Name,
			ThresholdTP: l.ThresholdTP,
			RewardUSD:   l.RewardUSD,
			Prize:       l.Prize,
			Status:      status,
		})
	}
	return out
}

// nextLevelThreshold returns the threshold of the level above currentLevel, or 0
// when already at (or above) the top of the ladder.
func nextLevelThreshold(currentLevel int) float64 {
	if currentLevel >= models.TPLevelCount {
		return 0
	}
	return models.ThresholdTPForLevel(currentLevel + 1)
}

// progressToNext returns the fraction [0,1] from the current level's threshold to
// the next level's threshold. Below L1 it measures progress from 0 → L1's
// threshold. At the top of the ladder it is 1.0.
func progressToNext(tp float64, currentLevel int) float64 {
	if currentLevel >= models.TPLevelCount {
		return 1.0
	}
	var lower float64
	if currentLevel >= 1 {
		lower = models.ThresholdTPForLevel(currentLevel)
	}
	upper := models.ThresholdTPForLevel(currentLevel + 1)
	span := upper - lower
	if span <= 0 {
		return 0
	}
	p := (tp - lower) / span
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return p
}

// ─── On-chain claimed adapter (wires totalClaimedMCGP / net claimable) ────────

// claimedOfReader is the narrow on-chain read surface the claimed adapter needs:
// claimed[account] (base units) on the distributor. *evmRewardChainReader (the
// reconciler's chain reader) satisfies it, so the same reader is reused.
type claimedOfReader interface {
	ClaimedOf(ctx context.Context, distributor, account string) (*big.Int, error)
}

// OnChainClaimedReader adapts the distributor's claimed[account] read into the
// claimedMCGPReader the summary service needs: it resolves the user's
// wallet_address from the DB, reads claimed[account] (18-dec base units) on the
// distributor, and returns it as a float MCGP. Wiring this (instead of nil) makes
// totalClaimedMCGP real and the hero claimable NET of claims (cumulative −
// claimed), so the "Reward earned" figure drops after a claim and the
// "Already claimed" row appears (fee/TP overhaul SP8 / §9).
//
// It is nil-safe and degrades to "claimed = 0" rather than failing the whole
// summary when it can't read (the chain is read-only here and a transient RPC
// blip must not hide the user's accrual — matching the dormant-by-default,
// accrual-always-visible posture). A user with no/invalid wallet address simply
// reads 0 claimed.
type OnChainClaimedReader struct {
	db          *gorm.DB
	reader      claimedOfReader
	distributor string
}

// NewOnChainClaimedReader builds the claimed adapter, returned as the
// claimedMCGPReader interface so a missing dependency propagates as a TRUE nil
// interface (the summary then falls back to gross cumulative, exactly as before)
// — avoiding the typed-nil-in-interface trap. reader is the RewardChainReader
// (which embeds ClaimedOf); a nil reader, nil db, or empty distributor all yield
// a nil interface. Keeps dev/dormant environments working.
func NewOnChainClaimedReader(db *gorm.DB, reader claimedOfReader, distributor string) claimedMCGPReader {
	distributor = strings.TrimSpace(distributor)
	// A nil RewardChainReader arrives as a typed-nil interface (non-nil ==),
	// so guard on the concrete value too.
	if db == nil || reader == nil || isNilClaimedReader(reader) || distributor == "" {
		return nil
	}
	return &OnChainClaimedReader{db: db, reader: reader, distributor: distributor}
}

// isNilClaimedReader detects a typed-nil claimedOfReader (e.g. a nil
// *evmRewardChainReader boxed into the interface) so the constructor can return a
// true nil interface instead of a live adapter wrapping a nil reader.
func isNilClaimedReader(r claimedOfReader) bool {
	switch v := r.(type) {
	case *evmRewardChainReader:
		return v == nil
	default:
		return false
	}
}

// ClaimedMCGP returns the user's total already-claimed MCGP on the distributor.
// Best-effort: a missing/invalid wallet, a not-found user, or an RPC read error
// all resolve to 0 (never an error) so a read blip can't hide the user's accrual.
func (a *OnChainClaimedReader) ClaimedMCGP(ctx context.Context, userID uuid.UUID) (float64, error) {
	if a == nil || a.db == nil || a.reader == nil {
		return 0, nil
	}
	var user models.User
	if err := a.db.WithContext(ctx).
		Select("id", "wallet_address").
		First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("load user %s for claimed read: %w", userID, err)
	}
	addr := strings.TrimSpace(user.WalletAddress)
	if addr == "" || !common.IsHexAddress(addr) {
		return 0, nil // no usable on-chain address ⇒ nothing claimed
	}
	claimed, err := a.reader.ClaimedOf(ctx, a.distributor, addr)
	if err != nil {
		// Read-only blip — degrade to 0 claimed rather than failing the summary.
		// (Worst case the hero is briefly the gross cumulative, never hidden.)
		return 0, nil
	}
	return baseUnitsToMCGP(claimed), nil
}

// baseUnitsToMCGP converts an 18-decimal MCGP base-unit big.Int into a float MCGP.
// Inverse of floorToBaseUnits; high-precision big.Float so large balances don't
// lose precision. nil / non-positive ⇒ 0.
func baseUnitsToMCGP(baseUnits *big.Int) float64 {
	if baseUnits == nil || baseUnits.Sign() <= 0 {
		return 0
	}
	bf := new(big.Float).SetPrec(256).SetInt(baseUnits)
	scale := new(big.Float).SetPrec(256).SetInt(baseUnitScale())
	f, _ := new(big.Float).Quo(bf, scale).Float64()
	return f
}
