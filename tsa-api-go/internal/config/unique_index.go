package config

import "gorm.io/gorm"

// uniqueIndexReady: the index exists and — on Postgres, where a failed
// CONCURRENTLY build leaves an INVALID index behind — is usable. Scoped to
// current_schema(), so another schema's index of the same name never counts.
func uniqueIndexReady(db *gorm.DB, table, name string) bool {
	if isPostgres(db) {
		var valid bool
		err := db.Raw(`SELECT COALESCE(bool_and(i.indisvalid), false)
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = ? AND n.nspname = current_schema()`, name).Scan(&valid).Error
		return err == nil && valid
	}
	return db.Migrator().HasIndex(table, name)
}

// buildUniqueIndex creates `CREATE UNIQUE INDEX <name><on>`. On Postgres it is
// built CONCURRENTLY (writes are not blocked) after dropping an INVALID
// leftover, which would satisfy IF NOT EXISTS and never enforce anything.
// CONCURRENTLY cannot run inside a transaction — AutoMigrate's Exec calls are
// not in one.
func buildUniqueIndex(db *gorm.DB, name, on string) error {
	if !isPostgres(db) {
		return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ` + name + on).Error
	}
	if err := db.Exec(`DROP INDEX CONCURRENTLY IF EXISTS ` + name).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY ` + name + on).Error
}
