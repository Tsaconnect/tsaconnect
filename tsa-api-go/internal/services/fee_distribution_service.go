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
// overhaul). It redefines how a settled platform fee is accounted off-chain
// using the COMPLETE per-feature split model (design §2). Each feature names a
// row in featureSplits whose five fractional shares (System, Buyer, Upline,
// Provider, TP) sum to exactly 1.0:
//
//	system   = split.System   · FeeUSD  (treasury retains; recorded only)
//	buyer    = split.Buyer     · FeeUSD  (buyer cashback, USD)
//	upline   = split.Upline    · FeeUSD  (direct gen-1 cashback, USD; → global pool if none)
//	provider = split.Provider  · FeeUSD  (provider cashback, USD; to FeeEvent.ProviderID)
//	tpPool   = split.TP        · FeeUSD  (distributed gen0..gen10 by GenerationPercentages)
//
// Carved features (product_merchant, p2p_platform, service_contact,
// instant_transfer) credit buyer/upline (and service_contact also provider)
// cashback plus TP; the 70/30 features (otc, spot, allswap, instant_pay,
// withdrawal) credit NO cashback (Buyer=Upline=Provider=0) and only TP; gas is
// 100% system with NO TP and NO recipients. A zero share credits nothing.
//
// The TP pool is split across gen0 (the transacting user) and gen1..gen10
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

	// onDirty, when set, is invoked once per DISTINCT recipient whose tp_balance
	// or cashback_balance changed in a settled fee event (gen0..gen10 TP
	// recipients + buyer/upline cashback recipients). It is the SP3→SP5 bridge:
	// main.go wires it to RewardService.MarkUserDirty so the SP5 reward processor
	// picks the user up on its next tick. Called AFTER the distribution tx commits
	// (so a rolled-back distribution never marks anyone dirty) and BEST-EFFORT (a
	// failed mark only delays a reward to the user's next genuine activity — the
	// reward computation is itself idempotent and self-healing, so a dropped mark
	// is never a lost reward). nil by default — the engine stays usable (and unit-
	// testable) without the reward service wired.
	onDirty func(ctx context.Context, userID uuid.UUID)
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

// SetDirtyHook wires the SP3→SP5 bridge: a callback invoked once per distinct
// recipient touched by a settled fee event (see the onDirty field doc). main.go
// passes RewardService.MarkUserDirty here so accrual flags users for the reward
// processor. nil-safe both ways — passing nil clears the hook, and the engine
// works fine without it.
func (s *FeeDistributionService) SetDirtyHook(fn func(ctx context.Context, userID uuid.UUID)) {
	if s == nil {
		return
	}
	s.onDirty = fn
}

// MaxTPGenerations is the number of upline generations the TP pool reaches
// beyond gen0 (the transacting user). gen0 + gen1..gen10.
const MaxTPGenerations = 10

// FeeShares is the per-feature split of a settled fee into its five components,
// each a FRACTION of the fee F (they MUST sum to exactly 1.0 — see the boot
// guard in init below). This is the COMPLETE per-feature split model (design §2,
// final PM clarification):
//
//	System   — treasury retains (recorded only).
//	Buyer    — buyer cashback (USD), credited to the transacting user.
//	Upline   — direct gen-1 cashback (USD), → global pool if no live gen-1.
//	Provider — provider cashback (USD), credited to FeeEvent.ProviderID in MCGP.
//	TP       — TP-pool share, distributed gen0..gen10 via GenerationPercentages.
//
// A 0 component credits nothing (e.g. the 70/30 trades have Buyer=Upline=0 so
// they generate no cashback; gas is System=1.0 so it credits only system — no TP,
// no recipients).
type FeeShares struct {
	System   float64
	Buyer    float64
	Upline   float64
	Provider float64
	TP       float64
}

// Sum is the total of the five fractional shares — the boot guard requires it to
// equal exactly 1.0 per feature (mirrors models.GenerationPercentages == 1.0).
func (fs FeeShares) Sum() float64 {
	return fs.System + fs.Buyer + fs.Upline + fs.Provider + fs.TP
}

// featureSplits is the COMPLETE per-feature split table (design §2). Each row's
// shares MUST sum to exactly 1.0; the init() below boot-validates every row and
// panics on drift (same contract as the GenerationPercentages ladder). The TP
// component is distributed across gen0..gen10 via the UNCHANGED
// models.GenerationPercentages ladder — only the fraction of the fee that BECOMES
// the TP pool changes per feature.
//
//	product_merchant (2%):   carved              {0.50, 0.15,   0.15,   0.00, 0.20}
//	p2p_platform     (1%):   carved              {0.50, 0.15,   0.15,   0.00, 0.20}
//	service_contact  ($0.10):carved + provider   {0.50, 0.10,   0.10,   0.10, 0.20}
//	instant_transfer (2%):   carved              {0.75, 0.0625, 0.0625, 0.00, 0.125}
//	otc              (2%):   70/30 no-cashback    {0.70, 0.00,   0.00,   0.00, 0.30}
//	spot             (3%):                        {0.70, 0.00,   0.00,   0.00, 0.30}
//	allswap          (3%):                        {0.70, 0.00,   0.00,   0.00, 0.30}
//	instant_pay      (0.5%):                      {0.70, 0.00,   0.00,   0.00, 0.30}
//	withdrawal       (0.5%):                      {0.70, 0.00,   0.00,   0.00, 0.30}
//	gas              (0.5%): 100% system, no TP   {1.00, 0.00,   0.00,   0.00, 0.00}
var featureSplits = map[string]FeeShares{
	models.FeatureProductMerchant: {System: 0.50, Buyer: 0.15, Upline: 0.15, Provider: 0.00, TP: 0.20},
	models.FeatureP2PPlatform:     {System: 0.50, Buyer: 0.15, Upline: 0.15, Provider: 0.00, TP: 0.20},
	models.FeatureServiceContact:  {System: 0.50, Buyer: 0.10, Upline: 0.10, Provider: 0.10, TP: 0.20},
	models.FeatureInstantTransfer: {System: 0.75, Buyer: 0.0625, Upline: 0.0625, Provider: 0.00, TP: 0.125},
	models.FeatureOTC:             {System: 0.70, Buyer: 0.00, Upline: 0.00, Provider: 0.00, TP: 0.30},
	models.FeatureSpot:            {System: 0.70, Buyer: 0.00, Upline: 0.00, Provider: 0.00, TP: 0.30},
	models.FeatureAllswap:         {System: 0.70, Buyer: 0.00, Upline: 0.00, Provider: 0.00, TP: 0.30},
	models.FeatureInstantPay:      {System: 0.70, Buyer: 0.00, Upline: 0.00, Provider: 0.00, TP: 0.30},
	models.FeatureWithdrawal:      {System: 0.70, Buyer: 0.00, Upline: 0.00, Provider: 0.00, TP: 0.30},
	models.FeatureGas:             {System: 1.00, Buyer: 0.00, Upline: 0.00, Provider: 0.00, TP: 0.00},
}

// defaultSplitFeature is the fallback feature whose split is used when an
// unknown feature key is settled (the call is logged once). product_merchant is
// the canonical carved split — a sane, conservative default that still credits
// cashback + TP rather than silently dropping the fee.
const defaultSplitFeature = models.FeatureProductMerchant

// init boot-validates the split table: EVERY feature's shares must sum to
// exactly 1.0 (within float tolerance), mirroring the GenerationPercentages
// conservation guard. A drift here would over- or under-credit the fee, so fail
// loud at boot rather than leak value at runtime.
func init() {
	for feature, fs := range featureSplits {
		if d := fs.Sum() - 1.0; d > 1e-9 || d < -1e-9 {
			panic(fmt.Sprintf(
				"services.featureSplits[%q] must sum to 1.0 (System+Buyer+Upline+Provider+TP), got %.6f (drift %.6f)",
				feature, fs.Sum(), d))
		}
	}
}

// splitFor returns the per-feature share table. An unknown feature falls back to
// the product_merchant split and logs once-per-call so a mis-keyed hook is
// visible but never drops the fee.
func splitFor(feature string) FeeShares {
	if fs, ok := featureSplits[feature]; ok {
		return fs
	}
	log.Printf("[fee-v2] unknown fee feature %q — defaulting to %q split", feature, defaultSplitFeature)
	return featureSplits[defaultSplitFeature]
}

// FeeEvent is the input to the engine: one settled platform fee. Stage 2 (the
// hook-up across checkout / service / swap / withdrawal / P2P release) builds
// these and calls OnFeeSettled.
//
//	Feature    — fee-feature key (models.FeatureProductMerchant, FeatureOTC, …).
//	             Selects the per-feature split via splitFor.
//	Token      — settlement token symbol (for audit; rates already applied).
//	FeeUSD     — total fee F in USD; the per-feature split is taken from this.
//	UserID     — the transacting user (gen0; buyer cashback recipient).
//	ProviderID — optional provider recipient for the provider-cashback component
//	             (only service_contact carries a non-zero provider share today).
//	             nil means no provider; a non-nil id with a zero provider share is
//	             ignored.
//	SourceType — the originating domain ("checkout", "service_contact", "swap", …).
//	SourceID   — the originating record id; (SourceType, SourceID) is the
//	             idempotency key — distributing twice for the same pair is a
//	             no-op the second time.
type FeeEvent struct {
	Feature    string
	Token      string
	FeeUSD     float64
	UserID     uuid.UUID
	ProviderID *uuid.UUID
	SourceType string
	SourceID   uuid.UUID
}

// FeeSplit is the computed per-feature breakdown of a FeeEvent, all in USD.
type FeeSplit struct {
	SystemUSD           float64
	BuyerCashbackUSD    float64
	UplineCashbackUSD   float64
	ProviderCashbackUSD float64
	TPPoolUSD           float64
}

// Total returns the sum of the five components — must equal the source FeeUSD
// (every feature's shares sum to 1.0, boot-validated in init).
func (sp FeeSplit) Total() float64 {
	return sp.SystemUSD + sp.BuyerCashbackUSD + sp.UplineCashbackUSD + sp.ProviderCashbackUSD + sp.TPPoolUSD
}

// ComputeSplit returns the USD breakdown of feeUSD for the given fee feature,
// using the COMPLETE per-feature split table (splitFor). An unknown feature
// falls back to the product_merchant split (and logs).
func ComputeSplit(feature string, feeUSD float64) FeeSplit {
	fs := splitFor(feature)
	return FeeSplit{
		SystemUSD:           feeUSD * fs.System,
		BuyerCashbackUSD:    feeUSD * fs.Buyer,
		UplineCashbackUSD:   feeUSD * fs.Upline,
		ProviderCashbackUSD: feeUSD * fs.Provider,
		TPPoolUSD:           feeUSD * fs.TP,
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

	// Distinct recipients whose tp_balance/cashback_balance actually changed this
	// run — fed to the onDirty (SP3→SP5) hook AFTER the tx commits so the reward
	// processor picks them up. Only populated on a fresh (non-duplicate)
	// distribution; a duplicate no-op marks no one. Global-pool routing touches no
	// individual user's balance, so global-only generations are not added here.
	recipients := make(map[uuid.UUID]struct{}, MaxTPGenerations+2)

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		split := ComputeSplit(ev.Feature, ev.FeeUSD)

		// (1) Idempotency guard. ON CONFLICT DO NOTHING then check RowsAffected
		// so a duplicate is a clean no-op on both PostgreSQL and SQLite.
		guard := models.FeeDistribution{
			ID:                  uuid.New(),
			SourceType:          ev.SourceType,
			SourceID:            ev.SourceID,
			UserID:              ev.UserID,
			Feature:             ev.Feature,
			Token:               ev.Token,
			FeeUSD:              ev.FeeUSD,
			SystemUSD:           split.SystemUSD,
			BuyerCashbackUSD:    split.BuyerCashbackUSD,
			UplineCashbackUSD:   split.UplineCashbackUSD,
			ProviderCashbackUSD: split.ProviderCashbackUSD,
			TPPoolUSD:           split.TPPoolUSD,
			TPPerUSD:            s.tpPerUSD,
			CreatedAt:           time.Now(),
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
				recipients[ev.UserID] = struct{}{}
				continue
			}

			anc, ok := byGen[gen]
			if ok && anc.Active {
				if err := s.creditTP(tx, anc.UserID, ev, gen, pct, shareUSD); err != nil {
					return err
				}
				recipients[anc.UserID] = struct{}{}
				continue
			}

			// No live ancestor at this generation: route the share to the
			// global pool and CONTINUE (do not stop the climb).
			if err := s.accrueGlobal(tx, ev, models.GlobalAccrualReasonTPGeneration, gen, shareUSD); err != nil {
				return err
			}
			globalPoolUSD += shareUSD
		}

		// (3a) Buyer cashback (split.Buyer · F) → the transacting user (USD).
		// Zero for the 70/30 trades and gas — those credit no buyer cashback.
		if split.BuyerCashbackUSD > 0 {
			if err := s.creditCashback(tx, ev.UserID, ev.UserID, ev, split.BuyerCashbackUSD); err != nil {
				return err
			}
			recipients[ev.UserID] = struct{}{}
		}

		// (3b) Upline cashback (split.Upline · F) → the direct (gen-1) ancestor if
		// present AND active; otherwise route to the global pool. Zero for the
		// 70/30 trades and gas.
		if split.UplineCashbackUSD > 0 {
			gen1, ok := byGen[1]
			if ok && gen1.Active {
				if err := s.creditCashback(tx, gen1.UserID, ev.UserID, ev, split.UplineCashbackUSD); err != nil {
					return err
				}
				recipients[gen1.UserID] = struct{}{}
			} else {
				if err := s.accrueGlobal(tx, ev, models.GlobalAccrualReasonUplineCashback, -1, split.UplineCashbackUSD); err != nil {
					return err
				}
				globalPoolUSD += split.UplineCashbackUSD
			}
		}

		// (3c) Provider cashback (split.Provider · F) → the service provider
		// (FeeEvent.ProviderID). Only service_contact carries a non-zero provider
		// share today; the cashback is recorded in USD here (the SP5 reward rail
		// converts it to MCGP, like all other cashback). When the split's provider
		// component is 0, or no ProviderID is supplied, credit nothing — there is
		// no global-pool fallback for the provider share (unlike upline).
		if split.ProviderCashbackUSD > 0 && ev.ProviderID != nil && *ev.ProviderID != uuid.Nil {
			if err := s.creditCashback(tx, *ev.ProviderID, ev.UserID, ev, split.ProviderCashbackUSD); err != nil {
				return err
			}
			recipients[*ev.ProviderID] = struct{}{}
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
	if err != nil {
		return err
	}

	// Distribution committed: bridge to SP5. Mark every touched recipient dirty so
	// the reward processor recomputes their level rewards / cashback conversion.
	// Best-effort and post-commit: a failed/dropped mark only defers a reward to
	// the user's next genuine activity (the reward computation is idempotent and
	// self-healing), so we log and continue rather than failing the settled fee.
	s.markRecipientsDirty(ctx, recipients)
	return nil
}

// markRecipientsDirty invokes the onDirty hook (when wired) once per recipient.
// Best-effort: the hook is the SP3→SP5 bridge (RewardService.MarkUserDirty); a
// panic or slow mark must never corrupt the already-committed distribution, so
// each call is isolated. No-op when no hook is wired or no recipients changed.
func (s *FeeDistributionService) markRecipientsDirty(ctx context.Context, recipients map[uuid.UUID]struct{}) {
	if s.onDirty == nil || len(recipients) == 0 {
		return
	}
	for uid := range recipients {
		s.onDirty(ctx, uid)
	}
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
