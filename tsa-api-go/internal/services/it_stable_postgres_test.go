package services

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Every other test in this package runs on SQLite, and SQLite's type affinity
// happily accepts an empty string into a numeric column. PostgreSQL does not:
// `SELECT ''::numeric` is an error (SQLSTATE 22P02), and GORM puts every field
// without a column `default:` tag into the INSERT whether or not it was set.
// That combination made a whole class of defect structurally invisible to this
// suite — CreateStableSellIntent could not insert a single row in production
// while the suite stayed green.
//
// This file is the counterweight: it exercises the real write path against a
// real PostgreSQL. It skips cleanly when DATABASE_URL is absent so CI without a
// database still passes.

// newStablePostgresService builds an ITService on a THROWAWAY schema inside the
// database DATABASE_URL points at, or skips the test when there is none.
//
// The temporary schema is the whole point: AutoMigrate is a DDL operation, and
// it must not touch the developer's real `it_intents` table. Everything the
// test creates is dropped with the schema.
func newStablePostgresService(t *testing.T) *ITService {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Fall back to the service's own .env so a local run picks it up the
		// same way the server does. Values are never read or logged here —
		// only handed to the driver.
		_ = godotenv.Load(filepath.Join("..", "..", ".env"))
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping the PostgreSQL-backed test")
	}

	schema := "it_stable_test_" + uuid.New().String()[:8]
	scoped, err := dsnWithSchema(dsn, schema)
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
	if err := db.AutoMigrate(&models.ITIntent{}); err != nil {
		t.Fatalf("migrate it_intents: %v", err)
	}

	s := NewITService(db, nil, stubFx{ngn: 1500})
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	return s
}

// dsnWithSchema pins a connection to one schema via libpq's search_path option,
// handling both the URL and the key=value DSN forms.
func dsnWithSchema(dsn, schema string) (string, error) {
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

func TestCreateStableSellIntent_InsertsOnPostgres(t *testing.T) {
	// The regression that shipped: six numeric columns went out as '' and
	// PostgreSQL rejected the whole INSERT with SQLSTATE 22P02, so no user
	// could create a sell at all. Nothing about this is visible on SQLite.
	s := newStablePostgresService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)

	q := testSellQuote()
	q.FeeMinor = big.NewInt(255789) // the fee the quote genuinely deducted

	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), q, "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent on PostgreSQL: %v", err)
	}

	got, err := s.GetIntent(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The fee must be recorded as the fee actually charged. Writing "0" here
	// would balance the row arithmetically and make the ledger understate what
	// the user paid.
	if got.FeeNgnKobo != "255789" {
		t.Fatalf("feeNgnKobo = %q, want the quote's fee 255789", got.FeeNgnKobo)
	}
	if got.FeeMinor != "255789" {
		t.Fatalf("feeMinor = %q — BeforeSave's mirror left the minor twin wrong", got.FeeMinor)
	}
	if got.McgpAmount != "0" {
		t.Fatalf("mcgpAmount = %q, want 0 (a stablecoin sell has no MCGP leg)", got.McgpAmount)
	}
	if got.NetNgnKobo != q.NetMinor.String() || got.NetMinor != q.NetMinor.String() {
		t.Fatalf("net = %q/%q, want %s", got.NetNgnKobo, got.NetMinor, q.NetMinor)
	}
	if got.AssetAmount != q.AssetAmount.String() {
		t.Fatalf("assetAmount = %q, want %s", got.AssetAmount, q.AssetAmount)
	}
}

func TestCreateStableSellIntent_LeavesNoNumericColumnEmptyOnPostgres(t *testing.T) {
	// The mechanical version of the test above: enumerate every numeric column
	// on it_intents and assert the row carries a value in each. Doing it from
	// information_schema rather than a hand-written list means a numeric column
	// added later is covered the day it lands, which is what stops this class
	// of defect from recurring.
	s := newStablePostgresService(t)
	s.SetStableDepositAddress("POLYGON", testFWAddr)

	intent, _, err := s.CreateStableSellIntent(context.Background(), uuid.New(), testSellQuote(), "POLYGON", testSellBank())
	if err != nil {
		t.Fatalf("CreateStableSellIntent on PostgreSQL: %v", err)
	}

	var columns []string
	if err := s.db.Raw(`
	    SELECT column_name FROM information_schema.columns
	    WHERE table_name = 'it_intents' AND data_type = 'numeric'
	      AND table_schema = current_schema()
	    ORDER BY column_name`).Scan(&columns).Error; err != nil {
		t.Fatalf("read numeric columns: %v", err)
	}
	if len(columns) == 0 {
		t.Fatal("no numeric columns found — the query is not testing anything")
	}

	for _, col := range columns {
		var isNull bool
		if err := s.db.Raw(
			fmt.Sprintf("SELECT %s IS NULL FROM it_intents WHERE id = ?", col),
			intent.ID,
		).Scan(&isNull).Error; err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if isNull {
			t.Errorf("numeric column %q is NULL — a reader parsing it as a number gets nothing", col)
		}
	}
}
