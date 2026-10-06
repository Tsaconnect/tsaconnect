package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	// ErrITBuyOrderOpen enforces one open MCGP buy order per user.
	//
	// It replaces superseding. Cancelling the prior order when the user opened
	// another made the first one vanish from the hub, and under central-account
	// attribution it did real harm: a transfer for the first order's amount then
	// matched no open claim and held for an operator with the money already
	// sent. Refusing keeps the order the user may be paying into; the app routes
	// them to it (ITBuyOrderOpenError carries which one), and they cancel it
	// themselves if they meant to start over.
	ErrITBuyOrderOpen = errors.New("it buy: this user already has an open buy order")

	// ErrITBuyPaymentArrived refuses a cancel once a credit that looks like this
	// order's payment is already in the inbound queue. Cancelling then would
	// close the claim the matcher is about to bind that money to, turning an
	// automatic settlement into a hold, a refund and a support ticket.
	ErrITBuyPaymentArrived = errors.New("it buy: a payment for this order has already arrived")
)

// ITBuyOrderOpenError is ErrITBuyOrderOpen with the order that caused it, so
// the caller can send the user to that order instead of a dead end.
type ITBuyOrderOpenError struct {
	Intent *models.ITIntent
}

func (e *ITBuyOrderOpenError) Error() string { return ErrITBuyOrderOpen.Error() }
func (e *ITBuyOrderOpenError) Unwrap() error { return ErrITBuyOrderOpen }

// scopeOpenMCGPBuys narrows a query to MCGP buy orders still open for payment.
//
// One definition for every reader of "open MCGP buy": the matcher's claim
// source, the stale-order sweep, the one-open-order check and the cancel CAS.
// The partial unique index uq_it_intents_open_mcgp_buy (config/database.go)
// spells the same predicate in SQL and must be changed with it.
//
//   - MCGP or the pre-Asset-column legacy rows: stablecoin buys have their own
//     lifecycle and their own one-open rule.
//   - Still carrying the MINTED payment_ref: settlement overwrites it with the
//     provider's identifier the moment money is attributed, so a row without
//     it has money landing against it and is not "open" in any sense.
func scopeOpenMCGPBuys(q *gorm.DB) *gorm.DB {
	return q.Where("direction = ? AND status = ? AND (asset IS NULL OR asset = '' OR asset = ?) AND payment_ref LIKE ?",
		models.ITDirectionBuy, models.ITStatusPendingPayment, models.ITAssetMCGP, mintedRefPrefix+"%")
}

// openMCGPBuyOrder returns the user's open MCGP buy order, or nil when there is
// none.
func (s *ITService) openMCGPBuyOrder(ctx context.Context, userID uuid.UUID) (*models.ITIntent, error) {
	var rows []models.ITIntent
	if err := scopeOpenMCGPBuys(s.db.WithContext(ctx).Where("user_id = ?", userID)).
		Order("created_at DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// IsBuyAwaitingTransfer reports whether intent is a buy the user can still pay
// into — and so still cancel, and still be shown a pay-into card for.
//
// A stablecoin buy is open while pending_payment: its settlement moves status
// and payment_ref in one write. An MCGP buy additionally needs its MINTED
// payment_ref, because MCGP settlement writes the provider ref first and moves
// status later; in between, the row reads pending_payment with money already
// attributed to it.
func IsBuyAwaitingTransfer(intent *models.ITIntent) bool {
	if intent == nil || intent.Direction != models.ITDirectionBuy || intent.Status != models.ITStatusPendingPayment {
		return false
	}
	if IsStableAsset(intent.Asset) {
		return true
	}
	return models.ITIntentAsset(intent) == models.ITAssetMCGP && strings.HasPrefix(intent.PaymentRef, mintedRefPrefix)
}

// CancelBuy lets a user abandon their own buy — MCGP or stablecoin — before
// money lands, and returns the cancelled intent.
//
// Refuses anything not owned by userID (ErrStableIntentNotFound, never leaking
// that another user's intent exists), anything that is not a buy still awaiting
// payment (ErrITBadState), and an order whose payment already appears to be in
// the inbound queue (ErrITBuyPaymentArrived).
func (s *ITService) CancelBuy(ctx context.Context, userID, intentID uuid.UUID) (*models.ITIntent, error) {
	return s.cancelBuy(ctx, userID, intentID, false)
}

// cancelBuy is CancelBuy, optionally narrowed to stablecoin buys for the older
// stablecoin-only endpoint.
func (s *ITService) cancelBuy(ctx context.Context, userID, intentID uuid.UUID, stableOnly bool) (*models.ITIntent, error) {
	intent, err := s.getIntentForUser(ctx, userID, intentID)
	if err != nil {
		return nil, err
	}
	if !IsBuyAwaitingTransfer(intent) || (stableOnly && !IsStableAsset(intent.Asset)) {
		return nil, ErrITBadState
	}
	mcgp := !IsStableAsset(intent.Asset)

	// Immediately before the CAS, never earlier, to keep the window short.
	//
	// Residual window, stated plainly: this is a read followed by a write, and
	// the credit it looks for is written by a different process (the statement
	// poller). A credit ingested after this read and before the UPDATE below
	// commits is not seen, and neither is a transfer the payer has sent but the
	// poller has not fetched yet — that second gap is the poll interval, and is
	// the larger of the two. Both end the same way: the claim is gone when the
	// matcher looks, so the credit holds for an operator to refund. No lock can
	// close it, because there is no row to lock until the poller writes one.
	// The app's confirm dialog says exactly that to the user before they tap.
	arrived, err := s.buyPaymentArrived(ctx, intent)
	if err != nil {
		return nil, err
	}
	if arrived {
		return nil, ErrITBuyPaymentArrived
	}

	// Every clause is load-bearing against a concurrent writer: status and the
	// minted ref lose to a settlement that claimed the order first; user_id
	// keeps the write on the row the ownership check read.
	q := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND user_id = ? AND direction = ? AND status = ?",
			intent.ID, userID, models.ITDirectionBuy, models.ITStatusPendingPayment)
	if mcgp {
		q = q.Where("payment_ref LIKE ?", mintedRefPrefix+"%")
	}
	res := q.Updates(map[string]any{
		"status":         models.ITStatusCancelled,
		"failure_reason": "cancelled by user",
	})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost to a payment claiming it. Not cancellable any more, and saying
		// so is the truth: the intent genuinely is not pending.
		return nil, ErrITBadState
	}
	if IsRoutedStableBuy(intent) {
		// Spec §7: an unpaid routed open is logged however it is cancelled —
		// the router's cancel and /api/it/buy/:id/cancel both land here.
		log.Printf("[it stable buy] METRIC ramp_it_open_unpaid intent=%s user=%s provider=%s reason=cancelled",
			intent.ID, userID, models.ITIntentProvider(intent))
	}
	return s.getIntentForUser(ctx, userID, intentID)
}

// buyPaymentArrived reports whether an inbound credit that the matcher would
// plausibly bind to this order is sitting unattributed: still unmatched or
// held, ingested at or after the order was opened, for the order's whole-naira
// amount, from a sender whose name matches the user's verified legal name by
// the matcher's own name function.
//
// Without a verified legal name there is nothing to compare a sender against,
// so there is nothing to refuse on and the cancel proceeds.
//
// Errors are returned, not swallowed: a guard that answers "no payment" when it
// could not look is exactly the cancel it exists to stop.
func (s *ITService) buyPaymentArrived(ctx context.Context, intent *models.ITIntent) (bool, error) {
	name, err := verifiedLegalName(ctx, s.db, intent.UserID)
	if err != nil {
		return false, fmt.Errorf("it buy cancel: legal name: %w", err)
	}
	if name == "" {
		return false, nil
	}
	want, err := strconv.ParseInt(wholeNairaKobo(intent.NgnKobo), 10, 64)
	if err != nil || want <= 0 {
		// No usable amount means the order was never offered as a claim (see
		// ITMCGPClaimSource.OpenClaims), so no credit can be waiting for it.
		return false, nil
	}

	var credits []models.InboundCredit
	if err := s.db.WithContext(ctx).
		Select("gross_kobo", "sender_name").
		Where("status IN ? AND created_at >= ?",
			[]string{models.InboundCreditUnmatched, models.InboundCreditHeld}, intent.CreatedAt).
		Find(&credits).Error; err != nil {
		return false, fmt.Errorf("it buy cancel: inbound credits: %w", err)
	}
	for _, c := range credits {
		if RoundKoboToNaira(c.GrossKobo) == want && MatchAccountName(name, c.SenderName).IsMatch() {
			return true, nil
		}
	}
	return false, nil
}
