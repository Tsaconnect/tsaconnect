package services

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
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
// alert on would otherwise log on every tick.
type alertLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

var reconcileAlerts = &alertLimiter{last: map[string]time.Time{}}

const reconcileAlertEvery = time.Hour

// alertf logs format as an ALERT at most once per reconcileAlertEvery for key.
func alertf(key, format string, args ...any) {
	reconcileAlerts.mu.Lock()
	if t, ok := reconcileAlerts.last[key]; ok && time.Since(t) < reconcileAlertEvery {
		reconcileAlerts.mu.Unlock()
		return
	}
	reconcileAlerts.last[key] = time.Now()
	reconcileAlerts.mu.Unlock()
	log.Printf(format, args...)
}
