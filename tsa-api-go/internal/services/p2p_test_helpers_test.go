package services

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupP2PTestDB opens an in-memory SQLite DB with the P2P reputation tables
// pre-created using raw DDL (avoiding PostgreSQL-specific defaults that would
// break SQLite AutoMigrate). Consistent with how setupTradeDB works.
func setupP2PTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE p2_p_merchant_stats (
			user_id TEXT PRIMARY KEY,
			released_count INTEGER NOT NULL DEFAULT 0,
			cancelled_count INTEGER NOT NULL DEFAULT 0,
			avg_release_secs_30d INTEGER,
			avg_pay_secs_30d INTEGER,
			thumbs_up INTEGER NOT NULL DEFAULT 0,
			thumbs_down INTEGER NOT NULL DEFAULT 0,
			last_seen_at DATETIME,
			verified INTEGER NOT NULL DEFAULT 0,
			trusted INTEGER NOT NULL DEFAULT 0,
			trusted_manual INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME
		)`,
		`CREATE TABLE p2_p_ratings (
			id TEXT PRIMARY KEY,
			trade_id TEXT NOT NULL,
			rater_id TEXT NOT NULL,
			ratee_id TEXT NOT NULL,
			value INTEGER NOT NULL,
			created_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v\nSQL=%s", err, s)
		}
	}
	return db
}
