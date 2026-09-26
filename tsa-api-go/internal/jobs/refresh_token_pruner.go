package jobs

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// RefreshTokenGrace is how long a revoked or expired refresh token is kept
// before it is deleted, so a recent sign-out or rotation stays visible.
const RefreshTokenGrace = 7 * 24 * time.Hour

// RefreshTokenPruner deletes refresh tokens that can never be used again —
// revoked or expired — once they are older than RefreshTokenGrace.
//
// Every rotation revokes one row and adds another, and a web session rotates
// hourly, so without this the table grows by a row per hour per signed-in
// browser, forever. Live tokens are never touched, however old.
type RefreshTokenPruner struct {
	db       *gorm.DB
	interval time.Duration
	now      func() time.Time
}

func NewRefreshTokenPruner(db *gorm.DB, interval time.Duration) *RefreshTokenPruner {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &RefreshTokenPruner{db: db, interval: interval, now: time.Now}
}

func (w *RefreshTokenPruner) Name() string            { return "refresh-token-pruner" }
func (w *RefreshTokenPruner) Interval() time.Duration { return w.interval }

func (w *RefreshTokenPruner) Run(ctx context.Context) error {
	now := w.now()
	res := w.db.WithContext(ctx).
		Where("(revoked = ? OR expires_at < ?) AND created_at < ?", true, now, now.Add(-RefreshTokenGrace)).
		Delete(&models.RefreshToken{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[%s] deleted %d revoked or expired refresh token(s) older than %s", w.Name(), res.RowsAffected, RefreshTokenGrace)
	}
	return nil
}
