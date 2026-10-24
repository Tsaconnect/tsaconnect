package services

import (
	"testing"

	"gorm.io/gorm"
)

// createPayoutTable creates the merchant payout account table as raw SQLite DDL
// (the model's PostgreSQL defaults do not migrate), for tests whose code reads
// the merchant's payout account.
func createPayoutTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS seller_payout_methods (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, currency TEXT NOT NULL,
		institution_code TEXT NOT NULL, institution_name TEXT NOT NULL, account_number TEXT NOT NULL,
		account_name TEXT NOT NULL, verified BOOLEAN NOT NULL DEFAULT false, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatalf("create seller_payout_methods: %v", err)
	}
}
