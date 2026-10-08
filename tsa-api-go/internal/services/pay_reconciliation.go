package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
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

// PaySummarySucceeded is what was paid in a period: the payments that
// succeeded in it (by succeeded_at), what their customers paid and what the
// merchant received, in kobo.
type PaySummarySucceeded struct {
	Count             int64 `json:"count"`
	CustomerTotalKobo int64 `json:"customerTotal"`
	MerchantNetKobo   int64 `json:"merchantNet"`
}

// PaySummaryRefunds is the refunds confirmed in a period (by confirmed_at). On
// the wire the amount is a decimal USD string ("12.50").
type PaySummaryRefunds struct {
	Count          int64
	AmountMicroUSD int64
}

func (r PaySummaryRefunds) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Count  int64  `json:"count"`
		Amount string `json:"amount"`
	}{r.Count, FormatMicroUSD(r.AmountMicroUSD)})
}

// PaySummary is a period's totals for one merchant and mode. ByStatus counts
// the payments made in the period (by created_at), keyed as the list filter
// names them: a payment its merchant called off is "cancelled", not "expired".
// Untried open-link payments are left out of it, as they are from the list.
type PaySummary struct {
	Succeeded PaySummarySucceeded `json:"succeeded"`
	ByStatus  map[string]int64    `json:"byStatus"`
	Refunds   PaySummaryRefunds   `json:"refunds"`
}

// PayRefundAgg is what has been refunded on one payment. Status is
// none | partial | full.
type PayRefundAgg struct {
	RefundedMicroUSD int64
	CapMicroUSD      int64
	Status           string
}

// PayRefundAggregator is the refunds read reconciliation needs. The refund
// service implements it; unset (nil), every payment reads as never refunded.
type PayRefundAggregator interface {
	AggregateByPayment(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]PayRefundAgg, error)
	TotalsConfirmed(ctx context.Context, merchantID uuid.UUID, mode string, start, end time.Time) (PaySummaryRefunds, error)
}

// SetRefundAggregator wires the refund totals into Summary and the export's
// refund columns. Never called with a typed-nil.
func (s *PayPaymentService) SetRefundAggregator(a PayRefundAggregator) { s.refundAgg = a }

// payStatusKeySQL is a payment's status as the list filter names it: its
// stored status, except "cancelled" for one its merchant called off.
const payStatusKeySQL = "CASE WHEN status = 'expired' AND failure_reason = ? THEN 'cancelled' ELSE status END"

// Summary is merchantID's totals in mode for [start, end). Zero bounds mean
// all time. Grouped queries only, over the (merchant_id, mode, succeeded_at)
// and (merchant_id, mode, created_at) indexes.
func (s *PayPaymentService) Summary(ctx context.Context, merchantID uuid.UUID, mode string, start, end time.Time) (PaySummary, error) {
	out := PaySummary{ByStatus: map[string]int64{}}
	scope := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&models.PayPayment{}).Where("merchant_id = ? AND mode = ?", merchantID, mode)
	}

	paid := payBetween(scope().Where("status = ?", models.PayStatusSucceeded), "succeeded_at", start, end)
	// SUM of a bigint is numeric on Postgres: cast back so it scans into int64.
	if err := paid.Select("COUNT(*) AS count, " +
		"CAST(COALESCE(SUM(customer_total_kobo), 0) AS BIGINT) AS customer_total_kobo, " +
		"CAST(COALESCE(SUM(merchant_net_kobo), 0) AS BIGINT) AS merchant_net_kobo").
		Scan(&out.Succeeded).Error; err != nil {
		return PaySummary{}, err
	}

	var rows []struct {
		StatusKey string
		N         int64
	}
	made := payHideUntriedOpen(payBetween(scope(), "created_at", start, end))
	if err := made.Select(payStatusKeySQL+" AS status_key, COUNT(*) AS n", PayReasonMerchantCancelled).
		Group("status_key").Scan(&rows).Error; err != nil {
		return PaySummary{}, err
	}
	for _, r := range rows {
		out.ByStatus[r.StatusKey] = r.N
	}

	if s.refundAgg != nil {
		refunds, err := s.refundAgg.TotalsConfirmed(ctx, merchantID, mode, start, end)
		if err != nil {
			return PaySummary{}, err
		}
		out.Refunds = refunds
	}
	return out, nil
}
