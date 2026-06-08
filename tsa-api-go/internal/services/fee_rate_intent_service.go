package services

import (
	"context"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// FeeRateIntentService is the off-chain authoring + plan/apply engine for
// DESIRED-state fee rate intents (configurable-FeeManager design §4/§5).
//
// An intent is per-(feature, token) but carries a global default rate plus
// optional per-chain overrides. The service:
//   - persists the intent + its overrides (Create),
//   - resolves the per-chain EFFECTIVE rate (Resolve = override ?? global),
//   - diffs the resolved rate against each chain's on-chain rateFor and builds
//     the UNSIGNED FeeManager.setRate tx(s) (Plan),
//   - drives the draft→pending→partially_applied→applied (+cancelled) state
//     machine (UpdateState / the confirm path in fee_admin_handler).
//
// An intent NEVER charges; it is desired-state only. The thing that is actually
// charged is the fee_config READ REPLICA (FeeConfigService), written exclusively
// from CONFIRMED on-chain RateSet values via RefreshFromConfirmedRate.
type FeeRateIntentService struct {
	db  *gorm.DB
	cfg *config.Config
	// chainReader is the on-chain rateFor reader used by Plan to diff intent vs
	// chain. nil = chain reads unavailable (Plan returns "not configured").
	chainReader FeeManagerChainReader
}

// NewFeeRateIntentService builds a FeeRateIntentService. cfg + chainReader may be
// nil in tests that exercise only the persistence primitives; Plan requires both.
func NewFeeRateIntentService(db *gorm.DB, cfg *config.Config, chainReader FeeManagerChainReader) *FeeRateIntentService {
	return &FeeRateIntentService{db: db, cfg: cfg, chainReader: chainReader}
}

// Create persists a GLOBAL-default intent in the pending state plus a set of
// per-chain override intents (chainID → rate). The whole set shares the same
// (feature, token) and is the unit Plan/confirm fan out across chains.
//
// The global row uses chainID = NULL; each override row carries its chain. All
// rows are (re)authored to `pending` (the design's draft→pending at POST). The
// (feature, token, chain) unique index means re-authoring the same key UPSERTS
// the rate and RESETS its state to pending (a re-author of an unapplied rate is
// the normal edit flow) rather than failing on conflict.
func (s *FeeRateIntentService) Create(feature, token string, def FeeManagerRate, overrides map[int64]FeeManagerRate) (*models.FeeRateIntent, error) {
	if s.db == nil {
		return nil, fmt.Errorf("fee rate intent: no db")
	}
	var global *models.FeeRateIntent

	err := s.db.Transaction(func(tx *gorm.DB) error {
		g, err := upsertIntentRow(tx, feature, token, nil, def)
		if err != nil {
			return err
		}
		global = g
		// Sort the override chainIDs so creation is deterministic (stable tests).
		chains := make([]int64, 0, len(overrides))
		for chainID := range overrides {
			chains = append(chains, chainID)
		}
		sort.Slice(chains, func(i, j int) bool { return chains[i] < chains[j] })
		for _, chainID := range chains {
			cid := chainID
			if _, err := upsertIntentRow(tx, feature, token, &cid, overrides[chainID]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return global, nil
}

// upsertIntentRow inserts or updates a single (feature, token, chain) intent row
// to the given rate, (re)setting its state to pending. Returns the row.
func upsertIntentRow(tx *gorm.DB, feature, token string, chainID *int64, r FeeManagerRate) (*models.FeeRateIntent, error) {
	q := tx.Where("feature = ? AND token = ?", feature, token)
	if chainID == nil {
		q = q.Where("chain_id IS NULL")
	} else {
		q = q.Where("chain_id = ?", *chainID)
	}
	var existing models.FeeRateIntent
	err := q.First(&existing).Error
	switch {
	case err == nil:
		updates := map[string]interface{}{
			"bps":         int(r.BPS),
			"flat_usd":    flatUnitsToUSD(r.FlatUSD),
			"mcgp_exempt": r.MCGPExempt,
			"active":      r.Active,
			"state":       models.FeeRateIntentPending,
		}
		if uerr := tx.Model(&models.FeeRateIntent{}).Where("id = ?", existing.ID).Updates(updates).Error; uerr != nil {
			return nil, uerr
		}
		if rerr := tx.First(&existing, "id = ?", existing.ID).Error; rerr != nil {
			return nil, rerr
		}
		return &existing, nil
	case err == gorm.ErrRecordNotFound:
		row := &models.FeeRateIntent{
			ID:         uuid.New(),
			Feature:    feature,
			Token:      token,
			ChainID:    chainID,
			BPS:        r.BPS,
			FlatUSD:    flatUnitsToUSD(r.FlatUSD),
			MCGPExempt: r.MCGPExempt,
			Active:     r.Active,
			State:      models.FeeRateIntentPending,
		}
		if cerr := tx.Create(row).Error; cerr != nil {
			return nil, cerr
		}
		return row, nil
	default:
		return nil, err
	}
}

// Get loads the GLOBAL (chain=NULL) intent row by ID.
func (s *FeeRateIntentService) Get(id uuid.UUID) (*models.FeeRateIntent, error) {
	var intent models.FeeRateIntent
	if err := s.db.First(&intent, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &intent, nil
}

// List returns all intents ordered by most-recent-first.
func (s *FeeRateIntentService) List() ([]models.FeeRateIntent, error) {
	var intents []models.FeeRateIntent
	if err := s.db.Order("created_at DESC").Find(&intents).Error; err != nil {
		return nil, err
	}
	return intents, nil
}

// UpdateState advances an intent ROW's lifecycle state.
func (s *FeeRateIntentService) UpdateState(id uuid.UUID, state models.FeeRateIntentState) error {
	return s.db.Model(&models.FeeRateIntent{}).
		Where("id = ?", id).
		Update("state", state).Error
}

// Resolve returns the EFFECTIVE rate for (feature, token, chainID) over the
// intent's rows: a chain-specific override row ?? the global (chain=NULL) row
// (design §4 resolution, the chain dimension on the off-chain authoring side).
// found=false when neither a chain override nor a global row exists.
func (s *FeeRateIntentService) Resolve(feature, token string, chainID int64) (FeeManagerRate, bool, error) {
	var override models.FeeRateIntent
	err := s.db.Where("feature = ? AND token = ? AND chain_id = ?", feature, token, chainID).
		First(&override).Error
	if err == nil {
		return rateFromIntent(override), true, nil
	}
	if err != gorm.ErrRecordNotFound {
		return FeeManagerRate{}, false, err
	}
	var global models.FeeRateIntent
	err = s.db.Where("feature = ? AND token = ? AND chain_id IS NULL", feature, token).
		First(&global).Error
	if err == nil {
		return rateFromIntent(global), true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return FeeManagerRate{}, false, nil
	}
	return FeeManagerRate{}, false, err
}

// rateFromIntent converts a stored intent row to the on-chain rate shape,
// clamping bps to [0, MaxFeeBPS] and converting the flat USD to 1e6 units.
func rateFromIntent(r models.FeeRateIntent) FeeManagerRate {
	bps := int(r.BPS)
	bps = clampBPS(bps)
	return FeeManagerRate{
		BPS:        uint16(bps),
		FlatUSD:    flatUSDToUnits(r.FlatUSD),
		MCGPExempt: r.MCGPExempt,
		Active:     r.Active,
	}
}

// PlanDiff is one (feature, token, chain) entry in a Plan: the on-chain `before`
// rate, the resolved intent `after` rate, and whether they already match (noop).
type PlanDiff struct {
	Feature string         `json:"feature"`
	Token   string         `json:"token"`
	ChainID int64          `json:"chainId"`
	Before  FeeManagerRate `json:"before"`
	After   FeeManagerRate `json:"after"`
	Noop    bool           `json:"noop"`
}

// PlanTx is one unsigned FeeManager.setRate tx in a Plan (design §5 schema). The
// `data` is the load-bearing pre-encoded calldata; `decoded` is advisory for the
// signer UI.
type PlanTx struct {
	ChainID  int64        `json:"chainId"`
	To       string       `json:"to"`
	Value    string       `json:"value"`
	Data     string       `json:"data"`
	Function string       `json:"function"`
	Decoded  PlanTxDecode `json:"decoded"`
}

// PlanTxDecode is the advisory decoded view of a PlanTx.
type PlanTxDecode struct {
	Feature string         `json:"feature"`
	Token   string         `json:"token"`
	Rate    FeeManagerRate `json:"rate"`
}

// PlanResult is the /plan response: the per-chain diff + the unsigned txs (one
// setRate per affected chain). Idempotent: Txs is empty when every chain already
// matches the intent.
type PlanResult struct {
	IntentID string     `json:"intentId"`
	Diff     []PlanDiff `json:"diff"`
	Txs      []PlanTx   `json:"txs"`
}

// ErrFeeManagerNotConfigured is returned by Plan when no FeeManager chain is
// configured (the whole flow is dormant) or no chain reader is wired.
var ErrFeeManagerNotConfigured = fmt.Errorf("fee manager not configured")

// Plan resolves the per-chain effective rate for the intent, reads each
// configured chain's on-chain rateFor, diffs them, and returns the diff plus the
// UNSIGNED setRate tx(s) for the chains that differ (design §5). Empty Txs when
// already matching (idempotent). The backend never signs or broadcasts.
//
// "Affected chains" = the chains in cfg.FeeManagerChains. For each, the effective
// rate is the intent's per-chain override ?? its global default; a chain with no
// resolvable intent rate is skipped (not in this intent's scope).
func (s *FeeRateIntentService) Plan(ctx context.Context, intentID uuid.UUID) (*PlanResult, error) {
	global, err := s.Get(intentID)
	if err != nil {
		return nil, err
	}
	if s.cfg == nil || len(s.cfg.FeeManagerChains) == 0 || s.chainReader == nil {
		return nil, ErrFeeManagerNotConfigured
	}

	res := &PlanResult{IntentID: intentID.String()}

	// Deterministic chain order for stable plans/tests.
	chainIDs := make([]int64, 0, len(s.cfg.FeeManagerChains))
	for cid := range s.cfg.FeeManagerChains {
		chainIDs = append(chainIDs, cid)
	}
	sort.Slice(chainIDs, func(i, j int) bool { return chainIDs[i] < chainIDs[j] })

	for _, chainID := range chainIDs {
		fmChain := s.cfg.FeeManagerChains[chainID]
		after, found, rerr := s.Resolve(global.Feature, global.Token, chainID)
		if rerr != nil {
			return nil, rerr
		}
		if !found {
			continue // intent doesn't scope this chain
		}

		before, cerr := s.chainReader.RateFor(ctx, chainID, fmChain.Address, global.Feature, global.Token)
		if cerr != nil {
			return nil, fmt.Errorf("read on-chain rateFor chain=%d: %w", chainID, cerr)
		}

		noop := ratesEqual(before, after)
		res.Diff = append(res.Diff, PlanDiff{
			Feature: global.Feature,
			Token:   global.Token,
			ChainID: chainID,
			Before:  before,
			After:   after,
			Noop:    noop,
		})
		if noop {
			continue // already matches — no tx (idempotent)
		}

		data, perr := PackSetRate(global.Feature, global.Token, after)
		if perr != nil {
			return nil, perr
		}
		res.Txs = append(res.Txs, PlanTx{
			ChainID:  chainID,
			To:       fmChain.Address,
			Value:    "0",
			Data:     "0x" + common.Bytes2Hex(data),
			Function: SetRateFunctionSig,
			Decoded: PlanTxDecode{
				Feature: global.Feature,
				Token:   global.Token,
				Rate:    after,
			},
		})
	}
	return res, nil
}

// ratesEqual reports whether two resolved rates are byte-for-byte identical
// (so Plan can short-circuit to a no-op tx-less diff entry).
func ratesEqual(a, b FeeManagerRate) bool {
	return a.BPS == b.BPS && a.FlatUSD == b.FlatUSD && a.MCGPExempt == b.MCGPExempt && a.Active == b.Active
}

// ConfirmChain advances the intent state machine after a single chain's
// FeeManager.setRate has been VERIFIED on-chain (design §5). The fee_admin
// handler has already (a) verified the receipt status==1 AND (b) read back
// rateFor == the resolved intent, and (c) written the fee_config replica via
// RefreshFromConfirmedRate — ConfirmChain only moves the state machine.
//
// State transitions (per the design §5 machine):
//
//	pending           --confirm(chainId) ok--> partially_applied (≥1 of N chains done)
//	partially_applied --all in-scope chains confirmed--> applied
//
// "in-scope chains" = the configured FeeManagerChains for which Resolve finds a
// rate. A chain's confirmation is recorded by setting that chain's override row
// to `applied` (or, when the chain rides the global default with no override row,
// by counting it as covered once its rateFor matches — tracked via confirmedChainIDs).
// The GLOBAL row's State is the aggregate the admin reads (pending →
// partially_applied → applied).
//
// Returns (applied=true) iff this confirmation brought ALL in-scope chains to
// applied (the intent is fully done).
func (s *FeeRateIntentService) ConfirmChain(intentID uuid.UUID, chainID int64) (applied bool, err error) {
	global, err := s.Get(intentID)
	if err != nil {
		return false, err
	}
	if global.State == models.FeeRateIntentApplied {
		return true, nil // already fully applied — idempotent
	}
	if global.State == models.FeeRateIntentCancelled {
		return false, fmt.Errorf("intent %s is cancelled", intentID)
	}

	// Resolve the effective rate for this chain so a default-riding chain (no
	// override row) gets a persisted per-chain marker carrying the same value —
	// resolution is unchanged (override == global) but coverage becomes durable.
	resolved, found, rerr := s.Resolve(global.Feature, global.Token, chainID)
	if rerr != nil {
		return false, rerr
	}
	if !found {
		return false, fmt.Errorf("chain %d is not in scope for intent %s", chainID, intentID)
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Upsert this chain's override row in the `applied` state. If it already
		// exists (an explicit override), just flip it applied; otherwise create a
		// marker row equal to the resolved (global) rate so multi-chain default
		// rollouts have durable per-chain coverage.
		var existing models.FeeRateIntent
		qerr := tx.Where("feature = ? AND token = ? AND chain_id = ?", global.Feature, global.Token, chainID).
			First(&existing).Error
		switch {
		case qerr == nil:
			return tx.Model(&models.FeeRateIntent{}).
				Where("id = ?", existing.ID).
				Update("state", models.FeeRateIntentApplied).Error
		case qerr == gorm.ErrRecordNotFound:
			cid := chainID
			return tx.Create(&models.FeeRateIntent{
				ID:         uuid.New(),
				Feature:    global.Feature,
				Token:      global.Token,
				ChainID:    &cid,
				BPS:        resolved.BPS,
				FlatUSD:    flatUnitsToUSD(resolved.FlatUSD),
				MCGPExempt: resolved.MCGPExempt,
				Active:     resolved.Active,
				State:      models.FeeRateIntentApplied,
			}).Error
		default:
			return qerr
		}
	})
	if err != nil {
		return false, err
	}

	// The global row advances to partially_applied on the first confirm and
	// applied only when EVERY in-scope chain has an applied per-chain marker.
	allDone, derr := s.InScopeChainsConfirmed(global, chainID)
	if derr != nil {
		return false, derr
	}
	next := models.FeeRateIntentPartiallyApplied
	if allDone {
		next = models.FeeRateIntentApplied
	}
	if err := s.UpdateState(global.ID, next); err != nil {
		return false, err
	}
	return allDone, nil
}

// InScopeChainsConfirmed reports whether EVERY in-scope chain (a configured
// FeeManagerChain for which the intent resolves a rate) has been confirmed —
// counting `justConfirmed` as confirmed even if its DB row write isn't visible
// yet in this transaction. A chain is "confirmed" when:
//   - it has an override row in state `applied`, OR
//   - it is `justConfirmed`, OR
//   - it rides the global default AND the global row is already `applied`.
//
// Because chains riding the global default have no per-chain row, the only
// robust signal that a default-chain is done is its own confirm call. We
// therefore require an explicit confirm for EACH in-scope chain; coverage is the
// AND over all in-scope chains of "has an applied override row OR == justConfirmed".
func (s *FeeRateIntentService) InScopeChainsConfirmed(global *models.FeeRateIntent, justConfirmed int64) (bool, error) {
	if s.cfg == nil || len(s.cfg.FeeManagerChains) == 0 {
		return false, nil
	}
	// Load all sibling rows for this (feature, token).
	var rows []models.FeeRateIntent
	if err := s.db.Where("feature = ? AND token = ?", global.Feature, global.Token).
		Find(&rows).Error; err != nil {
		return false, err
	}
	// Build a quick lookup of override-row state by chainID and a set of
	// chain confirms recorded (applied override rows + justConfirmed).
	overrideApplied := map[int64]bool{}
	for _, r := range rows {
		if r.ChainID != nil {
			overrideApplied[*r.ChainID] = r.State == models.FeeRateIntentApplied
		}
	}
	confirmed := map[int64]bool{justConfirmed: true}

	for chainID := range s.cfg.FeeManagerChains {
		// In scope? Resolve a rate for this chain.
		_, found, err := s.Resolve(global.Feature, global.Token, chainID)
		if err != nil {
			return false, err
		}
		if !found {
			continue // not in scope
		}
		// Confirmed if its override row is applied OR it is the chain we just
		// confirmed. (A default-riding chain has no override row, so it must be
		// confirmed explicitly — overrideApplied lookup is false and we rely on
		// justConfirmed; a multi-chain default rollout records each chain's
		// applied override or is confirmed one call at a time.)
		if overrideApplied[chainID] || confirmed[chainID] {
			continue
		}
		return false, nil
	}
	return true, nil
}

// Cancel marks the intent (and its sibling override rows) cancelled, allowed
// only while NOT fully applied (design §5). Returns an error if already applied.
func (s *FeeRateIntentService) Cancel(intentID uuid.UUID) error {
	global, err := s.Get(intentID)
	if err != nil {
		return err
	}
	if global.State == models.FeeRateIntentApplied {
		return fmt.Errorf("intent %s is already fully applied; cannot cancel", intentID)
	}
	return s.db.Model(&models.FeeRateIntent{}).
		Where("feature = ? AND token = ?", global.Feature, global.Token).
		Update("state", models.FeeRateIntentCancelled).Error
}
