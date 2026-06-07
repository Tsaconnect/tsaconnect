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
// GUARDED one-shot that does it:
//
//  1. Archives tp_earnings, cashback_earnings, and the per-user tp_balance /
//     cashback_balance snapshots into *_legacy tables (audit trail).
//  2. Zeros every user's tp_balance and cashback_balance.
//
// It is intentionally NOT wired into AutoMigrate, the scheduler, or any boot
// path. It must be invoked deliberately — from an admin endpoint or a CLI
// one-shot — and only ONCE, at cutover, AFTER flipping FEE_MODEL_V2_ENABLED.
// The RequireConfirm guard ("I_UNDERSTAND_THIS_ZEROES_BALANCES") makes an
// accidental call a no-op error rather than a silent wipe.
//
// Idempotency: the archive tables are created with a cutover_archived_at
// timestamp and the function refuses to run twice (it errors if a prior
// archive batch with the same label already exists), so a double-invocation
// cannot double-archive or re-zero balances that were already legitimately
// re-earned under v2.

// ResetConfirmPhrase is the exact string a caller must pass to actually run the
// reset. Anything else makes ResetForV2Cutover a no-op error.
const ResetConfirmPhrase = "I_UNDERSTAND_THIS_ZEROES_BALANCES"

// ResetSummary reports what a cutover reset archived and zeroed.
type ResetSummary struct {
	Label                 string    `json:"label"`
	TPEarningsArchived    int64     `json:"tpEarningsArchived"`
	CashbackArchived      int64     `json:"cashbackArchived"`
	BalancesSnapshotted   int64     `json:"balancesSnapshotted"`
	UsersZeroed           int64     `json:"usersZeroed"`
	ArchivedAt            time.Time `json:"archivedAt"`
}

// ResetForV2Cutover performs the guarded one-time cutover reset. See file
// header. `label` tags the archive batch (e.g. "2026-06-07-cutover") and makes
// a re-run with the same label refuse. `confirm` MUST equal ResetConfirmPhrase.
//
// All work runs in a single transaction so a failure leaves balances intact.
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

		// 3. Archive the two ledgers.
		res := tx.Exec(`INSERT INTO tp_earnings_legacy
			(id, user_id, source_user_id, source_type, source_id, generation, fee_amount_usd, percentage, tp_earned, created_at, cutover_label, cutover_archived_at)
			SELECT id, user_id, source_user_id, source_type, source_id, generation, fee_amount_usd, percentage, tp_earned, created_at, ?, ?
			FROM tp_earnings`, label, now)
		if res.Error != nil {
			return fmt.Errorf("archive tp_earnings: %w", res.Error)
		}
		summary.TPEarningsArchived = res.RowsAffected

		res = tx.Exec(`INSERT INTO cashback_earnings_legacy
			(id, user_id, source_user_id, source_type, source_id, amount_usd, tx_hash, created_at, cutover_label, cutover_archived_at)
			SELECT id, user_id, source_user_id, source_type, source_id, amount_usd, tx_hash, created_at, ?, ?
			FROM cashback_earnings`, label, now)
		if res.Error != nil {
			return fmt.Errorf("archive cashback_earnings: %w", res.Error)
		}
		summary.CashbackArchived = res.RowsAffected

		// 4. Snapshot per-user balances before zeroing.
		res = tx.Exec(`INSERT INTO user_balances_legacy
			(user_id, tp_balance, cashback_balance, cutover_label, cutover_archived_at)
			SELECT id, tp_balance, cashback_balance, ?, ?
			FROM users`, label, now)
		if res.Error != nil {
			return fmt.Errorf("snapshot balances: %w", res.Error)
		}
		summary.BalancesSnapshotted = res.RowsAffected

		// 5. Zero the live balances.
		res = tx.Exec(`UPDATE users SET tp_balance = 0, cashback_balance = 0`)
		if res.Error != nil {
			return fmt.Errorf("zero balances: %w", res.Error)
		}
		summary.UsersZeroed = res.RowsAffected

		// 6. Record the completed-batch sentinel LAST (inside the same tx) so the
		//    re-run guard is satisfied for this label regardless of how many rows
		//    were archived/zeroed. If anything above failed the tx rolled back and
		//    no sentinel exists, so a corrected re-run is allowed.
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
