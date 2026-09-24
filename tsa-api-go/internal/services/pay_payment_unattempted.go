package services

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ReleaseStuckTestPayments releases test-mode payments left in processing
// with no attempt linked for longer than olderThan, oldest first, up to
// limit. Live payments are never touched here: releasing one can strand a
// customer's money mid-flight, so reconcileProcessing owns their release
// rules.
func (s *PayPaymentService) ReleaseStuckTestPayments(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	return s.releaseUnattempted(ctx, models.PayModeTest, olderThan, limit, "released: test payment was stuck processing")
}

// releaseUnattempted releases payments of mode left in processing with NO
// attempt linked, locked for longer than olderThan, oldest first, up to limit.
// Each release goes through the guarded Release, so a payment that moved on
// since the select is skipped.
//
// "No attempt linked" is not "no attempt": a start that lost its bookkeeping
// leaves an order or intent she can fund with nothing pointing at it. For a
// live payment (test mode calls no provider) the rule therefore looks for one
// first (adoptStrayAttempt) and never releases a payment it finds one for.
func (s *PayPaymentService) releaseUnattempted(ctx context.Context, mode string, olderThan time.Duration, limit int, reason string) (int, error) {
	var stale []models.PayPayment
	if err := s.db.WithContext(ctx).
		Where("mode = ? AND status = ? AND paycrest_order_id IS NULL AND sell_intent_id IS NULL AND locked_at < ?",
			mode, models.PayStatusProcessing, s.now().Add(-olderThan)).
		Order("locked_at").
		Limit(limit).
		Find(&stale).Error; err != nil {
		return 0, err
	}
	n := 0
	for i := range stale {
		p := &stale[i]
		if mode == models.PayModeLive {
			adopted, err := s.adoptStrayAttempt(ctx, p)
			if err != nil {
				return n, err
			}
			if adopted {
				continue
			}
		}
		// releaseFrom guards on the reading (no order, no intent): an attempt
		// linked since the select above is never released.
		err := s.releaseFrom(ctx, p, reason)
		switch {
		case err == nil:
			n++
		case errors.Is(err, ErrPayTransition):
			// resolved or released by someone else since the select
		default:
			return n, err
		}
	}
	return n, nil
}

// adoptStrayAttempt looks for what a start opened for p but never linked, and
// links it, so the ordinary attempt rules decide what becomes of it — none of
// which frees the payment while the attempt could still pay the merchant. It
// reports true when it found one (linked now, or linked by someone since):
// the caller must not release. Two things can be found:
//
//   - an intent whose pay_payment_id is p, in any state but failed — the key is
//     exact, and an ended (failed) intent moves nothing;
//   - her sell order to THIS merchant's snapshotted bank for the merchant's
//     net, opened since she locked, unless it is a short order that was
//     cancelled with its deposit address withdrawn (cancelShortOrder) — the one
//     order that provably cannot be funded. An order that pays any other
//     account or amount cannot reach this merchant, so it does not count.
//
// An error linking (the store is down) is returned and leaves the payment
// processing: the next tick looks again.
func (s *PayPaymentService) adoptStrayAttempt(ctx context.Context, p *models.PayPayment) (bool, error) {
	column, attemptID, kind, err := s.findStrayAttempt(ctx, p)
	if err != nil || attemptID == uuid.Nil {
		return false, err
	}
	switch err := s.linkAttempt(ctx, p.ID, column, attemptID); {
	case err == nil:
		payAlert("unlinked_attempt", p, kind, attemptID)
	case errors.Is(err, ErrPayInProgress):
		// Linked, or moved on, since the select: not ours to release either way.
	default:
		return true, err
	}
	return true, nil
}

// findStrayAttempt is adoptStrayAttempt's read: the link column, id and kind of
// the attempt p has that nothing points at, or uuid.Nil for none.
func (s *PayPaymentService) findStrayAttempt(ctx context.Context, p *models.PayPayment) (column string, id uuid.UUID, kind string, err error) {
	// An intent of an EARLIER attempt at this payment (released, then paid again)
	// is not this attempt's: only one opened since the lock counts.
	var in models.RampSellIntent
	iq := s.db.WithContext(ctx).Where("pay_payment_id = ? AND status <> ?", p.ID, models.SellIntentFailed)
	if p.LockedAt != nil {
		iq = iq.Where("created_at >= ?", *p.LockedAt)
	}
	switch err := iq.Order("created_at DESC").First(&in).Error; {
	case err == nil:
		return "sell_intent_id", in.ID, "intent", nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", uuid.Nil, "", err
	}
	if p.SettlementMode != models.PaySettlementFiat || p.PayerUserID == nil || p.LockedAt == nil {
		return "", uuid.Nil, "", nil
	}
	var o models.PaycrestOrderRecord
	switch err := s.db.WithContext(ctx).
		Where("user_id = ? AND direction = ? AND created_at >= ? AND fiat_code = ? AND fiat_amount = ? AND payout_bank_code = ? AND payout_account = ?",
			*p.PayerUserID, models.PaycrestDirectionSell, *p.LockedAt, "NGN", payFiatAmount(p.MerchantNetKobo), p.SettleBankCode, p.SettleAccountNumber).
		Where("NOT (status = ? AND deposit_address = '')", models.PaycrestStatusCancelled).
		Order("created_at DESC").First(&o).Error; {
	case err == nil:
		return "paycrest_order_id", o.ID, "order", nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", uuid.Nil, "", err
	}
	return "", uuid.Nil, "", nil
}
