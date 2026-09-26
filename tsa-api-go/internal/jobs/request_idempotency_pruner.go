package jobs

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// RequestIdempotencyPruner deletes cached idempotent responses once they are
// past models.RequestIdempotencyRetention.
//
// The table backs every Idempotency-Key endpoint, and each row is the full
// response the endpoint gave — for a sell intent that includes her bank
// details. Without this the table kept every one of them forever.
//
// Hourly and not alert-wired: this is a tidy-up, not a money path. A missed
// tick only means rows live an hour longer, and a single DELETE ... WHERE is
// idempotent, so the next tick catches up.
type RequestIdempotencyPruner struct {
	db       *gorm.DB
	interval time.Duration
	now      func() time.Time
}

func NewRequestIdempotencyPruner(db *gorm.DB, interval time.Duration) *RequestIdempotencyPruner {
	if interval <= 0 {
		interval = time.Hour
	}
	return &RequestIdempotencyPruner{db: db, interval: interval, now: time.Now}
}

func (w *RequestIdempotencyPruner) Name() string            { return "request-idempotency-pruner" }
func (w *RequestIdempotencyPruner) Interval() time.Duration { return w.interval }

func (w *RequestIdempotencyPruner) Run(ctx context.Context) error {
	cutoff := w.now().Add(-models.RequestIdempotencyRetention)
	res := w.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&models.RequestIdempotency{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[%s] deleted %d cached response(s) older than %s", w.Name(), res.RowsAffected, models.RequestIdempotencyRetention)
	}
	return nil
}
