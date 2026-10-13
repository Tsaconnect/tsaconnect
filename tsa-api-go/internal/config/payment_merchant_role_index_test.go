package config

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

const dropOneRoleIndexSQL = "../../deploy/one-off/2026-10-13-payment-merchants-drop-one-role-index.sql"

// roleIndexDB is production's shape before this release: the table and the
// one-row-per-user index.
func roleIndexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.TempPostgres(t, "pm_role_idx_", &models.PaymentMerchant{})
	if err := db.Exec(`CREATE UNIQUE INDEX uq_payment_merchants_user ON payment_merchants (user_id)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func insertRole(db *gorm.DB, userID uuid.UUID, kind string) error {
	return db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: userID, Name: "Ada", Kind: kind,
		Status: models.PaymentMerchantStatusApproved}).Error
}

func dropOneRoleIndex(t *testing.T, db *gorm.DB) error {
	t.Helper()
	sql, err := os.ReadFile(dropOneRoleIndexSQL)
	if err != nil {
		t.Fatal(err)
	}
	// No arguments: pgx runs it on the simple protocol, which takes several statements.
	return db.Exec(string(sql)).Error
}

// oldIndexCount is scoped to this test's schema: the dev database's public
// schema, and parallel tests' temp schemas, hold an index of the same name.
func oldIndexCount(db *gorm.DB) int64 {
	var n int64
	db.Raw(`SELECT count(*) FROM pg_indexes WHERE indexname = 'uq_payment_merchants_user' AND schemaname = current_schema()`).Scan(&n)
	return n
}

func TestPaymentMerchantRoleIndex_OldIndexStillBlocksSecondRow_OnPostgres(t *testing.T) {
	db := roleIndexDB(t)
	if err := MigratePaymentMerchantRoleIndex(db); err != nil {
		t.Fatal(err)
	}
	if err := MigratePaymentMerchantRoleIndex(db); err != nil { // idempotent on every boot
		t.Fatalf("second run: %v", err)
	}
	u := uuid.New()
	if err := insertRole(db, u, models.PaymentMerchantKindMerchant); err != nil {
		t.Fatal(err)
	}
	if err := insertRole(db, u, models.PaymentMerchantKindLP); err == nil {
		t.Fatal("before the one-off the old index must still refuse a second row")
	}
}

func TestPaymentMerchantRoleIndex_OneRowPerRoleAfterOneOff_OnPostgres(t *testing.T) {
	db := roleIndexDB(t)
	if err := MigratePaymentMerchantRoleIndex(db); err != nil {
		t.Fatal(err)
	}
	if err := dropOneRoleIndex(t, db); err != nil {
		t.Fatalf("one-off: %v", err)
	}
	if oldIndexCount(db) != 0 {
		t.Fatal("old index still present after the one-off")
	}
	u := uuid.New()
	if err := insertRole(db, u, models.PaymentMerchantKindMerchant); err != nil {
		t.Fatal(err)
	}
	if err := insertRole(db, u, models.PaymentMerchantKindLP); err != nil {
		t.Fatalf("merchant + LP for one user: %v", err)
	}
	if err := insertRole(db, u, models.PaymentMerchantKindLP); err == nil {
		t.Fatal("a second LP row for the same user was accepted")
	}
	if !uniqueIndexReady(db, "payment_merchants", PaymentMerchantRoleIndexName) {
		t.Fatal("role index missing or invalid")
	}
}

func TestPaymentMerchantRoleIndex_OneOffRefusesWithoutTheRightNewIndex_OnPostgres(t *testing.T) {
	db := roleIndexDB(t) // migration never ran
	if err := dropOneRoleIndex(t, db); err == nil {
		t.Fatal("the one-off dropped the only uniqueness backstop")
	}
	if oldIndexCount(db) != 1 {
		t.Fatal("old index gone after a refused one-off")
	}
	// The right name on the wrong columns is refused too.
	if err := db.Exec(`CREATE UNIQUE INDEX ` + PaymentMerchantRoleIndexName + ` ON payment_merchants (user_id, name)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := dropOneRoleIndex(t, db); err == nil || oldIndexCount(db) != 1 {
		t.Fatalf("one-off accepted an index on the wrong columns: %v", err)
	}
}
