package services

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// withAdvisoryLock runs fn in one transaction holding a transaction-scoped
// Postgres advisory lock on key (spec 2026-10-08 §9.2, extracted from the
// company wallet's withStockLock). SQLite (tests) is single-writer. label is
// the error prefix the caller has always used.
func withAdvisoryLock(ctx context.Context, db *gorm.DB, key, label string, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
		return fn(tx)
	})
}

// poolLockKey is the pool's lock for one (chain, token) (§9.2). Every pool
// check-then-write takes it: open, re-pick, live re-price at claim, and C2's
// re-deliver.
func poolLockKey(chainKey, token string) string {
	return "pool:" + strings.ToLower(strings.TrimSpace(chainKey)) + ":" + strings.ToUpper(strings.TrimSpace(token))
}
