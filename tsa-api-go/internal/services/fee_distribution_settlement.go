package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SP4 — global-pool SETTLEMENT via a cumulative-index accumulator.
//
// SP3 ACCRUES into the singleton global pool (GlobalTPPool.AccruedTP) whenever a
// fee event has a missing/inactive ancestor or no direct upline. SP4 distributes
// that pool EQUALLY across ALL REGISTERED users (deleted_at IS NULL) without an
// O(users) write per epoch, using the MasterChef accRewardPerShare pattern:
//
//   - ONE global scalar index, GlobalTPPool.PerUserIndexScaled (a scaled int64,
//     units of 1/GlobalTPIndexScale TP), is incremented once per settlement
//     epoch by floor(epochDeltaTP · scale / userCount). ONE row write per epoch.
//   - Each user stores last_settled_global_index. Owed global TP =
//     (currentIndex − userCheckpoint) / scale, credited LAZILY on
//     read/claim/settle — ZERO per-user writes per epoch.
//
// Precision: a scaled int64 (not float) because per-user shares can be tiny
// (a $0.30 pool delta over 1M users is 3e-7 TP each). Integer division floors,
// so per-epoch dust stays in the pool and the index can never over-distribute.
//
// Conservation invariant: Σ over all users of (settled global TP) ≤ total pool
// TP folded into the index. floor() per epoch + lazy crediting guarantee no
// over-distribution; the remainder (un-folded delta + per-epoch dust) stays in
// the pool. SP4 NEVER decrements GlobalTPPool.AccruedTP — it advances IndexedTP
// (the high-water mark already folded) instead, which keeps the SP3 accrual
// ledger and the SP4 settlement ledger independently auditable.

// GlobalSettlementResult summarizes one SettleGlobalPoolEpoch run.
type GlobalSettlementResult struct {
	// Settled is false for a no-op epoch (userCount == 0 or deltaTP == 0); no
	// epoch row is written in that case.
	Settled bool

	DeltaTP       float64 // pool TP folded this epoch
	UserCount     int64   // denominator (registered users)
	PerUserScaled int64   // scaled-int increment applied to the index
	IndexAfter    int64   // cumulative index after the fold
}

// SettleGlobalPoolEpoch folds the global pool's newly-accrued TP into the
// cumulative per-user index, exactly once per epoch. It is the scheduled
// settlement tick (default daily).
//
// Steps, all in ONE transaction:
//  1. Load the singleton pool row (FOR no-op if it doesn't exist yet).
//  2. deltaTP = pool.AccruedTP − pool.IndexedTP (TP accrued since the last fold).
//  3. userCount = COUNT(users WHERE deleted_at IS NULL).
//  4. NO-OP (no writes, Settled=false) if userCount == 0 OR deltaTP <= 0.
//  5. perUserScaled = floor(deltaTP · scale / userCount). Increment the index by
//     it; advance IndexedTP by EXACTLY the folded amount perUserScaled·userCount
//     (NOT the whole delta) so integer-division dust is NOT marked indexed and
//     rolls into the next epoch's delta — dust is never lost, never duplicated.
//  6. Append a GlobalTPEpoch audit row.
//
// IDEMPOTENT: re-running with no new accrual sees deltaTP == 0 and no-ops (the
// same delta is never folded twice — IndexedTP is the guard).
func (s *FeeDistributionService) SettleGlobalPoolEpoch(ctx context.Context) (GlobalSettlementResult, error) {
	if s == nil || s.db == nil {
		return GlobalSettlementResult{}, fmt.Errorf("fee distribution service not initialized")
	}

	var res GlobalSettlementResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// (1) Load the singleton pool. No row yet => nothing ever accrued => no-op.
		var pool models.GlobalTPPool
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no accrual yet; res.Settled stays false
		}
		if err != nil {
			return fmt.Errorf("load global pool: %w", err)
		}

		// (2) Delta accrued since the last fold.
		deltaTP := pool.AccruedTP - pool.IndexedTP

		// (3) Denominator: every user who is not deleted and whose account is
		// active (PM 2026-10-21: not deleted / frozen / suspended).
		var userCount int64
		if err := tx.Model(&models.User{}).
			Where("deleted_at IS NULL AND account_status = ?", models.AccountStatusActive).
			Count(&userCount).Error; err != nil {
			return fmt.Errorf("count eligible users: %w", err)
		}

		// (4) No-op guards. delta can be 0 (idempotent re-run) or slightly
		// negative from float noise; either way nothing to fold.
		if userCount == 0 || deltaTP <= 0 {
			res = GlobalSettlementResult{Settled: false, DeltaTP: deltaTP, UserCount: userCount}
			return nil
		}

		// CONVERSION GUARD: deltaTP·scale must stay inside int64. int64() of an
		// out-of-range float64 is implementation-defined and can wrap NEGATIVE,
		// which makes perUserScaled negative and slips past the cumulative-index
		// guard below (that only checks the upper bound) — silently DECREMENTING
		// every user's owed balance: the exact ledger corruption SP4 must never
		// do. Above ~9.2e6 TP/epoch deltaTP·1e12 overflows int64; above ~9007 TP
		// (2^53/scale) float64 loses integer precision and the dust-conservation
		// invariant drifts. Realistic epochs are far below either bound, so refuse
		// and alert rather than fold a corrupt value, leaving the delta in the pool.
		const maxFoldableTP = float64(math.MaxInt64) / float64(models.GlobalTPIndexScale)
		if deltaTP >= maxFoldableTP {
			log.Printf("[fee-settlement] global_delta_overflow_total=1 ALERT deltaTP=%g maxFoldableTP=%g userCount=%d (per-epoch delta would overflow int64 when scaled — refusing to fold; SP4 distribution halted until the index scale/units are remediated)",
				deltaTP, maxFoldableTP, userCount)
			return fmt.Errorf("global TP delta overflow: deltaTP=%g exceeds the foldable max=%g; refusing to scale to avoid wrapping the ledger negative", deltaTP, maxFoldableTP)
		}

		// (5) Fold. Scale to integer, floor-divide by the denominator. The dust
		// (deltaTP·scale mod userCount) is NOT advanced into IndexedTP, so it
		// rolls into the next epoch's delta — conserved, never minted.
		deltaScaled := int64(deltaTP * float64(models.GlobalTPIndexScale))
		perUserScaled := deltaScaled / userCount
		if perUserScaled <= 0 {
			// delta is positive but smaller than one scaled unit per user — too
			// tiny to fold this epoch; leave it in the pool to accumulate. No-op.
			res = GlobalSettlementResult{Settled: false, DeltaTP: deltaTP, UserCount: userCount}
			return nil
		}

		foldedScaled := perUserScaled * userCount
		foldedTP := float64(foldedScaled) / float64(models.GlobalTPIndexScale)

		// OVERFLOW GUARD: the cumulative index is a monotonically-increasing int64.
		// If pool.PerUserIndexScaled + perUserScaled would exceed math.MaxInt64 the
		// addition wraps NEGATIVE, which silently bricks SP4 forever:
		// SettleUserGlobalShare computes owedScaled = currentIndex − checkpoint, and
		// a wrapped (now-smaller, possibly negative) index makes every owed value
		// <= 0, so NO user is ever credited again — a silent ledger corruption. Refuse
		// to fold and surface a loud ops alert instead, leaving the delta in the pool
		// and the index intact. (perUserScaled > 0 here, guarded above.)
		if pool.PerUserIndexScaled > math.MaxInt64-perUserScaled {
			log.Printf("[fee-settlement] global_index_overflow_total=1 ALERT currentIndex=%d perUserScaled=%d userCount=%d (cumulative per-user index would overflow int64 — refusing to fold; SP4 distribution halted until the index scale/units are remediated)",
				pool.PerUserIndexScaled, perUserScaled, userCount)
			return fmt.Errorf("global TP index overflow: currentIndex=%d + perUserScaled=%d would exceed int64 max; refusing to advance the index to avoid wrapping the ledger negative",
				pool.PerUserIndexScaled, perUserScaled)
		}
		newIndex := pool.PerUserIndexScaled + perUserScaled

		if err := tx.Model(&models.GlobalTPPool{}).
			Where("singleton = ?", models.GlobalTPPoolSingletonID).
			Updates(map[string]interface{}{
				// Advance the index and the high-water mark by ONLY the folded
				// amount; dust stays unindexed for the next epoch.
				"per_user_index_scaled": newIndex,
				"indexed_tp":            gorm.Expr("indexed_tp + ?", foldedTP),
				"updated_at":            time.Now(),
			}).Error; err != nil {
			return fmt.Errorf("advance global index: %w", err)
		}

		// Users left out of the denominator must not claim this epoch through the
		// index, so push their checkpoint past it. They keep whatever earlier
		// epochs they were owed; only this one is skipped.
		if err := tx.Model(&models.User{}).
			Where("deleted_at IS NULL AND account_status <> ?", models.AccountStatusActive).
			Update("last_settled_global_index", gorm.Expr("COALESCE(last_settled_global_index, 0) + ?", perUserScaled)).Error; err != nil {
			return fmt.Errorf("skip ineligible users for epoch: %w", err)
		}

		// (6) Audit row for this epoch.
		now := time.Now()
		epoch := models.GlobalTPEpoch{
			ID:               uuid.New(),
			EpochAt:          now,
			DeltaTP:          foldedTP,
			UserCount:        userCount,
			PerUserScaled:    perUserScaled,
			IndexAfterScaled: newIndex,
			CreatedAt:        now,
		}
		if err := tx.Create(&epoch).Error; err != nil {
			return fmt.Errorf("write global epoch row: %w", err)
		}

		res = GlobalSettlementResult{
			Settled:       true,
			DeltaTP:       foldedTP,
			UserCount:     userCount,
			PerUserScaled: perUserScaled,
			IndexAfter:    newIndex,
		}
		return nil
	})
	if err != nil {
		return GlobalSettlementResult{}, err
	}
	return res, nil
}

// StrandedGlobalTP measures the global-pool TP owed to users who were SOFT-
// DELETED after the index folded their share but before SettleUserGlobalShare
// credited them. There is no reclamation path (deferred), so this owed amount
// is never claimed and never returned to the pool — Σcredited < IndexedTP drifts
// upward with user churn, slowly degrading the conservation invariant. This is
// an OBSERVABILITY gauge for that drift (read-only, never mutates state); the
// settler logs it each epoch so the leak is visible and bounded rather than
// silent. Returns stranded TP = Σ(currentIndex − checkpoint) over soft-deleted
// users, in TP units.
func (s *FeeDistributionService) StrandedGlobalTP(ctx context.Context) (float64, error) {
	var pool models.GlobalTPPool
	err := s.db.WithContext(ctx).First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil // no pool yet ⇒ nothing stranded
	}
	if err != nil {
		return 0, fmt.Errorf("load global pool: %w", err)
	}
	var result struct{ Stranded int64 }
	q := s.db.WithContext(ctx).Model(&models.User{}).
		Select("COALESCE(SUM(? - last_settled_global_index), 0) AS stranded", pool.PerUserIndexScaled).
		Where("deleted_at IS NOT NULL AND last_settled_global_index IS NOT NULL AND last_settled_global_index < ?",
			pool.PerUserIndexScaled)
	if err := q.Scan(&result).Error; err != nil {
		return 0, fmt.Errorf("sum stranded global TP: %w", err)
	}
	return float64(result.Stranded) / float64(models.GlobalTPIndexScale), nil
}

// SettleUserGlobalShare lazily credits one user's accumulated global-pool share.
//
//	owedScaled = currentIndex − user.last_settled_global_index
//	owedTP     = owedScaled / GlobalTPIndexScale
//
// It credits owedTP as a TPEarning (SourceType "global_pool") + tp_balance
// increment, and advances the user's checkpoint to currentIndex — all in ONE
// transaction. Returns the TP credited (0 when nothing is owed).
//
// NEW-USER FAIRNESS: a user's checkpoint is anchored AT JOIN TIME — the signup
// path (auth_handler) seeds last_settled_global_index = CurrentGlobalIndex() so
// a new user is owed 0 for every epoch before they existed and shares only
// epochs AFTER they join. A NULL checkpoint means a PRE-SP4 incumbent (created
// before this column existed): those users were present since the index began
// at 0, so NULL is treated as 0 here (they earn the full history). New users
// never have a NULL checkpoint. This split is the correctness crux of the design.
//
// IDEMPOTENT: a second call with no intervening epoch sees owed == 0 and no-ops
// (the checkpoint already equals the index). Concurrency-safe via a row lock on
// the user.
func (s *FeeDistributionService) SettleUserGlobalShare(ctx context.Context, userID uuid.UUID) (float64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("fee distribution service not initialized")
	}
	if userID == uuid.Nil {
		return 0, fmt.Errorf("settle user global share: nil user id")
	}

	var creditedTP float64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Current index (0 if the pool row doesn't exist yet).
		currentIndex, err := currentGlobalIndex(tx)
		if err != nil {
			return err
		}

		// Lock the user row so a concurrent settle can't double-credit.
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id, last_settled_global_index").
			First(&user, "id = ?", userID).Error; err != nil {
			return fmt.Errorf("load user %s: %w", userID, err)
		}

		// NULL checkpoint => pre-SP4 incumbent, present since the index began at
		// 0. (New users are seeded a non-NULL checkpoint at signup, so a NULL
		// here is never a post-SP4 newcomer.)
		checkpoint := int64(0)
		if user.LastSettledGlobalIndex != nil {
			checkpoint = *user.LastSettledGlobalIndex
		}

		owedScaled := currentIndex - checkpoint
		if owedScaled <= 0 {
			// Already current (idempotent re-call) — no-op. Defensive: a negative
			// can only arise from manual index tampering; never credit it.
			creditedTP = 0
			return nil
		}

		owedTP := float64(owedScaled) / float64(models.GlobalTPIndexScale)

		// Ledger row for the credited global-pool share.
		earning := models.TPEarning{
			ID:           uuid.New(),
			UserID:       userID,
			SourceUserID: userID, // self — the global pool has no single source user
			SourceType:   TPSourceGlobalPool,
			SourceID:     userID, // checkpoint-advance is the natural idempotency unit
			Generation:   -1,     // not a generation credit
			FeeAmountUSD: 0,
			Percentage:   0,
			TPEarned:     owedTP,
			CreatedAt:    time.Now(),
		}
		if err := tx.Create(&earning).Error; err != nil {
			return fmt.Errorf("create global-pool tp earning (user=%s): %w", userID, err)
		}

		// Advance the checkpoint AND credit tp_balance atomically.
		if err := tx.Model(&models.User{}).Where("id = ?", userID).
			Updates(map[string]interface{}{
				"tp_balance":                gorm.Expr("tp_balance + ?", owedTP),
				"last_settled_global_index": currentIndex,
			}).Error; err != nil {
			return fmt.Errorf("credit + advance user %s: %w", userID, err)
		}

		creditedTP = owedTP
		return nil
	})
	if err != nil {
		return 0, err
	}
	return creditedTP, nil
}

// PendingUserGlobalShare returns the global-pool TP a user is owed WITHOUT
// crediting it (read-only). A NULL checkpoint is a pre-SP4 incumbent and is
// treated as 0 (owed since the index origin); new users are seeded their
// join-time index at signup so they share only future epochs. Useful for the
// TP screen / claim preview.
func (s *FeeDistributionService) PendingUserGlobalShare(ctx context.Context, userID uuid.UUID) (float64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("fee distribution service not initialized")
	}

	var (
		currentIndex int64
		user         models.User
	)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if currentIndex, err = currentGlobalIndex(tx); err != nil {
			return err
		}
		return tx.Select("id, last_settled_global_index").
			First(&user, "id = ?", userID).Error
	}); err != nil {
		return 0, err
	}

	checkpoint := int64(0)
	if user.LastSettledGlobalIndex != nil {
		checkpoint = *user.LastSettledGlobalIndex
	}
	owedScaled := currentIndex - checkpoint
	if owedScaled <= 0 {
		return 0, nil
	}
	return float64(owedScaled) / float64(models.GlobalTPIndexScale), nil
}

// CurrentGlobalIndex returns the current cumulative per-user index (scaled int,
// units of 1/GlobalTPIndexScale TP), or 0 when nothing has accrued yet. Exposed
// for read-only previews/diagnostics. The SIGNUP path must NOT use this to seed
// a new user's checkpoint via a separate INSERT — that read+insert is a TOCTOU
// race against SettleGlobalPoolEpoch (the epoch can fold between the read and the
// insert, excluding the new user from the fold denominator while still seeding a
// pre-fold checkpoint → a retroactive windfall + over-distribution). Use
// CreateUserWithGlobalCheckpoint instead, which reads the index and inserts the
// user in ONE transaction that locks the same pool row the settler locks.
func (s *FeeDistributionService) CurrentGlobalIndex(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("fee distribution service not initialized")
	}
	return currentGlobalIndex(s.db.WithContext(ctx))
}

// CreateUserWithGlobalCheckpoint inserts a new user AND seeds their global-pool
// settlement checkpoint to the join-time index, atomically — closing the
// new-user-fairness / conservation TOCTOU race against SettleGlobalPoolEpoch.
//
// THE RACE (without this): signup reads CurrentGlobalIndex() (plain SELECT) then
// INSERTs the user in a SEPARATE statement. If SettleGlobalPoolEpoch folds an
// epoch between those two, that epoch's denominator (COUNT of registered users
// at fold time) does NOT include the not-yet-inserted user, yet the user is then
// persisted with the PRE-fold (lower) checkpoint. The next SettleUserGlobalShare
// computes owedScaled = currentIndex − staleCheckpoint = that epoch's
// perUserScaled > 0, so the user claims a share of an epoch they were excluded
// from. That is both a retroactive windfall AND over-distribution: IndexedTP was
// advanced by perUserScaled·userCount (excluding this user), but
// perUserScaled·(userCount + racing-newcomers) can then be claimed — Σ credited
// can exceed IndexedTP, minting value beyond the folded pool.
//
// THE FIX: do the index read and the user INSERT inside ONE transaction that
// takes a FOR UPDATE lock on the SAME singleton pool row SettleGlobalPoolEpoch
// locks. The two transactions then serialize on that row, so the user is either:
//   - inserted BEFORE the fold → counted in the denominator, checkpoint = pre-fold
//     index, owed that epoch's share they helped earn; or
//   - inserted AFTER the fold → checkpoint = post-fold index, owed 0 for that epoch.
//
// Either way the seeded checkpoint equals the index value used as the denominator
// boundary for the user's FIRST eligible epoch — the conservation/fairness
// invariant the design requires.
//
// If the singleton pool row does not exist yet (nothing has ever accrued), there
// is no epoch to race against — SettleGlobalPoolEpoch no-ops on a missing row —
// so the index is 0 and the checkpoint is left NULL (the "pre-SP4 / earn-from-
// origin" sentinel), exactly as a missing-pool read would have produced.
//
// Nil-safe: when the fee engine isn't wired this method isn't used; the caller
// inserts the user directly without a checkpoint.
func (s *FeeDistributionService) CreateUserWithGlobalCheckpoint(ctx context.Context, user *models.User) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("fee distribution service not initialized")
	}
	if user == nil {
		return fmt.Errorf("create user with global checkpoint: nil user")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the singleton pool row FOR UPDATE so no fold can interleave between
		// reading the index and inserting the user. Same row + same lock the
		// settler takes, so the two serialize. A missing row => index 0, leave the
		// checkpoint NULL (earn-from-origin sentinel) and just insert the user;
		// there is no epoch to race because the settler no-ops on a missing row.
		var pool models.GlobalTPPool
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("singleton, per_user_index_scaled").
			First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// No pool yet: index 0, checkpoint stays NULL.
		case err != nil:
			return fmt.Errorf("lock global pool for checkpoint: %w", err)
		default:
			idx := pool.PerUserIndexScaled
			user.LastSettledGlobalIndex = &idx
		}

		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return nil
	})
}

// currentGlobalIndex reads the singleton pool's cumulative index within tx,
// returning 0 when the pool row doesn't exist yet.
func currentGlobalIndex(tx *gorm.DB) (int64, error) {
	var pool models.GlobalTPPool
	err := tx.Select("singleton, per_user_index_scaled").
		First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read global index: %w", err)
	}
	return pool.PerUserIndexScaled, nil
}

// TPSourceGlobalPool is the TPEarning.SourceType recorded for a credited
// global-pool share (SP4 lazy settlement).
const TPSourceGlobalPool = "global_pool"

// PayoutLocation is the zone payouts are scheduled in. Fixed-offset WAT: Lagos
// has no daylight saving, and this avoids depending on tzdata being installed.
var PayoutLocation = time.FixedZone("WAT", 60*60)

// LastPayoutInstant returns the most recent payout moment (payoutDay of a month,
// 00:00 WAT) at or before now.
func LastPayoutInstant(now time.Time, payoutDay int) time.Time {
	n := now.In(PayoutLocation)
	t := time.Date(n.Year(), n.Month(), payoutDay, 0, 0, 0, 0, PayoutLocation)
	if t.After(n) {
		t = time.Date(n.Year(), n.Month()-1, payoutDay, 0, 0, 0, 0, PayoutLocation)
	}
	return t
}

// SettleGlobalPoolIfDue is the monthly payout tick (PM 2026-10-21: the pool is
// paid on the 24th). It folds the pool into the per-user index only when the
// latest payout moment has not been paid yet, and is safe to call every hour:
// a restart or an outage over the 24th is made up by the next tick.
//
// The first call after rollout only stamps the current window (no fold), so the
// pool that exists at rollout carries into the next payout instead of paying out
// on deploy day.
func (s *FeeDistributionService) SettleGlobalPoolIfDue(ctx context.Context, now time.Time) (GlobalSettlementResult, error) {
	if s == nil || s.db == nil {
		return GlobalSettlementResult{}, fmt.Errorf("fee distribution service not initialized")
	}
	due := LastPayoutInstant(now, s.TPProgram().PayoutDay)

	var pool models.GlobalTPPool
	err := s.db.WithContext(ctx).First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return GlobalSettlementResult{}, nil // nothing has accrued yet
	}
	if err != nil {
		return GlobalSettlementResult{}, fmt.Errorf("load global pool: %w", err)
	}
	if pool.LastPayoutAt == nil {
		return GlobalSettlementResult{}, s.stampPayout(ctx, due)
	}
	if !pool.LastPayoutAt.Before(due) {
		return GlobalSettlementResult{}, nil // this month's payout already ran
	}
	// One payout per calendar month, whatever the payout day is changed to. Without
	// this, moving the day later after the 24th has paid would pay again on the new
	// day. (Moving it earlier in a month not yet paid does pay on the next tick,
	// which is what the new schedule says.)
	if last, d := pool.LastPayoutAt.In(PayoutLocation), due.In(PayoutLocation); last.Year() == d.Year() && last.Month() == d.Month() {
		return GlobalSettlementResult{}, nil
	}

	res, err := s.SettleGlobalPoolEpoch(ctx)
	if err != nil {
		return GlobalSettlementResult{}, err
	}
	// A fold that no-oped (empty pool, too small to split) still counts as this
	// month's payout: the next one is the next payout day, not the next tick.
	if err := s.stampPayout(ctx, due); err != nil {
		return res, err
	}
	return res, nil
}

func (s *FeeDistributionService) stampPayout(ctx context.Context, at time.Time) error {
	if err := s.db.WithContext(ctx).Model(&models.GlobalTPPool{}).
		Where("singleton = ?", models.GlobalTPPoolSingletonID).
		Update("last_payout_at", at).Error; err != nil {
		return fmt.Errorf("stamp payout window: %w", err)
	}
	return nil
}
