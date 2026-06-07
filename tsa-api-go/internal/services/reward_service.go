package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RewardService is the payout-COMPUTATION engine (fee/TP overhaul SP5). It turns
// a user's accrued TP + cashback (produced by SP3/SP4) into a per-user CLAIMABLE
// MCGP balance that SP6's merkle distributor publishes on-chain. It NEVER moves
// MCGP itself — it only computes & locks what is owed.
//
// ProcessUserRewards is the single idempotent entry point. It:
//
//	(a) settles the user's pending global-pool share (SP4) so tp_balance is
//	    current BEFORE the level is computed;
//	(b) computes the current reward level from tp_balance;
//	(c) for every level newly crossed above the user's recorded highest level,
//	    records a CUMULATIVE level reward (all skipped levels paid once),
//	    converting rewardUSD→MCGP at the CURRENT OTC price LOCKED NOW;
//	(d) converts each not-yet-converted CashbackEarning (USD)→MCGP, once per row;
//	(e) for L6+ crossings, records a prize_due row + emits an admin-notify event
//	    (manual fulfilment — prizes are NEVER auto-paid);
//	and tracks the user's highest awarded level.
//
// OTC-PRICE SAFETY (the money-critical rule): the MCGP price can be UNAVAILABLE
// (per the testnet MCGP-OTC-reverts memory). A zero/garbage rate must NEVER be
// locked. When the price is unavailable a conversion is DEFERRED (no entry
// written, the level is NOT advanced past the un-converted reward, the cashback
// row is left un-converted) and retried on the next run. The level checkpoint
// advances only over levels that were successfully converted, so a price outage
// can never skip a reward.
//
// Everything is FLAG-GATED upstream (the periodic job and call sites check
// cfg.IsFeeModelV2Enabled()); the service itself is pure and flag-agnostic.
type RewardService struct {
	db  *gorm.DB
	bus *events.Bus

	// settler provides SP4's SettleUserGlobalShare so a user's pending global
	// share is folded into tp_balance before the level is computed. An interface
	// keeps RewardService unit-testable without the full fee engine.
	settler globalShareSettler

	// price returns the current OTC MCGP price in USD per 1 MCGP. Production
	// wires the otc_service adapter; tests inject a mock. It returns
	// ErrMCGPPriceUnavailable (or any error) when the price cannot be obtained —
	// the engine then DEFERS the conversion rather than locking a bad rate.
	price mcgpPriceFunc
}

// globalShareSettler is the SP4 surface RewardService needs: lazily credit a
// user's accumulated global-pool TP into tp_balance and return what was credited.
type globalShareSettler interface {
	SettleUserGlobalShare(ctx context.Context, userID uuid.UUID) (float64, error)
}

// mcgpPriceFunc returns the current OTC buy price as USD per 1 MCGP. It MUST
// return a non-nil error (e.g. ErrMCGPPriceUnavailable) when the price is
// unavailable so the caller defers — it must never return a zero/garbage rate
// with a nil error.
type mcgpPriceFunc func(ctx context.Context) (float64, error)

// ErrMCGPPriceUnavailable signals the OTC price could not be obtained (RPC
// revert, contract returned 0, client absent). The engine treats it — and any
// other price error — as "defer this conversion, retry next run".
var ErrMCGPPriceUnavailable = errors.New("mcgp otc price unavailable")

// NewRewardService builds the payout-computation engine. price MUST be non-nil
// in production; a nil price is treated as permanently unavailable (every
// conversion defers) so a mis-wire degrades safely instead of locking a zero
// rate.
func NewRewardService(db *gorm.DB, bus *events.Bus, settler globalShareSettler, price mcgpPriceFunc) *RewardService {
	return &RewardService{db: db, bus: bus, settler: settler, price: price}
}

// otcBuyPricePerToken is the subset of *OTCService RewardService needs to build
// its price adapter. Kept as an interface so the adapter is testable and the
// service does not hard-depend on the on-chain client.
type otcBuyPricePerToken interface {
	GetBuyPricePerToken() (*big.Int, error)
}

// MCGPPriceFromOTC adapts an OTC service into the mcgpPriceFunc the engine
// expects. GetBuyPricePerToken returns USDC (6-decimal) per 1 MCGP; the USD
// price is priceWei/1e6 (matching wallet_handler / checkout_handler). A nil OTC
// service, an error, or a non-positive price all surface as
// ErrMCGPPriceUnavailable so the engine DEFERS rather than locking a bad rate.
func MCGPPriceFromOTC(otc otcBuyPricePerToken) mcgpPriceFunc {
	return func(_ context.Context) (float64, error) {
		if otc == nil {
			return 0, ErrMCGPPriceUnavailable
		}
		priceWei, err := otc.GetBuyPricePerToken()
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrMCGPPriceUnavailable, err)
		}
		if priceWei == nil || priceWei.Sign() <= 0 {
			return 0, ErrMCGPPriceUnavailable
		}
		// USDC is 6-decimal; price in USD per MCGP = priceWei / 1e6.
		usd := new(big.Float).Quo(new(big.Float).SetInt(priceWei), big.NewFloat(1e6))
		f, _ := usd.Float64()
		if f <= 0 {
			return 0, ErrMCGPPriceUnavailable
		}
		return f, nil
	}
}

// RewardRunResult summarizes one ProcessUserRewards run (for logs / the periodic
// job / tests).
type RewardRunResult struct {
	// CurrentLevel is the user's level after settling the pending global share.
	CurrentLevel int
	// LevelsAwarded is how many new level rewards were recorded this run.
	LevelsAwarded int
	// CashbacksConverted is how many cashback rows were converted this run.
	CashbacksConverted int
	// PrizesRecorded is how many L6+ prize_due rows were recorded this run.
	PrizesRecorded int
	// MCGPCredited is the total MCGP added to the claimable balance this run.
	MCGPCredited float64
	// Deferred is true when at least one conversion (level reward or cashback)
	// was deferred because the OTC price was unavailable — the run did partial
	// work and should be retried (the user is left dirty).
	Deferred bool
}

// ProcessUserRewards is the single idempotent entry point. See the type doc for
// the full flow. Safe to re-run: settled levels/cashbacks are guarded by unique
// keys, so a second run with no new TP/cashback awards nothing new.
//
// The OTC price is fetched ONCE per run (outside the write tx) and locked for
// every conversion this run; if it is unavailable, ALL conversions defer and the
// user stays dirty for the next run. The pending-global-share settlement (a)
// always runs even when the price is unavailable, so tp_balance stays current.
func (s *RewardService) ProcessUserRewards(ctx context.Context, userID uuid.UUID) (RewardRunResult, error) {
	var res RewardRunResult
	if s == nil || s.db == nil {
		return res, fmt.Errorf("reward service not initialized")
	}
	if userID == uuid.Nil {
		return res, fmt.Errorf("process user rewards: nil user id")
	}

	// (a) Settle the user's pending global-pool share FIRST so tp_balance
	// reflects everything they are owed before we compute the level. SP4's
	// SettleUserGlobalShare is itself idempotent.
	if s.settler != nil {
		if _, err := s.settler.SettleUserGlobalShare(ctx, userID); err != nil {
			return res, fmt.Errorf("settle global share for %s: %w", userID, err)
		}
	}

	// Resolve the OTC price ONCE for this run. A failure here does NOT abort the
	// run — it just means every conversion defers (priceOK=false). We still want
	// to advance bookkeeping that needs no conversion (none here) and leave the
	// user dirty for retry.
	priceUSD, priceErr := s.fetchPrice(ctx)
	priceOK := priceErr == nil
	if !priceOK {
		log.Printf("[reward-v2] ALERT reward_v2_price_unavailable_total=1 OTC MCGP price unavailable for user=%s — "+
			"deferring conversions (no rate locked), will retry: %v", userID, priceErr)
	}

	// Collect level-up events to publish AFTER the tx commits (don't emit on a
	// rolled-back tx).
	var levelUps []events.Event
	var prizeEvents []events.Event

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Load + lock the user's tp_balance and reward state together.
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id, tp_balance").
			First(&user, "id = ?", userID).Error; err != nil {
			return fmt.Errorf("load user %s: %w", userID, err)
		}

		state, err := s.loadOrInitRewardState(tx, userID)
		if err != nil {
			return err
		}

		// (b) Current level from the now-current tp_balance.
		currentLevel := models.LevelFor(user.TPBalance)
		res.CurrentLevel = currentLevel

		// (c) Award every newly-crossed level in (highest, current], cumulative.
		// If the price is unavailable we cannot lock a rate, so we award NOTHING
		// this run (defer) and leave the checkpoint where it is — the next run
		// with a live price awards all of them.
		newHighest := state.HighestAwardedLevel
		if currentLevel > state.HighestAwardedLevel {
			if !priceOK {
				res.Deferred = true
			} else {
				for lvl := state.HighestAwardedLevel + 1; lvl <= currentLevel; lvl++ {
					credited, ev, prizeEv, err := s.recordLevelReward(tx, userID, lvl, priceUSD)
					if err != nil {
						return err
					}
					res.LevelsAwarded++
					res.MCGPCredited += credited
					newHighest = lvl
					if ev != nil {
						levelUps = append(levelUps, *ev)
					}
					if prizeEv != nil {
						prizeEvents = append(prizeEvents, *prizeEv)
						res.PrizesRecorded++
					}
				}
			}
		}

		// (d) Convert not-yet-converted cashback rows (one entry per row). Each
		// conversion needs the price; if unavailable, defer ALL of them.
		if priceOK {
			converted, mcgp, err := s.convertPendingCashback(tx, userID, priceUSD)
			if err != nil {
				return err
			}
			res.CashbacksConverted = converted
			res.MCGPCredited += mcgp
		} else {
			// Only mark deferred if there is actually pending cashback to convert.
			pending, err := s.hasPendingCashback(tx, userID)
			if err != nil {
				return err
			}
			if pending {
				res.Deferred = true
			}
		}

		// Advance the checkpoint + mark processed. The user stays dirty when any
		// conversion deferred so the periodic job re-processes them.
		now := time.Now()
		state.HighestAwardedLevel = newHighest
		state.LastProcessedAt = &now
		state.Dirty = res.Deferred
		state.UpdatedAt = now
		if err := tx.Save(&state).Error; err != nil {
			return fmt.Errorf("save reward state for %s: %w", userID, err)
		}

		return nil
	})
	if err != nil {
		return RewardRunResult{}, err
	}

	// Publish events post-commit (best-effort; the bus drops on a full buffer).
	for _, ev := range levelUps {
		s.publish(ev)
	}
	for _, ev := range prizeEvents {
		s.publish(ev)
	}

	return res, nil
}

// fetchPrice calls the injected price function, normalizing a nil price function
// to "unavailable" so a mis-wire defers rather than panics.
func (s *RewardService) fetchPrice(ctx context.Context) (float64, error) {
	if s.price == nil {
		return 0, ErrMCGPPriceUnavailable
	}
	usd, err := s.price(ctx)
	if err != nil {
		return 0, err
	}
	if usd <= 0 {
		// Defensive: a price function that violates its contract by returning a
		// non-positive price with a nil error must NOT lock a zero rate.
		return 0, ErrMCGPPriceUnavailable
	}
	return usd, nil
}

// loadOrInitRewardState loads the user's UserRewardState, creating a zero-value
// row (highest level 0) on first sight. Locked FOR UPDATE within the tx so two
// concurrent ProcessUserRewards calls can't double-award.
func (s *RewardService) loadOrInitRewardState(tx *gorm.DB, userID uuid.UUID) (models.UserRewardState, error) {
	var state models.UserRewardState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&state, "user_id = ?", userID).Error
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return state, fmt.Errorf("load reward state for %s: %w", userID, err)
	}
	// First sight — create the checkpoint row.
	state = models.UserRewardState{
		UserID:              userID,
		HighestAwardedLevel: 0,
		Dirty:               false,
		UpdatedAt:           time.Now(),
	}
	if err := tx.Create(&state).Error; err != nil {
		return state, fmt.Errorf("init reward state for %s: %w", userID, err)
	}
	return state, nil
}

// recordLevelReward records ONE cumulative level reward (idempotent per
// (user, level)) at the locked price, bumps the cumulative claimable balance,
// records a prize_due row for L6+, and returns the MCGP credited + the level-up
// and (optional) prize events to publish post-commit.
//
// priceUSD MUST be > 0 (the caller never calls this when the price is
// unavailable). MCGP = rewardUSD / priceUSD, locked now.
func (s *RewardService) recordLevelReward(tx *gorm.DB, userID uuid.UUID, level int, priceUSD float64) (float64, *events.Event, *events.Event, error) {
	rewardUSD := models.RewardUSDForLevel(level)
	mcgp := rewardUSD / priceUSD

	entry := models.RewardEntry{
		ID:             uuid.New(),
		UserID:         userID,
		Kind:           models.RewardKindLevel,
		SourceLevel:    level,
		SourceID:       uuid.Nil,
		RewardUSD:      rewardUSD,
		MCGPAmount:     mcgp,
		RateUSDPerMCGP: priceUSD,
		CreatedAt:      time.Now(),
	}
	// Idempotent insert: a duplicate (user, kind=level, level) is a clean no-op.
	res := tx.Clauses(clause.OnConflict{Columns: idemColumns(), DoNothing: true}).Create(&entry)
	if res.Error != nil {
		return 0, nil, nil, fmt.Errorf("insert level reward (user=%s level=%d): %w", userID, level, res.Error)
	}
	if res.RowsAffected == 0 {
		// Already recorded this level (e.g. a prior partial run). No-op — do not
		// double-credit the balance or re-emit events.
		return 0, nil, nil, nil
	}

	if err := s.addClaimable(tx, userID, mcgp); err != nil {
		return 0, nil, nil, err
	}

	prize := models.PrizeForLevel(level)
	levelUp := events.Event{
		Type:    events.TPLevelUp,
		UserID:  userID,
		Title:   fmt.Sprintf("You reached Level %d!", level),
		Message: levelUpMessage(level, mcgp, prize),
		Data: map[string]interface{}{
			"level":          level,
			"rewardUsd":      rewardUSD,
			"mcgpAmount":     mcgp,
			"rateUsdPerMcgp": priceUSD,
			"prize":          prize,
		},
	}

	var prizeEvent *events.Event
	if models.LevelHasPhysicalPrize(level) {
		ev, err := s.recordPrizeDue(tx, userID, level, prize)
		if err != nil {
			return 0, nil, nil, err
		}
		prizeEvent = ev
	}

	return mcgp, &levelUp, prizeEvent, nil
}

// levelUpMessage composes the user-facing body for a TPLevelUp notification
// (in-app + push + email all read Event.Message). It names the MCGP reward the
// level unlocked, and the physical prize for L6+ rungs. Kept here so the
// reward-engine owns the copy and the generic notification dispatcher (the "*"
// subscriber) can deliver it unchanged across in-app / WS / push / email.
func levelUpMessage(level int, mcgp float64, prize string) string {
	base := fmt.Sprintf("You crossed Level %d and earned %.4f MCGP, claimable from your rewards screen.", level, mcgp)
	if prize != "" {
		base += fmt.Sprintf(" You also unlocked a prize: %s.", prize)
	}
	return base
}

// recordPrizeDue records a manual-fulfilment physical-prize obligation
// (idempotent per (user, level)) and returns the admin-notify event to publish.
// Returns a nil event when the row already existed (no duplicate notify).
func (s *RewardService) recordPrizeDue(tx *gorm.DB, userID uuid.UUID, level int, prize string) (*events.Event, error) {
	row := models.PrizeDue{
		ID:        uuid.New(),
		UserID:    userID,
		Level:     level,
		Prize:     prize,
		Status:    models.PrizeDueStatusPending,
		CreatedAt: time.Now(),
	}
	res := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "level"}},
		DoNothing: true,
	}).Create(&row)
	if res.Error != nil {
		return nil, fmt.Errorf("insert prize due (user=%s level=%d): %w", userID, level, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil // already recorded — no duplicate admin notify
	}
	ev := events.Event{
		Type:   events.TPPrizeDue,
		UserID: userID,
		Title:  "Physical prize due",
		Data: map[string]interface{}{
			"level": level,
			"prize": prize,
		},
	}
	return &ev, nil
}

// convertPendingCashback converts every CashbackEarning row for the user that
// does not yet have a RewardEntry (kind=cashback, source_id=row.id) into MCGP at
// the locked price, one entry per row (idempotent per row). Returns the count
// converted and the MCGP credited.
func (s *RewardService) convertPendingCashback(tx *gorm.DB, userID uuid.UUID, priceUSD float64) (int, float64, error) {
	var pending []models.CashbackEarning
	if err := tx.
		Where("user_id = ?", userID).
		Where("id NOT IN (?)",
			tx.Model(&models.RewardEntry{}).
				Select("source_id").
				Where("user_id = ? AND kind = ?", userID, models.RewardKindCashback),
		).
		Order("created_at ASC").
		Find(&pending).Error; err != nil {
		return 0, 0, fmt.Errorf("load pending cashback for %s: %w", userID, err)
	}

	var (
		count int
		total float64
	)
	for _, cb := range pending {
		if cb.AmountUSD <= 0 {
			continue
		}
		mcgp := cb.AmountUSD / priceUSD
		entry := models.RewardEntry{
			ID:             uuid.New(),
			UserID:         userID,
			Kind:           models.RewardKindCashback,
			SourceLevel:    0,
			SourceID:       cb.ID,
			RewardUSD:      cb.AmountUSD,
			MCGPAmount:     mcgp,
			RateUSDPerMCGP: priceUSD,
			CreatedAt:      time.Now(),
		}
		res := tx.Clauses(clause.OnConflict{Columns: idemColumns(), DoNothing: true}).Create(&entry)
		if res.Error != nil {
			return 0, 0, fmt.Errorf("insert cashback reward (user=%s cb=%s): %w", userID, cb.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			continue // already converted (race / partial prior run)
		}
		if err := s.addClaimable(tx, userID, mcgp); err != nil {
			return 0, 0, err
		}
		count++
		total += mcgp
	}
	return count, total, nil
}

// hasPendingCashback reports whether the user has any positive CashbackEarning
// row not yet converted to a RewardEntry — used to decide whether a
// price-unavailable run actually deferred work (and must stay dirty).
func (s *RewardService) hasPendingCashback(tx *gorm.DB, userID uuid.UUID) (bool, error) {
	var n int64
	err := tx.Model(&models.CashbackEarning{}).
		Where("user_id = ? AND amount_usd > 0", userID).
		Where("id NOT IN (?)",
			tx.Model(&models.RewardEntry{}).
				Select("source_id").
				Where("user_id = ? AND kind = ?", userID, models.RewardKindCashback),
		).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("count pending cashback for %s: %w", userID, err)
	}
	return n > 0, nil
}

// addClaimable bumps the per-user cumulative claimable-MCGP balance by mcgp,
// creating the row on first credit. Upsert so the running sum is correct on both
// PostgreSQL and SQLite.
func (s *RewardService) addClaimable(tx *gorm.DB, userID uuid.UUID, mcgp float64) error {
	row := models.RewardClaimable{
		UserID:        userID,
		ClaimableMCGP: mcgp,
		UpdatedAt:     time.Now(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"claimable_mcgp": gorm.Expr("reward_claimables.claimable_mcgp + ?", mcgp),
			"updated_at":     time.Now(),
		}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("bump claimable for %s: %w", userID, err)
	}
	return nil
}

// publish emits an event on the bus when one is wired (no-op otherwise).
func (s *RewardService) publish(ev events.Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ev)
}

// idemColumns is the composite unique key for RewardEntry idempotency.
func idemColumns() []clause.Column {
	return []clause.Column{
		{Name: "user_id"}, {Name: "kind"}, {Name: "source_level"}, {Name: "source_id"},
	}
}

// ClaimableMCGP returns the user's current cumulative claimable-MCGP balance
// (0 when no rewards have been recorded yet). Read-only; SP6 reads this to build
// its merkle root.
func (s *RewardService) ClaimableMCGP(ctx context.Context, userID uuid.UUID) (float64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("reward service not initialized")
	}
	var row models.RewardClaimable
	err := s.db.WithContext(ctx).First(&row, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.ClaimableMCGP, nil
}

// MarkUserDirty flags a user as having recent TP/cashback activity so the
// periodic job (RewardProcessorJob) will process them on its next tick — without
// scanning all users. Called by the accrual path (SP3 OnFeeSettled / SP4
// settlement) when a user's tp_balance or cashback changes. Idempotent: an
// already-dirty user stays dirty. Best-effort and self-healing — a dropped mark
// just delays a reward to the next genuine activity, never loses it, and the
// reward computation itself is idempotent.
func (s *RewardService) MarkUserDirty(ctx context.Context, userID uuid.UUID) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("reward service not initialized")
	}
	if userID == uuid.Nil {
		return nil
	}
	row := models.UserRewardState{
		UserID:    userID,
		Dirty:     true,
		UpdatedAt: time.Now(),
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"dirty":      true,
			"updated_at": time.Now(),
		}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("mark user dirty %s: %w", userID, err)
	}
	return nil
}

// DirtyUserIDs returns up to limit user ids flagged dirty (recent activity),
// oldest-updated first so processing is roughly FIFO. The periodic job uses this
// to process only users with new activity instead of a blind full-table scan.
func (s *RewardService) DirtyUserIDs(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reward service not initialized")
	}
	if limit <= 0 {
		limit = 500
	}
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.UserRewardState{}).
		Where("dirty = ?", true).
		Order("updated_at ASC").
		Limit(limit).
		Pluck("user_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("list dirty users: %w", err)
	}
	return ids, nil
}

// ProcessDirtyUsers runs ProcessUserRewards over every currently-dirty user (up
// to limit per call). It is the body of the periodic job. Per-user errors are
// logged and skipped so one bad user can't stall the batch. Returns how many
// users were processed and how many errored.
func (s *RewardService) ProcessDirtyUsers(ctx context.Context, limit int) (processed, failed int, err error) {
	ids, err := s.DirtyUserIDs(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return processed, failed, ctx.Err()
		}
		if _, perr := s.ProcessUserRewards(ctx, id); perr != nil {
			failed++
			log.Printf("[reward-v2] ProcessUserRewards failed for user=%s: %v", id, perr)
			continue
		}
		processed++
	}
	return processed, failed, nil
}
