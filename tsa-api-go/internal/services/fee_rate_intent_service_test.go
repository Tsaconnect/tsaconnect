package services

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupIntentTestDB creates the fee_rate_intents + fee_configs tables for the
// configurable-FeeManager admin flow tests (SQLite in-memory, raw DDL — matches
// the fee_config chain-test pattern).
func setupIntentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// An in-memory SQLite database is PER-CONNECTION: a multi-statement
	// transaction that grabs a second pooled connection would see an empty DB
	// ("no such table"). Cap the pool to 1 so the DDL below and every later tx
	// share the one connection. (Test-only; mirrors the standard sqlite-:memory:
	// fix.)
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.Exec(`CREATE TABLE fee_rate_intents (
		id TEXT PRIMARY KEY,
		feature TEXT NOT NULL,
		token TEXT NOT NULL,
		chain_id INTEGER,
		bps INTEGER NOT NULL DEFAULT 0,
		flat_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		state TEXT NOT NULL DEFAULT 'draft',
		created_at DATETIME,
		updated_at DATETIME,
		UNIQUE(feature, token, chain_id)
	)`).Error; err != nil {
		t.Fatalf("create fee_rate_intents: %v", err)
	}
	if err := db.Exec(`CREATE TABLE fee_configs (
		id TEXT PRIMARY KEY,
		feature TEXT NOT NULL,
		token_symbol TEXT NOT NULL,
		chain_id INTEGER,
		bps INTEGER NOT NULL DEFAULT 0,
		flat_amount_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME,
		UNIQUE(feature, token_symbol, chain_id)
	)`).Error; err != nil {
		t.Fatalf("create fee_configs: %v", err)
	}
	return db
}

// stubFeeManagerReader is an in-memory FeeManagerChainReader. rates maps
// "chainID|feature|token" → the on-chain rate; txOK reports receipt success per
// txHash; rateErr / txErr force read failures for negative paths.
type stubFeeManagerReader struct {
	rates   map[string]FeeManagerRate
	txOK    map[string]bool
	rateErr error
	txErr   error
}

func newStubReader() *stubFeeManagerReader {
	return &stubFeeManagerReader{rates: map[string]FeeManagerRate{}, txOK: map[string]bool{}}
}

func rk(chainID int64, feature, token string) string {
	return strconv.FormatInt(chainID, 10) + "|" + feature + "|" + strings.ToUpper(token)
}

func (s *stubFeeManagerReader) set(chainID int64, feature, token string, r FeeManagerRate) {
	s.rates[rk(chainID, feature, token)] = r
}

func (s *stubFeeManagerReader) RateFor(_ context.Context, chainID int64, _ /*feeManager*/, feature, token string) (FeeManagerRate, error) {
	if s.rateErr != nil {
		return FeeManagerRate{}, s.rateErr
	}
	if r, ok := s.rates[rk(chainID, feature, token)]; ok {
		return r, nil
	}
	return FeeManagerRate{}, nil // inactive/zero (active=false) — never configured
}

func (s *stubFeeManagerReader) TxSucceeded(_ context.Context, _ int64, txHash string) (bool, error) {
	if s.txErr != nil {
		return false, s.txErr
	}
	return s.txOK[txHash], nil
}

// cfgWithChains builds a config with the given FeeManager chains configured.
func cfgWithChains(chains ...int64) *config.Config {
	c := &config.Config{FeeManagerChains: map[int64]config.FeeManagerChainConfig{}}
	for i, id := range chains {
		key := "sonic"
		if i == 1 {
			key = "bsc"
		}
		c.FeeManagerChains[id] = config.FeeManagerChainConfig{
			ChainID:  id,
			ChainKey: key,
			Address:  "0xFeeManager" + strconv.FormatInt(id, 10),
		}
	}
	return c
}

const (
	sonicC int64 = 146
	bscC   int64 = 56
)

// TestPlanProducesSetRateCalldata: a single-chain intent at 60 bps vs an on-chain
// 50 bps produces a diff (noop=false) and exactly one setRate tx with non-empty
// decodable calldata.
func TestPlanProducesSetRateCalldata(t *testing.T) {
	db := setupIntentTestDB(t)
	reader := newStubReader()
	reader.set(sonicC, models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true})

	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), reader)
	intent, err := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 60, Active: true}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	plan, err := svc.Plan(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Diff) != 1 || plan.Diff[0].Noop {
		t.Fatalf("expected 1 non-noop diff, got %+v", plan.Diff)
	}
	if plan.Diff[0].Before.BPS != 50 || plan.Diff[0].After.BPS != 60 {
		t.Fatalf("diff before/after = %d/%d, want 50/60", plan.Diff[0].Before.BPS, plan.Diff[0].After.BPS)
	}
	if len(plan.Txs) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(plan.Txs))
	}
	tx := plan.Txs[0]
	if tx.ChainID != sonicC || tx.Function != SetRateFunctionSig {
		t.Fatalf("tx chain/func = %d/%s", tx.ChainID, tx.Function)
	}
	// The calldata must decode back to setRate(withdrawal, *, (60,0,false,true)).
	raw := common.FromHex(tx.Data)
	if len(raw) < 4 {
		t.Fatalf("calldata too short: %s", tx.Data)
	}
	method, err := parsedFeeManagerABI.MethodById(raw[:4])
	if err != nil || method.Name != "setRate" {
		t.Fatalf("decode method: %v (%v)", err, method)
	}
	args, err := method.Inputs.Unpack(raw[4:])
	if err != nil {
		t.Fatalf("unpack args: %v", err)
	}
	if args[0].(string) != models.FeatureWithdrawal || args[1].(string) != "*" {
		t.Fatalf("decoded feature/token = %v/%v", args[0], args[1])
	}
}

// TestPlanEmptyOnNoop: when the intent already matches the on-chain rate, Plan
// returns the diff with noop=true and NO txs (idempotent).
func TestPlanEmptyOnNoop(t *testing.T) {
	db := setupIntentTestDB(t)
	reader := newStubReader()
	reader.set(sonicC, models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true})

	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), reader)
	intent, _ := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil)

	plan, err := svc.Plan(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Txs) != 0 {
		t.Fatalf("expected 0 txs (idempotent no-op), got %d", len(plan.Txs))
	}
	if len(plan.Diff) != 1 || !plan.Diff[0].Noop {
		t.Fatalf("expected 1 noop diff, got %+v", plan.Diff)
	}
}

// TestResolveOverrideBeatsGlobal: Resolve returns the per-chain override when set,
// else the global default.
func TestResolveOverrideBeatsGlobal(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC, bscC), nil)
	_, err := svc.Create(models.FeatureWithdrawal, "*",
		FeeManagerRate{BPS: 50, Active: true},
		map[int64]FeeManagerRate{bscC: {BPS: 60, Active: true}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// BSC resolves to the override (60).
	r, found, err := svc.Resolve(models.FeatureWithdrawal, "*", bscC)
	if err != nil || !found || r.BPS != 60 {
		t.Fatalf("bsc resolve = %d found=%v err=%v, want 60", r.BPS, found, err)
	}
	// Sonic (no override) resolves to the global default (50).
	r, found, err = svc.Resolve(models.FeatureWithdrawal, "*", sonicC)
	if err != nil || !found || r.BPS != 50 {
		t.Fatalf("sonic resolve = %d found=%v err=%v, want 50", r.BPS, found, err)
	}
}

// TestListViewsGroupsRowsIntoLogicalIntents: the flat default + override rows for
// a (feature, token) collapse into one IntentView whose IntentID/State come from
// the global row and whose per-chain overrides are keyed by chainId.
func TestListViewsGroupsRowsIntoLogicalIntents(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC, bscC), nil)

	wIntent, err := svc.Create(models.FeatureWithdrawal, "*",
		FeeManagerRate{BPS: 50, Active: true},
		map[int64]FeeManagerRate{bscC: {BPS: 60, Active: true}})
	if err != nil {
		t.Fatalf("create withdrawal: %v", err)
	}
	if _, err := svc.Create(models.FeatureP2P, "*", FeeManagerRate{BPS: 100, Active: true}, nil); err != nil {
		t.Fatalf("create p2p: %v", err)
	}

	views, err := svc.ListViews()
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 logical intents, got %d", len(views))
	}

	byFeature := map[string]IntentView{}
	for _, v := range views {
		byFeature[v.Feature] = v
	}

	w := byFeature[models.FeatureWithdrawal]
	if w.IntentID != wIntent.ID.String() {
		t.Fatalf("withdrawal intentId = %s, want the global row id %s", w.IntentID, wIntent.ID)
	}
	if w.State != string(models.FeeRateIntentPending) {
		t.Fatalf("withdrawal state = %s, want pending", w.State)
	}
	if w.Default == nil || w.Default.BPS != 50 {
		t.Fatalf("withdrawal default = %+v, want bps 50", w.Default)
	}
	ov, ok := w.Overrides[strconv.FormatInt(bscC, 10)]
	if !ok || ov.BPS != 60 {
		t.Fatalf("withdrawal bsc override = %+v ok=%v, want bps 60", ov, ok)
	}

	p := byFeature[models.FeatureP2P]
	if p.Default == nil || p.Default.BPS != 100 || len(p.Overrides) != 0 {
		t.Fatalf("p2p view = %+v, want default bps 100 and no overrides", p)
	}

	// State filter: nothing is applied yet.
	if applied, _ := svc.ListViews(models.FeeRateIntentApplied); len(applied) != 0 {
		t.Fatalf("expected 0 applied intents, got %d", len(applied))
	}
	if pending, _ := svc.ListViews(models.FeeRateIntentPending); len(pending) != 2 {
		t.Fatalf("expected 2 pending intents, got %d", len(pending))
	}
}

// TestListViewsMultiStateFilterIsOR: ListViews(pending, partially_applied) must
// return intents in EITHER state (the OR filter the dashboard's pending-changes
// panel depends on), while a single-state filter narrows to just that state.
func TestListViewsMultiStateFilterIsOR(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), nil)

	if _, err := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil); err != nil {
		t.Fatalf("create withdrawal: %v", err)
	}
	p2p, err := svc.Create(models.FeatureP2P, "*", FeeManagerRate{BPS: 100, Active: true}, nil)
	if err != nil {
		t.Fatalf("create p2p: %v", err)
	}
	// Advance the p2p intent's summary (global) row to partially_applied.
	if err := svc.UpdateState(p2p.ID, models.FeeRateIntentPartiallyApplied); err != nil {
		t.Fatalf("update state: %v", err)
	}

	both, err := svc.ListViews(models.FeeRateIntentPending, models.FeeRateIntentPartiallyApplied)
	if err != nil {
		t.Fatalf("ListViews OR: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("OR filter returned %d, want 2 (one pending + one partially_applied)", len(both))
	}

	pending, err := svc.ListViews(models.FeeRateIntentPending)
	if err != nil {
		t.Fatalf("ListViews pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Feature != models.FeatureWithdrawal {
		t.Fatalf("single-state filter = %+v, want only the pending withdrawal", pending)
	}
}

// TestPlanAllBundlesPendingIntoOneSetRates: two pending intents collapse into a
// single setRates tx (one per chain) carrying both features + both intent ids;
// when the on-chain rates already match, PlanAll returns no chains.
func TestPlanAllBundlesPendingIntoOneSetRates(t *testing.T) {
	db := setupIntentTestDB(t)
	reader := newStubReader() // unset features read back as 0/inactive on-chain
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), reader)

	w, err := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil)
	if err != nil {
		t.Fatalf("create withdrawal: %v", err)
	}
	p, err := svc.Create(models.FeatureP2P, "*", FeeManagerRate{BPS: 100, Active: true}, nil)
	if err != nil {
		t.Fatalf("create p2p: %v", err)
	}

	plan, err := svc.PlanAll(context.Background())
	if err != nil {
		t.Fatalf("PlanAll: %v", err)
	}
	if len(plan.Chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(plan.Chains))
	}
	ch := plan.Chains[0]
	if ch.ChainID != sonicC || ch.Function != SetRatesFunctionSig {
		t.Fatalf("chain=%d fn=%s", ch.ChainID, ch.Function)
	}
	if len(ch.IntentIDs) != 2 {
		t.Fatalf("expected 2 intentIds, got %v", ch.IntentIDs)
	}
	ids := map[string]bool{ch.IntentIDs[0]: true, ch.IntentIDs[1]: true}
	if !ids[w.ID.String()] || !ids[p.ID.String()] {
		t.Fatalf("intentIds %v missing %s / %s", ch.IntentIDs, w.ID, p.ID)
	}

	// The single calldata decodes back to a setRates over both features.
	method := parsedFeeManagerABI.Methods["setRates"]
	raw := common.FromHex(ch.Data)
	if len(raw) < 4 || common.Bytes2Hex(raw[:4]) != common.Bytes2Hex(method.ID) {
		t.Fatalf("data does not start with the setRates selector")
	}
	args, uerr := method.Inputs.Unpack(raw[4:])
	if uerr != nil {
		t.Fatalf("unpack setRates: %v", uerr)
	}
	if features, ok := args[0].([]string); !ok || len(features) != 2 {
		t.Fatalf("decoded features = %v (want 2)", args[0])
	}

	// When on-chain already matches every intent, PlanAll is empty (idempotent).
	reader.set(sonicC, models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true})
	reader.set(sonicC, models.FeatureP2P, "*", FeeManagerRate{BPS: 100, Active: true})
	empty, err := svc.PlanAll(context.Background())
	if err != nil {
		t.Fatalf("PlanAll (matched): %v", err)
	}
	if len(empty.Chains) != 0 {
		t.Fatalf("expected no chains when on-chain matches, got %d", len(empty.Chains))
	}
}

// TestPlanMultiChainOverride: a global default + a BSC override yields a per-chain
// plan that proposes 50 on Sonic and 60 on BSC.
func TestPlanMultiChainOverride(t *testing.T) {
	db := setupIntentTestDB(t)
	reader := newStubReader()
	// Both chains currently at 0/inactive on-chain.
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC, bscC), reader)
	intent, _ := svc.Create(models.FeatureWithdrawal, "*",
		FeeManagerRate{BPS: 50, Active: true},
		map[int64]FeeManagerRate{bscC: {BPS: 60, Active: true}})

	plan, err := svc.Plan(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Txs) != 2 {
		t.Fatalf("expected 2 txs (sonic+bsc), got %d", len(plan.Txs))
	}
	got := map[int64]uint16{}
	for _, tx := range plan.Txs {
		got[tx.ChainID] = tx.Decoded.Rate.BPS
	}
	if got[sonicC] != 50 || got[bscC] != 60 {
		t.Fatalf("plan rates sonic=%d bsc=%d, want 50/60", got[sonicC], got[bscC])
	}
}

// TestStateMachinePartiallyAppliedAcrossTwoChains: confirming chain A moves the
// intent to partially_applied; confirming chain B moves it to applied.
func TestStateMachinePartiallyAppliedAcrossTwoChains(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC, bscC), nil)
	intent, _ := svc.Create(models.FeatureWithdrawal, "*",
		FeeManagerRate{BPS: 50, Active: true},
		map[int64]FeeManagerRate{bscC: {BPS: 60, Active: true}})

	// Initial state is pending.
	g, _ := svc.Get(intent.ID)
	if g.State != models.FeeRateIntentPending {
		t.Fatalf("initial state = %s, want pending", g.State)
	}

	// Confirm Sonic → partially_applied (BSC still pending).
	applied, err := svc.ConfirmChain(intent.ID, sonicC)
	if err != nil {
		t.Fatalf("confirm sonic: %v", err)
	}
	if applied {
		t.Fatalf("intent should NOT be fully applied after 1 of 2 chains")
	}
	g, _ = svc.Get(intent.ID)
	if g.State != models.FeeRateIntentPartiallyApplied {
		t.Fatalf("state after sonic = %s, want partially_applied", g.State)
	}

	// Confirm BSC → applied.
	applied, err = svc.ConfirmChain(intent.ID, bscC)
	if err != nil {
		t.Fatalf("confirm bsc: %v", err)
	}
	if !applied {
		t.Fatalf("intent should be fully applied after both chains")
	}
	g, _ = svc.Get(intent.ID)
	if g.State != models.FeeRateIntentApplied {
		t.Fatalf("state after bsc = %s, want applied", g.State)
	}
}

// TestCancelOnlyWhileNotApplied: cancel succeeds while pending and is rejected
// after the intent is fully applied.
func TestCancelOnlyWhileNotApplied(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), nil)
	intent, _ := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil)

	if err := svc.Cancel(intent.ID); err != nil {
		t.Fatalf("cancel while pending should succeed: %v", err)
	}
	g, _ := svc.Get(intent.ID)
	if g.State != models.FeeRateIntentCancelled {
		t.Fatalf("state = %s, want cancelled", g.State)
	}

	// Re-author + fully apply, then cancel must fail.
	intent2, _ := svc.Create(models.FeatureSwap, "*", FeeManagerRate{BPS: 10, Active: true}, nil)
	if _, err := svc.ConfirmChain(intent2.ID, sonicC); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := svc.Cancel(intent2.ID); err == nil {
		t.Fatalf("cancel of an applied intent should fail")
	}
}

// feeConfigOverIntentDB builds a FeeConfigService backed by the SAME DB as the
// intent service (the fee_configs table created by setupIntentTestDB). It does
// NOT seed (the table starts empty) so the replica-write/rollback assertions are
// unambiguous.
func feeConfigOverIntentDB(db *gorm.DB) *FeeConfigService {
	return &FeeConfigService{db: db, cache: map[string]models.FeeConfig{}, lastKnownGood: map[string]models.FeeConfig{}}
}

func feeConfigRowCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.FeeConfig{}).Count(&n).Error; err != nil {
		t.Fatalf("count fee_configs: %v", err)
	}
	return n
}

// TestConfirmAndApply_AtomicRollback (finding #3): a forced failure in the intent
// STATE advance must roll back the fee_config replica write made earlier in the
// SAME transaction — the cache must never hold a rate the intent never advanced
// to. WriteReplicaRowTx runs FIRST inside the tx; we force confirmChainTx to fail
// by confirming a CANCELLED intent (a deterministic, realistic state-advance
// error that fires AFTER the replica write within the same tx).
func TestConfirmAndApply_AtomicRollback(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), nil)
	feeCfg := feeConfigOverIntentDB(db)

	intent, err := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n := feeConfigRowCount(t, db); n != 0 {
		t.Fatalf("precondition: fee_configs should be empty, got %d", n)
	}
	// Cancel the intent so the in-tx state advance (confirmChainTx) errors AFTER
	// WriteReplicaRowTx has already written the replica row inside the tx.
	if err := svc.Cancel(intent.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	applied, cerr := svc.ConfirmAndApply(feeCfg, intent.ID, sonicC, FeeManagerRate{BPS: 50, Active: true})
	if cerr == nil {
		t.Fatalf("expected ConfirmAndApply to fail for a cancelled intent")
	}
	if applied {
		t.Fatalf("applied should be false on failure")
	}

	// The replica write must have rolled back — NO fee_config row persisted, and
	// the live cache must NOT hold the rate either.
	if n := feeConfigRowCount(t, db); n != 0 {
		t.Fatalf("replica write was NOT rolled back: fee_configs has %d rows, want 0", n)
	}
	feeCfg.mu.RLock()
	cacheLen := len(feeCfg.cache)
	feeCfg.mu.RUnlock()
	if cacheLen != 0 {
		t.Fatalf("cache holds %d rows after a rolled-back confirm, want 0", cacheLen)
	}
}

// TestConfirmAndApply_HappyPath: a confirmed single-chain intent writes the
// replica AND advances the state to applied, both committed together.
func TestConfirmAndApply_HappyPath(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC), nil)
	feeCfg := feeConfigOverIntentDB(db)

	intent, _ := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil)

	applied, err := svc.ConfirmAndApply(feeCfg, intent.ID, sonicC, FeeManagerRate{BPS: 50, Active: true})
	if err != nil {
		t.Fatalf("ConfirmAndApply: %v", err)
	}
	if !applied {
		t.Fatalf("single in-scope chain should be fully applied")
	}
	// Replica row written + cache loaded.
	if n := feeConfigRowCount(t, db); n != 1 {
		t.Fatalf("expected 1 replica row, got %d", n)
	}
	bps, _, _, found := feeCfg.RateFor(models.FeatureWithdrawal, "USDT", &[]int64{sonicC}[0])
	if !found || bps != 50 {
		t.Fatalf("cache RateFor = %d found=%v, want 50/true", bps, found)
	}
	g, _ := svc.Get(intent.ID)
	if g.State != models.FeeRateIntentApplied {
		t.Fatalf("intent state = %s, want applied", g.State)
	}
}

// TestConfirmChain_ConcurrentConvergesToApplied (finding #7): two concurrent
// per-chain confirms on a two-chain intent must converge to `applied` (not get
// stuck at partially_applied or race the summary to a wrong value). The
// compare-and-swap on the global summary row makes the advance race-safe.
func TestConfirmChain_ConcurrentConvergesToApplied(t *testing.T) {
	db := setupIntentTestDB(t)
	svc := NewFeeRateIntentService(db, cfgWithChains(sonicC, bscC), nil)

	// Global default scopes BOTH chains (no per-chain override).
	intent, _ := svc.Create(models.FeatureWithdrawal, "*", FeeManagerRate{BPS: 50, Active: true}, nil)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	chains := []int64{sonicC, bscC}
	wg.Add(2)
	for i, ch := range chains {
		go func(i int, ch int64) {
			defer wg.Done()
			_, errs[i] = svc.ConfirmChain(intent.ID, ch)
		}(i, ch)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent confirm[%d] error: %v", i, e)
		}
	}

	// After BOTH chains confirm, the summary must be `applied` — never stuck at
	// partially_applied, never an inconsistent state.
	g, _ := svc.Get(intent.ID)
	if g.State != models.FeeRateIntentApplied {
		t.Fatalf("after concurrent confirms state = %s, want applied", g.State)
	}
	// Both per-chain markers must be applied.
	if done, _ := svc.InScopeChainsConfirmed(g, -1); !done {
		t.Fatalf("not all in-scope chains marked confirmed after concurrent confirms")
	}
}
