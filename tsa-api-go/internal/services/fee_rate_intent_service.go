package services

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// IntentView is one logical intent (per feature/token) for the admin dashboard:
// the flat rows grouped so the consumer never regroups them. IntentID + State +
// Default come from the GLOBAL (chain=NULL) row — the same id Plan/Confirm use —
// and non-NULL rows become per-chain Overrides. Rates use the 1e6-flat-unit
// FeeManagerRate shape (identical to Plan/Confirm), keyed by chainId string.
type IntentView struct {
	IntentID  string                    `json:"intentId"`
	Feature   string                    `json:"feature"`
	Token     string                    `json:"token"`
	State     string                    `json:"state"`
	Default   *FeeManagerRate           `json:"default"`
	Overrides map[string]FeeManagerRate `json:"overrides"`
	CreatedAt time.Time                 `json:"createdAt"`
	UpdatedAt time.Time                 `json:"updatedAt"`
}

// ListViews groups the flat intent rows into per-(feature, token) logical intents
// for the dashboard's pending-changes view. The global (chain=NULL) row supplies
// IntentID/State/Default; non-NULL rows become Overrides. When `states` is
// non-empty, only intents whose summary state is in the set are returned. Order
// follows List() (most-recently-touched first).
func (s *FeeRateIntentService) ListViews(states ...models.FeeRateIntentState) ([]IntentView, error) {
	rows, err := s.List()
	if err != nil {
		return nil, err
	}
	type key struct{ feature, token string }
	order := make([]key, 0)
	groups := map[key][]models.FeeRateIntent{}
	for _, r := range rows {
		k := key{r.Feature, r.Token}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	want := map[models.FeeRateIntentState]bool{}
	for _, st := range states {
		want[st] = true
	}
	views := make([]IntentView, 0, len(order))
	for _, k := range order {
		v := IntentView{Feature: k.feature, Token: k.token, Overrides: map[string]FeeManagerRate{}}
		globalFound := false
		for _, r := range groups[k] {
			if r.ChainID == nil {
				rate := rateFromIntent(r)
				v.IntentID, v.State, v.Default = r.ID.String(), string(r.State), &rate
				v.CreatedAt, v.UpdatedAt = r.CreatedAt, r.UpdatedAt
				globalFound = true
			} else {
				v.Overrides[strconv.FormatInt(*r.ChainID, 10)] = rateFromIntent(r)
			}
		}
		if !globalFound {
			// Only per-chain overrides exist (no global row) — still surface the
			// intent using its first row so it stays visible/actionable.
			r := groups[k][0]
			v.IntentID, v.State = r.ID.String(), string(r.State)
			v.CreatedAt, v.UpdatedAt = r.CreatedAt, r.UpdatedAt
		}
		if len(want) > 0 && !want[models.FeeRateIntentState(v.State)] {
			continue
		}
		views = append(views, v)
	}
	return views, nil
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
	return s.resolveTx(s.db, feature, token, chainID)
}

// resolveTx is Resolve over a specific DB handle. Passing the open transaction
// keeps every read inside confirmChainTx on the SAME connection — critical when
// the pool is capped to one connection (a separate s.db read would then deadlock
// against the connection the tx already holds).
func (s *FeeRateIntentService) resolveTx(db *gorm.DB, feature, token string, chainID int64) (FeeManagerRate, bool, error) {
	var override models.FeeRateIntent
	err := db.Where("feature = ? AND token = ? AND chain_id = ?", feature, token, chainID).
		First(&override).Error
	if err == nil {
		return rateFromIntent(override), true, nil
	}
	if err != gorm.ErrRecordNotFound {
		return FeeManagerRate{}, false, err
	}
	var global models.FeeRateIntent
	err = db.Where("feature = ? AND token = ? AND chain_id IS NULL", feature, token).
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

// BatchPlanChain is one chain's aggregated update: a single unsigned setRates tx
// that applies every pending change on that chain in ONE signature, plus the
// before→after diff and the intent ids to confirm afterward (against the same
// tx hash).
type BatchPlanChain struct {
	ChainID   int64      `json:"chainId"`
	To        string     `json:"to"`
	Value     string     `json:"value"`
	Data      string     `json:"data"`
	Function  string     `json:"function"`
	Diff      []PlanDiff `json:"diff"`
	IntentIDs []string   `json:"intentIds"`
}

// BatchPlan is the /plan-all response: one setRates per configured chain that has
// at least one pending change differing on-chain. Empty Chains when everything
// already matches (idempotent).
type BatchPlan struct {
	Chains []BatchPlanChain `json:"chains"`
}

// PlanAll bundles ALL pending / partially_applied intents into a single setRates
// tx per configured chain — the "make all changes, sign once" path. For each
// chain it resolves every intent's effective rate, diffs it against the on-chain
// rateFor, keeps only the features that differ, and encodes ONE setRates covering
// them. A chain with no differing features is omitted. Reuses the same per-chain
// resolve/diff logic as Plan; the backend never signs or broadcasts.
func (s *FeeRateIntentService) PlanAll(ctx context.Context) (*BatchPlan, error) {
	if s.cfg == nil || len(s.cfg.FeeManagerChains) == 0 || s.chainReader == nil {
		return nil, ErrFeeManagerNotConfigured
	}
	views, err := s.ListViews(models.FeeRateIntentPending, models.FeeRateIntentPartiallyApplied)
	if err != nil {
		return nil, err
	}

	out := &BatchPlan{Chains: []BatchPlanChain{}}

	// Deterministic chain order for stable plans/tests.
	chainIDs := make([]int64, 0, len(s.cfg.FeeManagerChains))
	for cid := range s.cfg.FeeManagerChains {
		chainIDs = append(chainIDs, cid)
	}
	sort.Slice(chainIDs, func(i, j int) bool { return chainIDs[i] < chainIDs[j] })

	for _, chainID := range chainIDs {
		fmChain := s.cfg.FeeManagerChains[chainID]
		var (
			features, tokens []string
			rates            []FeeManagerRate
			diffs            []PlanDiff
			intentIDs        []string
		)
		for _, v := range views {
			after, found, rerr := s.Resolve(v.Feature, v.Token, chainID)
			if rerr != nil {
				return nil, rerr
			}
			if !found {
				continue // intent doesn't scope this chain
			}
			before, cerr := s.chainReader.RateFor(ctx, chainID, fmChain.Address, v.Feature, v.Token)
			if cerr != nil {
				return nil, fmt.Errorf("read on-chain rateFor chain=%d: %w", chainID, cerr)
			}
			noop := ratesEqual(before, after)
			diffs = append(diffs, PlanDiff{Feature: v.Feature, Token: v.Token, ChainID: chainID, Before: before, After: after, Noop: noop})
			if noop {
				continue // already matches — not in the setRates batch
			}
			features = append(features, v.Feature)
			tokens = append(tokens, v.Token)
			rates = append(rates, after)
			intentIDs = append(intentIDs, v.IntentID)
		}
		if len(features) == 0 {
			continue // nothing differs on this chain
		}
		data, perr := PackSetRates(features, tokens, rates)
		if perr != nil {
			return nil, perr
		}
		out.Chains = append(out.Chains, BatchPlanChain{
			ChainID:   chainID,
			To:        fmChain.Address,
			Value:     "0",
			Data:      "0x" + common.Bytes2Hex(data),
			Function:  SetRatesFunctionSig,
			Diff:      diffs,
			IntentIDs: intentIDs,
		})
	}
	return out, nil
}

// ConfirmAndApply atomically (a) writes the CONFIRMED on-chain rate into the
// fee_config read replica AND (b) advances the intent state machine for one
// chain, in a SINGLE database transaction (findings #3 + #7). The fee_admin
// handler has already verified the receipt status==1 AND read back rateFor ==
// the resolved intent BEFORE calling this — this method only performs the two
// writes, which MUST commit/roll back together so the cache can never hold a
// rate the intent never advanced to (#3), and the global-summary state advance
// uses a compare-and-swap on the intent row so concurrent per-chain confirms
// can't race the summary to a wrong value (#7).
//
// `onchain` is the verified on-chain rate (chain truth, never admin input). On
// success the FeeConfigService cache is reloaded AFTER commit (so a rollback
// leaves the cache untouched). Returns (applied=true) iff this confirmation
// brought ALL in-scope chains to applied.
func (s *FeeRateIntentService) ConfirmAndApply(feeCfg *FeeConfigService, intentID uuid.UUID, chainID int64, onchain FeeManagerRate) (applied bool, err error) {
	if feeCfg == nil {
		return false, fmt.Errorf("ConfirmAndApply: nil fee config service")
	}
	global, err := s.Get(intentID)
	if err != nil {
		return false, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		// (1) Write the replica row INSIDE this tx so it commits/rolls back with
		// the state advance (#3). Use the verified on-chain value.
		if werr := feeCfg.WriteReplicaRowTx(tx, global.Feature, global.Token, &chainID,
			onchain.BPS, flatUnitsToUSD(onchain.FlatUSD), onchain.MCGPExempt, onchain.Active); werr != nil {
			return werr
		}
		// (2) Advance the intent state machine in the SAME tx.
		a, serr := s.confirmChainTx(tx, intentID, chainID)
		if serr != nil {
			return serr
		}
		applied = a
		return nil
	})
	if err != nil {
		return false, err
	}

	// Reload the cache from the now-committed replica (refreshes last-known-good
	// on success). Done AFTER commit so a rolled-back tx never leaves a stale
	// rate in the cache (#3). A reload failure is non-fatal — the next Load /
	// reconciler self-heals; the confirmed write already committed.
	if lerr := feeCfg.Load(); lerr != nil {
		return applied, fmt.Errorf("confirmed + state advanced but cache reload failed (self-heals on next load): %w", lerr)
	}
	return applied, nil
}

// ConfirmChain advances the intent state machine after a single chain's
// FeeManager.setRate has been VERIFIED on-chain (design §5). Kept for callers /
// tests that advance state independently; the production confirm path uses
// ConfirmAndApply to couple the replica write with the state advance atomically.
//
// State transitions (per the design §5 machine):
//
//	pending           --confirm(chainId) ok--> partially_applied (≥1 of N chains done)
//	partially_applied --all in-scope chains confirmed--> applied
//
// "in-scope chains" = the configured FeeManagerChains for which Resolve finds a
// rate. The GLOBAL row's State is the aggregate the admin reads.
//
// Returns (applied=true) iff this confirmation brought ALL in-scope chains to
// applied (the intent is fully done).
func (s *FeeRateIntentService) ConfirmChain(intentID uuid.UUID, chainID int64) (applied bool, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		a, e := s.confirmChainTx(tx, intentID, chainID)
		applied = a
		return e
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// confirmChainTx is the transactional core of the state advance, run inside the
// caller's tx. It (a) flips/creates this chain's per-chain override marker to
// `applied`, and (b) advances the GLOBAL summary row via a COMPARE-AND-SWAP
// (UpdateState ... WHERE state = <expected>) so two concurrent per-chain
// confirms can never both read pending and clobber the summary (#7). The global
// row is also re-read inside the tx (not the pre-tx snapshot) so the CAS sees
// the latest committed state.
func (s *FeeRateIntentService) confirmChainTx(tx *gorm.DB, intentID uuid.UUID, chainID int64) (bool, error) {
	// Re-read the global row INSIDE the tx (a row a concurrent confirm may have
	// already advanced) so the CAS below compares against fresh state.
	var global models.FeeRateIntent
	if err := tx.First(&global, "id = ?", intentID).Error; err != nil {
		return false, err
	}
	if global.State == models.FeeRateIntentApplied {
		return true, nil // already fully applied — idempotent
	}
	if global.State == models.FeeRateIntentCancelled {
		return false, fmt.Errorf("intent %s is cancelled", intentID)
	}

	// Resolve the effective rate for this chain so a default-riding chain (no
	// override row) gets a persisted per-chain marker carrying the same value.
	// Read via the tx (single-connection-pool safe).
	resolved, found, rerr := s.resolveTx(tx, global.Feature, global.Token, chainID)
	if rerr != nil {
		return false, rerr
	}
	if !found {
		return false, fmt.Errorf("chain %d is not in scope for intent %s", chainID, intentID)
	}

	// Upsert this chain's override row in the `applied` state.
	var existing models.FeeRateIntent
	qerr := tx.Where("feature = ? AND token = ? AND chain_id = ?", global.Feature, global.Token, chainID).
		First(&existing).Error
	switch {
	case qerr == nil:
		if err := tx.Model(&models.FeeRateIntent{}).
			Where("id = ?", existing.ID).
			Update("state", models.FeeRateIntentApplied).Error; err != nil {
			return false, err
		}
	case qerr == gorm.ErrRecordNotFound:
		cid := chainID
		if err := tx.Create(&models.FeeRateIntent{
			ID:         uuid.New(),
			Feature:    global.Feature,
			Token:      global.Token,
			ChainID:    &cid,
			BPS:        resolved.BPS,
			FlatUSD:    flatUnitsToUSD(resolved.FlatUSD),
			MCGPExempt: resolved.MCGPExempt,
			Active:     resolved.Active,
			State:      models.FeeRateIntentApplied,
		}).Error; err != nil {
			return false, err
		}
	default:
		return false, qerr
	}

	// The global row advances to partially_applied on the first confirm and
	// applied only when EVERY in-scope chain has an applied per-chain marker.
	// Evaluate coverage INSIDE the tx (over the just-written override row) so a
	// concurrent confirm's marker is visible per the tx isolation level.
	allDone, derr := s.inScopeChainsConfirmedTx(tx, &global, chainID)
	if derr != nil {
		return false, derr
	}
	next := models.FeeRateIntentPartiallyApplied
	if allDone {
		next = models.FeeRateIntentApplied
	}
	// Compare-and-swap the GLOBAL summary state: only advance from the state we
	// observed (#7). A concurrent confirm that already moved it past `expected`
	// fails the WHERE and we re-read to converge. Never DOWNGRADE applied →
	// partially_applied: if another confirm already set applied, leave it.
	expected := global.State
	res := tx.Model(&models.FeeRateIntent{}).
		Where("id = ? AND state = ?", global.ID, expected).
		Update("state", next)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// A concurrent confirm advanced the summary first. Re-read and reconcile:
		// recompute coverage over the now-committed-in-tx markers and set the
		// correct terminal state, again via CAS-from-current.
		var cur models.FeeRateIntent
		if err := tx.First(&cur, "id = ?", global.ID).Error; err != nil {
			return false, err
		}
		if cur.State == models.FeeRateIntentApplied {
			return true, nil
		}
		done2, derr2 := s.inScopeChainsConfirmedTx(tx, &cur, chainID)
		if derr2 != nil {
			return false, derr2
		}
		if done2 {
			if err := tx.Model(&models.FeeRateIntent{}).
				Where("id = ? AND state <> ?", cur.ID, models.FeeRateIntentApplied).
				Update("state", models.FeeRateIntentApplied).Error; err != nil {
				return false, err
			}
		}
		return done2, nil
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
	return s.inScopeChainsConfirmedTx(s.db, global, justConfirmed)
}

// inScopeChainsConfirmedTx is InScopeChainsConfirmed run over a specific DB
// handle (a transaction) so coverage is evaluated against the markers written
// EARLIER IN THE SAME TX (the just-flipped per-chain override). Passing s.db
// gives the original non-transactional behavior.
func (s *FeeRateIntentService) inScopeChainsConfirmedTx(db *gorm.DB, global *models.FeeRateIntent, justConfirmed int64) (bool, error) {
	if s.cfg == nil || len(s.cfg.FeeManagerChains) == 0 {
		return false, nil
	}
	// Load all sibling rows for this (feature, token).
	var rows []models.FeeRateIntent
	if err := db.Where("feature = ? AND token = ?", global.Feature, global.Token).
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
		// In scope? Resolve a rate for this chain (over the same DB handle so a
		// capped single-connection pool inside a tx can't deadlock).
		_, found, err := s.resolveTx(db, global.Feature, global.Token, chainID)
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
	// One transaction so the read-check and the bulk update can't race a
	// concurrent ConfirmChain (which would otherwise apply a sibling row between
	// our Get and Update). FOR UPDATE on the summary row serializes cancels.
	return s.db.Transaction(func(tx *gorm.DB) error {
		var global models.FeeRateIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&global, "id = ?", intentID).Error; err != nil {
			return err
		}
		if global.State == models.FeeRateIntentApplied {
			return fmt.Errorf("intent %s is already fully applied; cannot cancel", intentID)
		}
		// Cancel only NOT-yet-applied rows. An applied per-chain override is live
		// on-chain and reflected in the replica; flipping its intent row to
		// cancelled would desync the ledger from on-chain truth. Leave it; only
		// the pending/planned siblings are cancellable.
		return tx.Model(&models.FeeRateIntent{}).
			Where("feature = ? AND token = ? AND state <> ?",
				global.Feature, global.Token, models.FeeRateIntentApplied).
			Update("state", models.FeeRateIntentCancelled).Error
	})
}
