// Package testutil holds helpers shared by tests in several packages.
package testutil

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TempPostgres opens a THROWAWAY schema inside the database DATABASE_URL points
// at and migrates dst into it, or skips the test when there is no database.
//
// SQLite, which the rest of the suite runs on, hides whole classes of
// PostgreSQL behaviour (numeric casts, row locks, concurrent transactions), so
// the few tests that need the real thing come here. The temporary schema is
// the point: AutoMigrate is DDL and must not touch the developer's real
// tables. Everything is dropped with the schema.
func TempPostgres(t testing.TB, prefix string, dst ...any) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Fall back to the service's own .env (tests run from internal/<pkg>)
		// so a local run picks it up the way the server does. The value is
		// never read or logged here, only handed to the driver.
		_ = godotenv.Load(filepath.Join("..", "..", ".env"))
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping the PostgreSQL-backed test")
	}

	schema := prefix + uuid.New().String()[:8]
	scoped, err := DSNWithSchema(dsn, schema)
	if err != nil {
		t.Fatalf("scope dsn to a temp schema: %v", err)
	}

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Skipf("cannot reach PostgreSQL: %v", err)
	}
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		if sqlDB, dErr := admin.DB(); dErr == nil {
			_ = sqlDB.Close()
		}
	})

	db, err := gorm.Open(postgres.Open(scoped), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open temp schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dErr := db.DB(); dErr == nil {
			_ = sqlDB.Close()
		}
	})
	if len(dst) > 0 {
		if err := db.AutoMigrate(dst...); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	return db
}

// DSNWithSchema pins a connection to one schema via libpq's search_path option,
// handling both the URL and the key=value DSN forms.
func DSNWithSchema(dsn, schema string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		// key=value form ("host=... dbname=..."); options append positionally.
		return dsn + " search_path=" + schema, nil
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
