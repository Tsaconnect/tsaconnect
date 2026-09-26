package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func idempotencyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.RequestIdempotency{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedIdempotencyRow(t *testing.T, db *gorm.DB, key string, createdAt time.Time) {
	t.Helper()
	row := models.RequestIdempotency{Key: key, UserID: uuid.New(), RequestHash: "h",
		ResponseStatus: 200, ResponseBody: []byte(`{"success":true}`), CreatedAt: createdAt}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// The cached responses hold whatever the endpoint answered — bank details
// included, for a sell intent — so rows past the replay window are deleted,
// and rows still inside it are kept for the retries they exist to serve.
func TestRequestIdempotencyPruner_DeletesOnlyRowsPastTheWindow(t *testing.T) {
	db := idempotencyDB(t)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	seedIdempotencyRow(t, db, "old", now.Add(-25*time.Hour))
	seedIdempotencyRow(t, db, "ancient", now.Add(-30*24*time.Hour))
	seedIdempotencyRow(t, db, "recent", now.Add(-time.Hour))
	seedIdempotencyRow(t, db, "just-inside", now.Add(-models.RequestIdempotencyRetention+time.Minute))

	p := NewRequestIdempotencyPruner(db, time.Hour)
	p.now = func() time.Time { return now }
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var keys []string
	if err := db.Model(&models.RequestIdempotency{}).Order("key").Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "just-inside" || keys[1] != "recent" {
		t.Fatalf("remaining keys = %v, want [just-inside recent]", keys)
	}
}

func TestRequestIdempotencyPruner_ReportsFailure(t *testing.T) {
	db := idempotencyDB(t)
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	if err := NewRequestIdempotencyPruner(db, time.Hour).Run(context.Background()); err == nil {
		t.Fatal("a failed delete must surface to the scheduler")
	}
}

func TestRequestIdempotencyPruner_ZeroIntervalFallsBackToHourly(t *testing.T) {
	p := NewRequestIdempotencyPruner(nil, 0)
	if p.Interval() != time.Hour || p.Name() != "request-idempotency-pruner" {
		t.Fatalf("name %q interval %v", p.Name(), p.Interval())
	}
}
