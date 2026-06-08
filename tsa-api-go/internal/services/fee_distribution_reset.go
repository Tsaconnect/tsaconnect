package services

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Fee/TP v2 cutover RESET tooling (design §3, "Reset/migration").
//
// At the deliberate cutover to the v2 fee model the PM decision is to RESET
// balances to zero (no grandfathering — design §0). This file provides the
// GUARDED one-shot that does a FULL reset so a re-mint under v2 cannot
// double-pay and no half-migrated v2 state survives:
//
//  1. Archives tp_earnings + cashback_earnings (and the per-user tp_balance /
//     cashback_balance snapshots) into *_legacy tables (audit trail), THEN
//     DELETES the live tp_earnings + cashback_earnings rows. Deleting the live
//     cashback rows is what stops SP5 convertPendingCashback from ever seeing a
//     pre-cutover CashbackEarning and minting it again as MCGP.
//  2. CLEARS every v2 reward-state table so the engine starts from a clean
//     slate: reward_entries, reward_claimables, user_reward_states,
//     fee_distributions, global_tp_accruals, global_tp_epochs, reward_flags;
//     resets the global_tp_pool singleton (accrued/index/indexed → 0); and sets
//     users.last_settled_global_index = NULL (so a fresh global index starts at
//     zero for everyone) while zeroing tp_balance / cashback_balance.
//
// It is intentionally NOT wired into AutoMigrate, the scheduler, or any boot
// path. It must be invoked deliberately — from the super-admin-gated endpoint
// (POST /api/admin/fee-tp/cutover-reset) or a CLI one-shot — and only ONCE, at
// cutover, STRICTLY BEFORE flipping FEE_MODEL_V2_ENABLED. The RequireConfirm
// guard ("I_UNDERSTAND_THIS_ZEROES_BALANCES") makes an accidental call a no-op
// error rather than a silent wipe.
//
// CUTOVER RUNBOOK (order matters):
//
//  1. Deploy the MCGPRewardDistributor contract (SP6) + fund treasury.
//  2. Run this FULL reset (ResetForV2Cutover) — archive+delete the legacy
//     ledgers and clear ALL v2 state. This MUST happen BEFORE the flag flip so
//     the v2 engine never accrues on top of un-cleared pre-cutover state.
//  3. Flip FEE_MODEL_V2_ENABLED=true — the v2 fee/TP engine goes live.
//  4. Publish the first merkle root (SP6) once post-cutover rewards accrue.
//
// Idempotency: a per-label sentinel (cutover_batches) records each completed
// batch; the function refuses to run twice for the same label, so a
// double-invocation cannot double-archive, re-zero re-earned balances, or
// re-clear freshly-accrued v2 state.

// ResetConfirmPhrase is the exact string a caller must pass to actually run the
// reset. Anything else makes ResetForV2Cutover a no-op error.
const ResetConfirmPhrase = "I_UNDERSTAND_THIS_ZEROES_BALANCES"

// ResetSummary reports what a cutover reset archived, deleted, and cleared.
type ResetSummary struct {
	Label               string    `json:"label"`
	TPEarningsArchived  int64     `json:"tpEarningsArchived"`
	TPEarningsDeleted   int64     `json:"tpEarningsDeleted"`
	CashbackArchived    int64     `json:"cashbackArchived"`
	CashbackDeleted     int64     `json:"cashbackDeleted"`
	BalancesSnapshotted int64     `json:"balancesSnapshotted"`
	UsersZeroed         int64     `json:"usersZeroed"`
	V2StateCleared      int64     `json:"v2StateCleared"`
	ArchivedAt          time.Time `json:"archivedAt"`
}

// ResetForV2Cutover performs the guarded one-time cutover reset. See file
// header. `label` tags the archive batch (e.g. "2026-06-07-cutover") and makes
// a re-run with the same label refuse. `confirm` MUST equal ResetConfirmPhrase.
//
// All work runs in a single transaction so a failure leaves balances AND v2
// state intact (an archive+delete+clear that fails halfway never leaves a
// half-migrated DB).
func ResetForV2Cutover(ctx context.Context, db *gorm.DB, label, confirm string) (*ResetSummary, error) {
	if db == nil {
		return nil, fmt.Errorf("reset: nil db")
	}
	if confirm != ResetConfirmPhrase {
		return nil, fmt.Errorf("reset: refusing to run — pass confirm=%q to proceed", ResetConfirmPhrase)
	}
	if label == "" {
		return nil, fmt.Errorf("reset: a non-empty label is required (e.g. a cutover date)")
	}

	now := time.Now()
	summary := &ResetSummary{Label: label, ArchivedAt: now}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Ensure the *_legacy archive tables exist. Types must be PORTABLE
		//    across PostgreSQL (production) AND SQLite (tests): use TIMESTAMP (NOT
		//    DATETIME — PostgreSQL has no DATETIME type, which would error and roll
		//    the whole cutover back, leaving balances un-zeroed) and DOUBLE
		//    PRECISION (NOT REAL — REAL is a 4-byte float on PostgreSQL and would
		//    lose precision on money snapshots). SQLite accepts both as
		//    type-affinity tokens, so the same DDL runs in both engines. The schema
		//    mirrors the live ledgers plus the batch label + archive timestamp.
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS tp_earnings_legacy (
			id TEXT,
			user_id TEXT,
			source_user_id TEXT,
			source_type TEXT,
			source_id TEXT,
			generation INTEGER,
			fee_amount_usd DOUBLE PRECISION,
			percentage DOUBLE PRECISION,
			tp_earned DOUBLE PRECISION,
			created_at TIMESTAMP,
			cutover_label TEXT,
			cutover_archived_at TIMESTAMP
		)`).Error; err != nil {
			return fmt.Errorf("create tp_earnings_legacy: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS cashback_earnings_legacy (
			id TEXT,
			user_id TEXT,
			source_user_id TEXT,
			source_type TEXT,
			source_id TEXT,
			amount_usd DOUBLE PRECISION,
			tx_hash TEXT,
			created_at TIMESTAMP,
			cutover_label TEXT,
			cutover_archived_at TIMESTAMP
		)`).Error; err != nil {
			return fmt.Errorf("create cashback_earnings_legacy: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS user_balances_legacy (
			user_id TEXT,
			tp_balance DOUBLE PRECISION,
			cashback_balance DOUBLE PRECISION,
			cutover_label TEXT,
			cutover_archived_at TIMESTAMP
		)`).Error; err != nil {
			return fmt.Errorf("create user_balances_legacy: %w", err)
		}
		// A per-label sentinel table: one row per completed cutover batch. This is
		// the authoritative re-run guard — counting user_balances_legacy rows is
		// NOT safe because an empty users table snapshots 0 rows, leaving the old
		// prior==0 check unsatisfied and permitting a second destructive run.
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS cutover_batches (
			label TEXT PRIMARY KEY,
			completed_at TIMESTAMP
		)`).Error; err != nil {
			return fmt.Errorf("create cutover_batches: %w", err)
		}

		// 2. Refuse to re-run for the same label (idempotency / accident guard).
		//    Keyed on the sentinel table, NOT on archived row counts, so it holds
		//    even when there were zero users/ledger rows to archive.
		var prior int64
		if err := tx.Raw(`SELECT COUNT(*) FROM cutover_batches WHERE label = ?`, label).
			Scan(&prior).Error; err != nil {
			return fmt.Errorf("check prior archive: %w", err)
		}
		if prior > 0 {
			return fmt.Errorf("reset: label %q already completed — refusing to re-run", label)
		}

		// 3. Archive the two legacy ledgers, THEN delete the live rows. The
		//    *_legacy copies preserve the full history; deleting the live rows is
		//    what guarantees the v2 engine (notably SP5 convertPendingCashback)
		//    can never see a pre-cutover CashbackEarning / TPEarning and re-mint
		//    it. Archive-before-delete, both in this tx, so a failure preserves the
		//    live ledger.
		res := tx.Exec(`INSERT INTO tp_earnings_legacy
			(id, user_id, source_user_id, source_type, source_id, generation, fee_amount_usd, percentage, tp_earned, created_at, cutover_label, cutover_archived_at)
			SELECT id, user_id, source_user_id, source_type, source_id, generation, fee_amount_usd, percentage, tp_earned, created_at, ?, ?
			FROM tp_earnings`, label, now)
		if res.Error != nil {
			return fmt.Errorf("archive tp_earnings: %w", res.Error)
		}
		summary.TPEarningsArchived = res.RowsAffected

		res = tx.Exec(`DELETE FROM tp_earnings`)
		if res.Error != nil {
			return fmt.Errorf("delete tp_earnings: %w", res.Error)
		}
		summary.TPEarningsDeleted = res.RowsAffected

		res = tx.Exec(`INSERT INTO cashback_earnings_legacy
			(id, user_id, source_user_id, source_type, source_id, amount_usd, tx_hash, created_at, cutover_label, cutover_archived_at)
			SELECT id, user_id, source_user_id, source_type, source_id, amount_usd, tx_hash, created_at, ?, ?
			FROM cashback_earnings`, label, now)
		if res.Error != nil {
			return fmt.Errorf("archive cashback_earnings: %w", res.Error)
		}
		summary.CashbackArchived = res.RowsAffected

		res = tx.Exec(`DELETE FROM cashback_earnings`)
		if res.Error != nil {
			return fmt.Errorf("delete cashback_earnings: %w", res.Error)
		}
		summary.CashbackDeleted = res.RowsAffected

		// 4. Snapshot per-user balances before zeroing.
		res = tx.Exec(`INSERT INTO user_balances_legacy
			(user_id, tp_balance, cashback_balance, cutover_label, cutover_archived_at)
			SELECT id, tp_balance, cashback_balance, ?, ?
			FROM users`, label, now)
		if res.Error != nil {
			return fmt.Errorf("snapshot balances: %w", res.Error)
		}
		summary.BalancesSnapshotted = res.RowsAffected

		// 5. Zero the live balances AND reset the global-pool checkpoint so every
		//    user starts the v2 era owed nothing from the (now-cleared) global pool.
		res = tx.Exec(`UPDATE users SET tp_balance = 0, cashback_balance = 0, last_settled_global_index = NULL`)
		if res.Error != nil {
			return fmt.Errorf("zero balances: %w", res.Error)
		}
		summary.UsersZeroed = res.RowsAffected

		// 6. CLEAR every v2 reward-state table. A half-migration (legacy zeroed but
		//    v2 state retained) would let a re-mint double-pay or settle stale
		//    pool/level/claimable state. Each table is cleared only if it exists
		//    (HasTable is portable across PostgreSQL + SQLite) so this is safe in
		//    test schemas that don't create every table, and in early deployments
		//    where a dormant v2 table hasn't been migrated yet.
		v2Tables := []string{
			"reward_entries",
			"reward_claimables",
			"user_reward_states",
			"fee_distributions",
			"global_tp_accruals",
			"global_tp_epochs",
			"reward_flags",
		}
		for _, name := range v2Tables {
			if !tx.Migrator().HasTable(name) {
				continue
			}
			r := tx.Exec(fmt.Sprintf(`DELETE FROM %s`, name))
			if r.Error != nil {
				return fmt.Errorf("clear %s: %w", name, r.Error)
			}
			summary.V2StateCleared += r.RowsAffected
		}

		// 7. Reset the global_tp_pool singleton (accrued + cumulative index +
		//    indexed-high-water all back to 0) so SP4 settlement starts fresh. Only
		//    if the table exists.
		if tx.Migrator().HasTable("global_tp_pool") {
			if err := tx.Exec(`UPDATE global_tp_pool
				SET accrued_usd = 0, accrued_tp = 0, per_user_index_scaled = 0, indexed_tp = 0, updated_at = ?`, now).Error; err != nil {
				return fmt.Errorf("reset global_tp_pool: %w", err)
			}
		}

		// 8. Record the completed-batch sentinel LAST (inside the same tx) so the
		//    re-run guard is satisfied for this label regardless of how many rows
		//    were archived/deleted/cleared. If anything above failed the tx rolled
		//    back and no sentinel exists, so a corrected re-run is allowed.
		if err := tx.Exec(`INSERT INTO cutover_batches (label, completed_at) VALUES (?, ?)`, label, now).Error; err != nil {
			return fmt.Errorf("record cutover sentinel: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return summary, nil
}

// CutoverTimestamp returns the created_at of the LATEST completed cutover batch
// (the cutover_batches sentinel). It is the settlement-timestamp FLOOR (design
// §3, "cutover keyed on settlement timestamp") that SP5 cashback conversion uses
// to refuse pre-cutover cashback rows belt-and-suspenders with the DELETE above.
//
// If no cutover has ever run (the sentinel table is absent or empty) it returns
// the zero time so callers treat the floor as "no floor" — every row is >=
// zero-time, exactly the pre-cutover behavior. PORTABLE: HasTable guards SQLite
// test schemas that never created the sentinel table.
func CutoverTimestamp(ctx context.Context, db *gorm.DB) (time.Time, error) {
	if db == nil {
		return time.Time{}, fmt.Errorf("cutover timestamp: nil db")
	}
	if !db.WithContext(ctx).Migrator().HasTable("cutover_batches") {
		return time.Time{}, nil
	}
	var at *time.Time
	err := db.WithContext(ctx).
		Raw(`SELECT completed_at FROM cutover_batches ORDER BY completed_at DESC LIMIT 1`).
		Scan(&at).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("read cutover timestamp: %w", err)
	}
	if at == nil {
		return time.Time{}, nil
	}
	return *at, nil
}
