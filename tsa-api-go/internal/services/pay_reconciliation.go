package services

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Reconciliation: what a merchant owner reads to match TSA Pay against their
// books. That is payments by the day they were made, a period's totals by the
// day money was paid, and a CSV of everything paid in a range.

// payWAT is Nigeria's time, UTC+1 all year (no DST). It is a fixed zone, never
// LoadLocation: the host has no guaranteed tzdata, and a missing zoneinfo must
// not quietly shift every date by an hour.
var payWAT = time.FixedZone("WAT", 3600)

// ErrPayDateRange means from/to are not both YYYY-MM-DD with to on or after
// from. For an export it also means the range is missing or longer than
// payExportMaxDays.
var ErrPayDateRange = errors.New("dates must be YYYY-MM-DD, with both from and to, and to on or after from")

const payDateLayout = "2006-01-02"

// ParsePayDateRange reads an owner's from/to (days in WAT, to inclusive) as
// [start, endExclusive). Both empty means no range: zero times and no error.
func ParsePayDateRange(from, to string) (start, endExclusive time.Time, err error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" && to == "" {
		return time.Time{}, time.Time{}, nil
	}
	start, errFrom := time.ParseInLocation(payDateLayout, from, payWAT)
	last, errTo := time.ParseInLocation(payDateLayout, to, payWAT)
	if errFrom != nil || errTo != nil || last.Before(start) {
		return time.Time{}, time.Time{}, ErrPayDateRange
	}
	return start, last.AddDate(0, 0, 1), nil
}

// payBetween narrows q to col in [start, end); a zero bound is open. Bounds are
// bound in UTC: Postgres compares instants either way, and SQLite (the tests)
// compares the stored text, which the tests write in UTC.
func payBetween(q *gorm.DB, col string, start, end time.Time) *gorm.DB {
	if !start.IsZero() {
		q = q.Where(col+" >= ?", start.UTC())
	}
	if !end.IsZero() {
		q = q.Where(col+" < ?", end.UTC())
	}
	return q
}
