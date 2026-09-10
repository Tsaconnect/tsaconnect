package jobs

import (
	"context"
	"time"
)

// bookingRequestExpirer is the slice of BookingRequestService this worker needs.
type bookingRequestExpirer interface {
	ExpireStaleBookingRequests(ctx context.Context) (int64, error)
}

// BookingRequestExpiry closes booking requests that nobody acted on.
//
// The lifecycle declared `expired` as a valid transition from both `requested`
// and `approved`, but nothing ever set it. Two things leaked as a result: a
// request the provider ignored sat in the buyer's list forever, and — the one
// that costs money — an approved request kept its provider-named price
// indefinitely, so a buyer could come back weeks later and pay a quote the
// provider would no longer honour.
//
// Hourly: this is a tidy-up, not a deadline, and a row closed up to an hour
// late changes nothing. ExpireStaleBookingRequests is a pair of guarded UPDATEs
// and idempotent, so a missed tick is recovered by the next one.
type BookingRequestExpiry struct {
	svc      bookingRequestExpirer
	interval time.Duration
}

func NewBookingRequestExpiry(svc bookingRequestExpirer, interval time.Duration) *BookingRequestExpiry {
	if interval <= 0 {
		interval = time.Hour
	}
	return &BookingRequestExpiry{svc: svc, interval: interval}
}

func (w *BookingRequestExpiry) Name() string            { return "booking-request-expiry" }
func (w *BookingRequestExpiry) Interval() time.Duration { return w.interval }

func (w *BookingRequestExpiry) Run(ctx context.Context) error {
	_, err := w.svc.ExpireStaleBookingRequests(ctx)
	return err
}
