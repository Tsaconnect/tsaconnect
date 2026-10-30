package services

import (
	"context"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// How long a booking request stays live before the sweep closes it.
//
// The two windows differ because they protect different things. An unanswered
// request is only clutter: a week is generous and costs nothing. An APPROVED
// request may carry a provider-named price (PricingMethod on_request), and the
// buyer pays whatever that says whenever they get round to it — so the quote is
// the thing with a shelf life, and two days is about as long as a service price
// can be trusted without the provider re-confirming it.
// approvalClock is when an approval was given: approved_at, or updated_at on
// rows approved before approved_at existed.
const approvalClock = "COALESCE(approved_at, updated_at)"

const (
	BookingRequestTTL         = 7 * 24 * time.Hour
	BookingRequestApprovalTTL = 48 * time.Hour
)

// BookingRequestService owns the off-chain booking-request lifecycle work that
// no user action drives.
type BookingRequestService struct {
	db *gorm.DB
}

func NewBookingRequestService(db *gorm.DB) *BookingRequestService {
	return &BookingRequestService{db: db}
}

// ExpireStaleBookingRequests closes requests nobody acted on, returning how many
// rows it changed.
//
// Two guarded UPDATEs rather than a read-then-write: the status predicate is
// what makes this idempotent and safe against a concurrent approve or pay, so a
// repeated or overlapping run cannot resurrect a terminal row. Only `requested`
// and `approved` are ever touched — every other status is already terminal.
func (s *BookingRequestService) ExpireStaleBookingRequests(ctx context.Context) (int64, error) {
	now := time.Now()
	var total int64

	sweeps := []struct {
		from   string
		since  string // when the status's clock started
		cutoff time.Time
	}{
		{models.BookingRequestRequested, "updated_at", now.Add(-BookingRequestTTL)},
		{models.BookingRequestApproved, approvalClock, now.Add(-BookingRequestApprovalTTL)},
	}

	for _, sweep := range sweeps {
		res := s.db.WithContext(ctx).
			Model(&models.BookingRequest{}).
			Where("status = ? AND "+sweep.since+" < ?", sweep.from, sweep.cutoff).
			Updates(map[string]interface{}{
				"status":     models.BookingRequestExpired,
				"updated_at": now,
			})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
	}
	return total, nil
}
