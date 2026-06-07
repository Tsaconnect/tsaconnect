package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FeeDistributionService is the v2 fee/TP accrual engine (SP3 of the fee/TP
// overhaul). It redefines how a settled platform fee is accounted off-chain:
//
//	system  = 50% · FeeUSD  (treasury retains; recorded only)
//	buyer   = 10% · FeeUSD  (buyer cashback, USD)
//	upline  = 10% · FeeUSD  (direct gen-1 cashback, USD; → global pool if none)
//	tpPool  = 30% · FeeUSD  (distributed gen0..gen10 by GenerationPercentages)
//
// The 30% TP pool is split across gen0 (the transacting user) and gen1..gen10
// uplines using the EXISTING models.GenerationPercentages ladder (gen0 35.4%
// … gen10 0.4%; handlers.GenerationPercentages aliases the same slice). A
// generation with NO live ancestor (chain shorter than 10, or
// an inactive/suspended/deleted ancestor) routes its share to the GLOBAL POOL
// and the climb CONTINUES (route-and-continue — the key behavior change from
// the legacy break-on-inactive walk). TP credited = shareUSD · TPPerUSD.
//
// Everything for a single fee event happens in ONE DB transaction and is
// idempotent per (SourceType, SourceID): a FeeDistribution guard row with a
// unique index makes double-distribution impossible.
//
// This engine is purely additive and FLAG-GATED (cfg.IsFeeModelV2Enabled).
// While the flag is off the legacy handlers.DistributeTPEarnings path stays
// live and OnFeeSettled is never invoked. Call sites must check the flag.
type FeeDistributionService struct {
	db    *gorm.DB
	graph *ReferralGraphService

	// tpPerUSD is the minting-rate constant captured at construction. Default
	// 1.0 (1 TP = $1 of pool value); tunable via config so level pacing can be
	// retuned without code changes.
	tpPerUSD float64
}

// NewFeeDistributionService builds the engine. tpPerUSD <= 0 falls back to 1.0
// so the service always has a sane minting rate (mirrors
// config.TPPerUSDOrDefault).
func NewFeeDistributionService(db *gorm.DB, graph *ReferralGraphService, tpPerUSD float64) *FeeDistributionService {
	if tpPerUSD <= 0 {
		tpPerUSD = 1.0
	}
	if graph == nil {
		graph = NewReferralGraphService(db)
	}
	return &FeeDistributionService{db: db, graph: graph, tpPerUSD: tpPerUSD}
}

// TPPerUSD exposes the minting rate in effect (for tests / diagnostics).
func (s *FeeDistributionService) TPPerUSD() float64 { return s.tpPerUSD }

// Split constants for the uniform 50 / 10 / 10 / 30 fee accounting (design §2).
const (
	SystemShare         = 0.50
	BuyerCashbackShare  = 0.10
	UplineCashbackShare = 0.10
	TPPoolShare         = 0.30

	// MaxTPGenerations is the number of upline generations the TP pool reaches
	// beyond gen0 (the transacting user). gen0 + gen1..gen10.
	MaxTPGenerations = 10
)

// FeeEvent is the input to the engine: one settled platform fee. Stage 2 (the
// hook-up across checkout / service / swap / withdrawal / P2P release) builds
// these and calls OnFeeSettled.
//
//	Feature    — fee-feature key (models.FeatureProduct, FeatureP2P, …).
//	Token      — settlement token symbol (for audit; rates already applied).
//	FeeUSD     — total fee F in USD; the 50/10/10/30 split is taken from this.
//	UserID     — the transacting user (gen0; buyer cashback recipient).
//	SourceType — the originating domain ("checkout", "p2p_release", "swap", …).
//	SourceID   — the originating record id; (SourceType, SourceID) is the
//	             idempotency key — distributing twice for the same pair is a
//	             no-op the second time.
type FeeEvent struct {
	Feature    string
	Token      string
	FeeUSD     float64
	UserID     uuid.UUID
	SourceType string
	SourceID   uuid.UUID
}

// FeeSplit is the computed 50/10/10/30 breakdown of a FeeEvent, all in USD.
type FeeSplit struct {
	SystemUSD         float64
	BuyerCashbackUSD  float64
	UplineCashbackUSD float64
	TPPoolUSD         float64
}

// Total returns the sum of the four components — must equal the source FeeUSD.
func (sp FeeSplit) Total() float64 {
	return sp.SystemUSD + sp.BuyerCashbackUSD + sp.UplineCashbackUSD + sp.TPPoolUSD
}

// ComputeSplit returns the 50/10/10/30 breakdown of feeUSD.
func ComputeSplit(feeUSD float64) FeeSplit {
	return FeeSplit{
		SystemUSD:         feeUSD * SystemShare,
		BuyerCashbackUSD:  feeUSD * BuyerCashbackShare,
		UplineCashbackUSD: feeUSD * UplineCashbackShare,
		TPPoolUSD:         feeUSD * TPPoolShare,
	}
}

// errAlreadyDistributed is the sentinel used inside the transaction to bail out
// idempotently when a guard row already exists. It is swallowed by OnFeeSettled
// (a duplicate call is a successful no-op, not an error).
var errAlreadyDistributed = errors.New("fee already distributed for source")

// OnFeeSettled distributes one settled fee synchronously, in a single DB
// transaction, idempotently per (SourceType, SourceID). This is the core fn;
// EnqueueFeeSettled is the async wrapper that keeps it off the request hot path.
//
// Steps:
//  0. Resolve uplines gen1..gen10 (ReferralGraphService.Uplines — full path,
//     inactive ancestors flagged not dropped). Read OUTSIDE the write
//     transaction: the upline graph is not mutated by distribution, and the
//     recursive-CTE read must not contend with the write tx for the connection
//     (a single-connection pool would otherwise deadlock — tx holds the conn,
//     the nested read waits for one forever).
//
// Then, all inside ONE write tx:
//  1. Insert the FeeDistribution guard row. The unique (source_type, source_id)
//     index makes a concurrent/duplicate call fail here → treated as a no-op.
//  2. Distribute the 30% TP pool: gen0 → the user; gen i → the active ancestor
//     at that gen if present, else → global pool. Continue past gaps.
//  3. Buyer cashback (10%) → the user's CashbackEarning. Upline cashback (10%)
//     → the gen-1 ancestor if present+active, else → global pool.
//  4. System 50% is recorded on the guard row only (treasury retains it).
func (s *FeeDistributionService) OnFeeSettled(ctx context.Context, ev FeeEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("fee distribution service not initialized")
	}
	if ev.FeeUSD <= 0 {
		// Nothing to distribute; not an error (e.g. MCGP-exempt fee path).
		return nil
	}
	if ev.SourceID == uuid.Nil {
		return fmt.Errorf("fee event missing source id (type=%q)", ev.SourceType)
	}

	// (0) Resolve the upline path once, before the write tx (see doc comment).
	// Inactive ancestors are returned flagged Active=false so we route-and-
	// continue. Index by generation for O(1) lookup; a gen is "live" only if an
	// ancestor exists at it AND is active.
	uplines, err := s.graph.Uplines(ctx, ev.UserID, MaxTPGenerations)
	if err != nil {
		return fmt.Errorf("resolve uplines for %s: %w", ev.UserID, err)
	}
	byGen := make(map[int]Ancestor, len(uplines))
	for _, a := range uplines {
		byGen[a.Generation] = a
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		split := ComputeSplit(ev.FeeUSD)

		// (1) Idempotency guard. ON CONFLICT DO NOTHING then check RowsAffected
		// so a duplicate is a clean no-op on both PostgreSQL and SQLite.
		guard := models.FeeDistribution{
			ID:                uuid.New(),
			SourceType:        ev.SourceType,
			SourceID:          ev.SourceID,
			UserID:            ev.UserID,
			Feature:           ev.Feature,
			Token:             ev.Token,
			FeeUSD:            ev.FeeUSD,
			SystemUSD:         split.SystemUSD,
			BuyerCashbackUSD:  split.BuyerCashbackUSD,
			UplineCashbackUSD: split.UplineCashbackUSD,
			TPPoolUSD:         split.TPPoolUSD,
			TPPerUSD:          s.tpPerUSD,
			CreatedAt:         time.Now(),
		}
		res := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_type"}, {Name: "source_id"}},
			DoNothing: true,
		}).Create(&guard)
		if res.Error != nil {
			return fmt.Errorf("insert fee guard (%s/%s): %w", ev.SourceType, ev.SourceID, res.Error)
		}
		if res.RowsAffected == 0 {
			// Already distributed — idempotent no-op.
			return errAlreadyDistributed
		}

		// (2) Distribute the 30% TP pool across gen0..gen10.
		var globalPoolUSD float64
		for gen := 0; gen <= MaxTPGenerations; gen++ {
			pct := models.GenerationPercentages[gen]
			shareUSD := split.TPPoolUSD * pct
			if shareUSD <= 0 {
				continue
			}

			if gen == 0 {
				// gen0 is always the transacting user.
				if err := s.creditTP(tx, ev.UserID, ev, gen, pct, shareUSD); err != nil {
					return err
				}
				continue
			}

			anc, ok := byGen[gen]
			if ok && anc.Active {
				if err := s.creditTP(tx, anc.UserID, ev, gen, pct, shareUSD); err != nil {
					return err
				}
				continue
			}

			// No live ancestor at this generation: route the share to the
			// global pool and CONTINUE (do not stop the climb).
			if err := s.accrueGlobal(tx, ev, models.GlobalAccrualReasonTPGeneration, gen, shareUSD); err != nil {
				return err
			}
			globalPoolUSD += shareUSD
		}

		// (3a) Buyer cashback (10%) → the transacting user (USD).
		if split.BuyerCashbackUSD > 0 {
			if err := s.creditCashback(tx, ev.UserID, ev.UserID, ev, split.BuyerCashbackUSD); err != nil {
				return err
			}
		}

		// (3b) Upline cashback (10%) → the direct (gen-1) ancestor if present
		// AND active; otherwise route to the global pool.
		if split.UplineCashbackUSD > 0 {
			gen1, ok := byGen[1]
			if ok && gen1.Active {
				if err := s.creditCashback(tx, gen1.UserID, ev.UserID, ev, split.UplineCashbackUSD); err != nil {
					return err
				}
			} else {
				if err := s.accrueGlobal(tx, ev, models.GlobalAccrualReasonUplineCashback, -1, split.UplineCashbackUSD); err != nil {
					return err
				}
				globalPoolUSD += split.UplineCashbackUSD
			}
		}

		// (4) Stamp the total global-pool routing on the guard row for audit.
		if globalPoolUSD > 0 {
			if err := tx.Model(&models.FeeDistribution{}).
				Where("id = ?", guard.ID).
				Update("global_pool_usd", globalPoolUSD).Error; err != nil {
				return fmt.Errorf("stamp global pool on guard: %w", err)
			}
		}

		return nil
	})

	if errors.Is(err, errAlreadyDistributed) {
		return nil
	}
	return err
}

// creditTP writes one TPEarning row and atomically increments the recipient's
// tp_balance. TP = shareUSD · tpPerUSD.
func (s *FeeDistributionService) creditTP(tx *gorm.DB, userID uuid.UUID, ev FeeEvent, gen int, pct, shareUSD float64) error {
	tpEarned := shareUSD * s.tpPerUSD
	earning := models.TPEarning{
		ID:           uuid.New(),
		UserID:       userID,
		SourceUserID: ev.UserID,
		SourceType:   ev.SourceType,
		SourceID:     ev.SourceID,
		Generation:   gen,
		FeeAmountUSD: ev.FeeUSD,
		Percentage:   pct,
		TPEarned:     tpEarned,
		CreatedAt:    time.Now(),
	}
	if err := tx.Create(&earning).Error; err != nil {
		return fmt.Errorf("create tp earning (user=%s gen=%d): %w", userID, gen, err)
	}
	if err := tx.Model(&models.User{}).Where("id = ?", userID).
		Update("tp_balance", gorm.Expr("tp_balance + ?", tpEarned)).Error; err != nil {
		return fmt.Errorf("increment tp_balance (user=%s): %w", userID, err)
	}
	return nil
}

// creditCashback writes one CashbackEarning row (USD) and atomically increments
// the recipient's cashback_balance. Payout currency is SP5's decision; SP3 only
// records the USD amount (TxHash left empty — no on-chain payment here).
func (s *FeeDistributionService) creditCashback(tx *gorm.DB, userID, sourceUserID uuid.UUID, ev FeeEvent, amountUSD float64) error {
	earning := models.CashbackEarning{
		ID:           uuid.New(),
		UserID:       userID,
		SourceUserID: sourceUserID,
		SourceType:   ev.SourceType,
		SourceID:     ev.SourceID,
		AmountUSD:    amountUSD,
		CreatedAt:    time.Now(),
	}
	if err := tx.Create(&earning).Error; err != nil {
		return fmt.Errorf("create cashback earning (user=%s): %w", userID, err)
	}
	if err := tx.Model(&models.User{}).Where("id = ?", userID).
		Update("cashback_balance", gorm.Expr("cashback_balance + ?", amountUSD)).Error; err != nil {
		return fmt.Errorf("increment cashback_balance (user=%s): %w", userID, err)
	}
	return nil
}

// accrueGlobal routes amountUSD into the global pool: it appends a
// GlobalTPAccrual ledger row AND increments the singleton GlobalTPPool running
// balance. ACCRUAL ONLY — SP4 owns settlement to users.
func (s *FeeDistributionService) accrueGlobal(tx *gorm.DB, ev FeeEvent, reason string, gen int, amountUSD float64) error {
	amountTP := amountUSD * s.tpPerUSD

	accrual := models.GlobalTPAccrual{
		ID:         uuid.New(),
		SourceType: ev.SourceType,
		SourceID:   ev.SourceID,
		Reason:     reason,
		Generation: gen,
		AmountUSD:  amountUSD,
		AmountTP:   amountTP,
		CreatedAt:  time.Now(),
	}
	if err := tx.Create(&accrual).Error; err != nil {
		return fmt.Errorf("create global accrual (%s gen=%d): %w", reason, gen, err)
	}

	// Upsert the singleton running balance. ON CONFLICT increments in place so
	// the first accrual creates the row and subsequent ones add to it, on both
	// PostgreSQL and SQLite.
	pool := models.GlobalTPPool{
		Singleton:  models.GlobalTPPoolSingletonID,
		AccruedUSD: amountUSD,
		AccruedTP:  amountTP,
		UpdatedAt:  time.Now(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "singleton"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"accrued_usd": gorm.Expr("global_tp_pool.accrued_usd + ?", amountUSD),
			"accrued_tp":  gorm.Expr("global_tp_pool.accrued_tp + ?", amountTP),
			"updated_at":  time.Now(),
		}),
	}).Create(&pool).Error; err != nil {
		return fmt.Errorf("upsert global pool balance: %w", err)
	}
	return nil
}

// GlobalPool returns the current singleton global-pool running balance. Returns
// a zero-value pool (not an error) when no accrual has happened yet.
func (s *FeeDistributionService) GlobalPool(ctx context.Context) (models.GlobalTPPool, error) {
	var pool models.GlobalTPPool
	err := s.db.WithContext(ctx).First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.GlobalTPPool{Singleton: models.GlobalTPPoolSingletonID}, nil
	}
	if err != nil {
		return models.GlobalTPPool{}, err
	}
	return pool, nil
}

// feeEnqueueMaxAttempts bounds how many times EnqueueFeeSettled re-drives a
// transiently-failing OnFeeSettled before giving up and alerting. OnFeeSettled
// is idempotent (the guard row), so retrying a partial/failed attempt is always
// safe: a successful retry no-ops on the guard, and a never-distributed event
// completes. This closes the "single transient DB error logs once and the fee
// is lost forever" hole the SP3 audit flagged.
const feeEnqueueMaxAttempts = 5

// feeEnqueueRetryBackoff is the base backoff between retry attempts (linear:
// attempt n waits n·base). Kept short — these are local DB writes, not network
// calls — so a transient lock/connection blip clears within a couple of seconds.
var feeEnqueueRetryBackoff = 500 * time.Millisecond

// EnqueueFeeSettled is the ASYNC wrapper: it runs OnFeeSettled off the request
// hot path so a fee event never blocks the HTTP response or the chain
// reconciler tick. OnFeeSettled is fully idempotent (the guard row), so a retry
// of a partial/failed attempt is always safe — a successful retry no-ops on the
// guard, a never-distributed event completes.
//
// Durability (SP3 audit fix): the caller commits the source record terminal
// (order completed, trade released) synchronously and there is NO reconciler for
// most fee-settled sources (checkout/swap/service) and the P2P reconciler
// early-returns on terminal trades — so a dropped goroutine is a PERMANENTLY
// lost fee, not a self-healing one. Until a durable outbox lands (the Phase-2
// River seam below), this wrapper retries a transient failure up to
// feeEnqueueMaxAttempts with linear backoff and, on final failure, emits a
// loud, greppable alert line (keyed fee_v2_settle_failed_total) carrying the
// full FeeEvent so the settlement can be re-driven by hand. This is best-effort,
// not a true outbox — but it removes the single-transient-error data-loss path.
//
// Phase 1 fires a detached goroutine with a bounded background context, mirror-
// ing the jobs package's "in-process, idempotent" stance (scheduler.go). When
// the jobs package migrates to a durable queue (River, per scheduler.go's
// Phase-2 note) this is the single seam to swap for an outbox enqueue — the
// synchronous OnFeeSettled core does not change.
func (s *FeeDistributionService) EnqueueFeeSettled(ev FeeEvent) {
	if s == nil {
		return
	}
	go func() {
		var lastErr error
		for attempt := 1; attempt <= feeEnqueueMaxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := s.OnFeeSettled(ctx, ev)
			cancel()
			if err == nil {
				return
			}
			lastErr = err
			log.Printf("[fee-v2] OnFeeSettled attempt %d/%d failed (type=%s id=%s): %v",
				attempt, feeEnqueueMaxAttempts, ev.SourceType, ev.SourceID, err)
			if attempt < feeEnqueueMaxAttempts {
				time.Sleep(time.Duration(attempt) * feeEnqueueRetryBackoff)
			}
		}
		// Final failure: the fee is NOT distributed and nothing will re-drive it.
		// Emit a loud, greppable alert with the full event so an operator can
		// replay it. The keyed token mirrors the p2p_reconciler_drift_total
		// log-metric convention.
		log.Printf("[fee-v2] ALERT fee_v2_settle_failed_total=1 PERMANENT settlement failure after %d attempts — "+
			"fee NOT distributed, manual replay required: feature=%s token=%s feeUSD=%.6f user=%s type=%s id=%s: %v",
			feeEnqueueMaxAttempts, ev.Feature, ev.Token, ev.FeeUSD, ev.UserID, ev.SourceType, ev.SourceID, lastErr)
	}()
}
