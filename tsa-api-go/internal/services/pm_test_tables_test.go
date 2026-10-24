package services

import (
	"testing"

	"gorm.io/gorm"
)

// createPaymentMerchantTables creates the users, payment_merchants and
// merchant_requests tables as raw SQLite DDL (the models' PostgreSQL defaults
// do not migrate), for tests that read or write payment-merchant rows.
func createPaymentMerchantTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, verification_status TEXT, verified_legal_name TEXT)`, // approval locks the user row
		`CREATE TABLE IF NOT EXISTS payment_merchants (
			id                        TEXT PRIMARY KEY,
			user_id                   TEXT NOT NULL,
			name                      TEXT NOT NULL DEFAULT '',
			status                    TEXT NOT NULL DEFAULT 'pending',
			kind TEXT NOT NULL DEFAULT 'merchant', pool_chain_id INTEGER,
			nomba_account_id          TEXT,
			nomba_wallet_id           TEXT,
			nomba_creds_ref           TEXT,
			inbound_account_number    TEXT,
			inbound_bank              TEXT,
			nomba_account_ref         TEXT,
			operator_wallet_address   TEXT,
			nomba_sub_account_id      TEXT,
			settlement_bank_code      TEXT,
			settlement_account_number TEXT,
			settlement_account_name   TEXT,
			available_ngn             TEXT NOT NULL DEFAULT '0',
			reserved_ngn              TEXT NOT NULL DEFAULT '0',
			provisioning_status       TEXT NOT NULL DEFAULT 'pending',
			per_trade_cap_usd         REAL NOT NULL DEFAULT 0,
			daily_cap_usd             REAL NOT NULL DEFAULT 0,
			reviewed_by               TEXT,
			reviewed_at               DATETIME,
			review_note               TEXT,
			created_at                DATETIME,
			updated_at                DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS merchant_requests (
			id                          TEXT PRIMARY KEY,
			user_id                     TEXT NOT NULL,
			business_type               TEXT NOT NULL,
			business_name               TEXT NOT NULL DEFAULT '',
			business_description        TEXT,
			address                     TEXT NOT NULL DEFAULT '',
			city                        TEXT NOT NULL DEFAULT '',
			state                       TEXT NOT NULL DEFAULT '',
			country                     TEXT NOT NULL DEFAULT '',
			phone                       TEXT NOT NULL DEFAULT '',
			registration_number         TEXT,
			status                      TEXT NOT NULL DEFAULT 'pending',
			admin_note                  TEXT,
			reviewed_by                 TEXT,
			reviewed_at                 DATETIME,
			settlement_bank_code        TEXT,
			settlement_account_number   TEXT,
			maker_wallet_address        TEXT,
			requested_per_trade_cap_usd REAL DEFAULT 0,
			requested_daily_cap_usd     REAL DEFAULT 0,
			created_at                  DATETIME,
			updated_at                  DATETIME
		)`,
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
}
