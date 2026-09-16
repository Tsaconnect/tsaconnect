package services

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// reconcileCursor pages a reconciler scan across ticks.
//
// A plain "ORDER BY updated_at LIMIT n" scan starves: rows the reconciler can
// only alert on never change, so they stay the oldest and fill every page,
// and a new stuck row behind them is never reached. The cursor resumes after
// the last row of the previous page and wraps to the start once a page comes
// back short, so every candidate is visited within ceil(rows/limit) ticks.
//
// In-memory and per process: a restart only restarts the walk, and two
// instances walking independently is harmless because every action the scans
// take is guarded on the state it read.
type reconcileCursor struct {
	mu  sync.Mutex
	pos map[string]cursorPos
}

type cursorPos struct {
	at time.Time
	id uuid.UUID
}

// page applies the resume point and a stable order to q. table qualifies the
// columns for joined queries ("" for none).
func (c *reconcileCursor) page(key, table string, q *gorm.DB, limit int) *gorm.DB {
	col := func(name string) string {
		if table == "" {
			return name
		}
		return table + "." + name
	}
	c.mu.Lock()
	p, ok := c.pos[key]
	c.mu.Unlock()
	if ok {
		q = q.Where(fmt.Sprintf("(%s > ? OR (%s = ? AND %s > ?))", col("updated_at"), col("updated_at"), col("id")), p.at, p.at, p.id)
	}
	return q.Order(col("updated_at") + " asc").Order(col("id") + " asc").Limit(limit)
}

// advance records where the page ended; a short page wraps the walk.
func (c *reconcileCursor) advance(key string, got, limit int, lastAt time.Time, lastID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pos == nil {
		c.pos = map[string]cursorPos{}
	}
	if got < limit {
		delete(c.pos, key)
		return
	}
	c.pos[key] = cursorPos{at: lastAt, id: lastID}
}

// alertLimiter rate-limits per-row ALERT lines: a row the reconciler can only
// alert on would otherwise log on every tick. Entries older than the window are
// evicted, so the map cannot grow with every row the service has ever seen.
type alertLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// alertLimiterMaxKeys bounds the map between sweeps.
const alertLimiterMaxKeys = 4096

var reconcileAlerts = &alertLimiter{last: map[string]time.Time{}}

const reconcileAlertEvery = time.Hour

// allow reports whether key may log now, and records the decision.
func (l *alertLimiter) allow(key string, every time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < every {
		return false
	}
	l.last[key] = now
	if len(l.last) > alertLimiterMaxKeys {
		for k, t := range l.last {
			if now.Sub(t) >= every {
				delete(l.last, k)
			}
		}
		// Still over the bound (every key is inside its window): drop the
		// oldest half rather than grow without limit. Re-alerting a dropped key
		// early is harmless; leaking memory is not.
		if len(l.last) > alertLimiterMaxKeys {
			cutoff := now.Add(-every / 2)
			for k, t := range l.last {
				if t.Before(cutoff) {
					delete(l.last, k)
				}
			}
		}
	}
	return true
}

func (l *alertLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.last)
}

// alertf logs format as an ALERT at most once per reconcileAlertEvery for key.
func alertf(key, format string, args ...any) {
	if reconcileAlerts.allow(key, reconcileAlertEvery) {
		log.Printf(format, args...)
	}
}

// claimDailyRun reports whether this process may run the periodic audit named
// name, and records the run. Persisted (models.JobRunMarker) so a restart does
// not repeat it and two instances cannot both run it.
func claimDailyRun(ctx context.Context, db *gorm.DB, name string, every time.Duration) bool {
	if db == nil {
		return false
	}
	now := time.Now()
	res := db.WithContext(ctx).Model(&models.JobRunMarker{}).
		Where("name = ? AND last_run_at <= ?", name, now.Add(-every)).
		Update("last_run_at", now)
	if res.Error == nil && res.RowsAffected == 1 {
		return true
	}
	// No row yet: the first run creates it. A duplicate-key error means another
	// instance created it first, which is a "someone else ran it" answer.
	if err := db.WithContext(ctx).Create(&models.JobRunMarker{Name: name, LastRunAt: now}).Error; err != nil {
		return false
	}
	return true
}

// olderThanExpr builds a dialect-appropriate "column is older than d" test so
// age comparisons on money guards are evaluated by the database rather than
// against the calling process's clock.
func olderThanExpr(db *gorm.DB, column string, d time.Duration) (string, []any) {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "sqlite" {
		// SQLite has no interval type; compare Julian days.
		return fmt.Sprintf("julianday('now') - julianday(%s) > ?", column), []any{d.Hours() / 24}
	}
	return fmt.Sprintf("%s < NOW() - (? * interval '1 second')", column), []any{int64(d.Seconds())}
}
