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

func refreshTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Raw DDL: the model's uuid default is PostgreSQL's gen_random_uuid().
	if err := db.Exec(`CREATE TABLE refresh_tokens (
		id TEXT PRIMARY KEY, user_id TEXT, token_hash TEXT UNIQUE, expires_at DATETIME,
		revoked INTEGER DEFAULT 0, device_info TEXT, web INTEGER NOT NULL DEFAULT 0, created_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	return db
}

func TestRefreshTokenPruner_DeletesOnlyDeadTokensPastTheGrace(t *testing.T) {
	db := refreshTokenDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-RefreshTokenGrace - time.Hour)
	recent := now.Add(-RefreshTokenGrace + time.Hour)
	seed := map[string]models.RefreshToken{
		"old-revoked":    {Revoked: true, ExpiresAt: now.Add(time.Hour), CreatedAt: old},
		"old-expired":    {ExpiresAt: now.Add(-time.Hour), CreatedAt: old},
		"old-live":       {ExpiresAt: now.Add(24 * time.Hour), CreatedAt: old},
		"recent-revoked": {Revoked: true, ExpiresAt: now.Add(time.Hour), CreatedAt: recent},
		"recent-expired": {ExpiresAt: now.Add(-time.Minute), CreatedAt: recent},
		"recent-live":    {ExpiresAt: now.Add(24 * time.Hour), CreatedAt: recent},
	}
	for hash, rt := range seed {
		rt.ID, rt.UserID, rt.TokenHash = uuid.New(), uuid.New(), hash
		if err := db.Create(&rt).Error; err != nil {
			t.Fatalf("seed %s: %v", hash, err)
		}
	}

	p := NewRefreshTokenPruner(db, 0)
	p.now = func() time.Time { return now }
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var left []string
	if err := db.Model(&models.RefreshToken{}).Order("token_hash").Pluck("token_hash", &left).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{"old-live", "recent-expired", "recent-live", "recent-revoked"}
	if len(left) != len(want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	for i := range want {
		if left[i] != want[i] {
			t.Fatalf("left %v, want %v", left, want)
		}
	}
}

func TestRefreshTokenPruner_ReportsFailure(t *testing.T) {
	db := refreshTokenDB(t)
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	if err := NewRefreshTokenPruner(db, 0).Run(context.Background()); err == nil {
		t.Fatal("a failed delete must surface to the scheduler")
	}
}

func TestRefreshTokenPruner_RunsDaily(t *testing.T) {
	p := NewRefreshTokenPruner(nil, 0)
	if p.Interval() != 24*time.Hour || p.Name() != "refresh-token-pruner" {
		t.Fatalf("name %q interval %v", p.Name(), p.Interval())
	}
}
