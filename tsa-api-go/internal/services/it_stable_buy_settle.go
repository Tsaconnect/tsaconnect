package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// stableBuyClaimWindow bounds how long an unpaid stablecoin buy intent may
// claim an incoming charge. Nothing else expires or cancels one, so without
// this an abandoned intent sits in pending_payment forever and hijacks a
// later, unrelated payment from the SAME user — including a plain MCGP
// purchase, which has no stablecoin asset filter to protect it. 24h, not the
// (nonexistent) rate-lock TTL: there is deliberately no rate lock on buy, and
// a bank transfer can reasonably take most of a day.
const stableBuyClaimWindow = 24 * time.Hour

// settleStableBuyFromCharge attributes an incoming naira charge to this user's
// open stablecoin purchase, if they have one.
//
// Returns (true, nil) when the charge funded a buy and the caller must stop.
// Returns (false, nil) when there was no open stablecoin buy — the caller then
// continues to the existing MCGP credit path, which is left untouched.
//
// "Open" means pending_payment specifically. That is the only state which can
// claim money: an intent already converting or delivering has been funded and
// cannot absorb more, so it must not block or capture a later payment.
func (s *ITService) settleStableBuyFromCharge(ctx context.Context, userID uuid.UUID, chargeID, amountMinor string) (bool, error) {
	chargeID = strings.TrimSpace(chargeID)
	if chargeID == "" {
		return false, errors.New("it stable buy: empty charge id")
	}

	// Idempotency first: a redelivered webhook must not settle twice.
	var existing models.ITIntent
	err := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).
		First(&existing).Error
	if err == nil {
		if !IsStableAsset(existing.Asset) {
			return false, nil
		}
		// A crash between claiming the charge (below) and pricing it would
		// otherwise leave the creation-time quote in place forever — the rate
		// lock the design removed, reappearing in a crash window. Redelivery
		// finishes the job the interrupted attempt started.
		if existing.Status == models.ITStatusPaymentReceived && !existing.Repriced {
			if _, err := s.priceStableBuy(ctx, &existing, existing.ReceivedAmount,
				models.ITStatusPaymentReceived, models.ITStatusPaymentReceived); err != nil {
				log.Printf("[it stable buy] ALARM charge=%s redelivery could not price: %v", chargeID, err)
				// Status-guarded like every other write on this path: a watcher
				// tick that advanced the intent while we were on the rate call
				// must not be dragged backwards into the park.
				if uErr := s.db.WithContext(ctx).Model(&models.ITIntent{}).
					Where("id = ? AND status = ?", existing.ID, models.ITStatusPaymentReceived).
					Updates(map[string]any{
						"status":         models.ITStatusBuyAwaitingRate,
						"failure_reason": "could not price at settlement: " + err.Error(),
					}).Error; uErr != nil {
					return false, uErr
				}
			}
		}
		return true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	// Claim atomically: the WHERE carries the expected status, so two
	// concurrent webhooks cannot both move the same intent. Only the amount is
	// written here — the price is struck below, once the intent is ours.
	amountMinor = strings.TrimSpace(amountMinor)

	// Pick ONE intent, then claim it by id. A bulk Updates over the whole
	// predicate would write the same payment_ref to every matching row if the
	// one-open-purchase check ever raced and let two through — and payment_ref
	// carries a partial UNIQUE index (config/database.go), so that INSERT-time
	// race would become a permanent webhook error instead of a claimed charge.
	var candidate models.ITIntent
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ? AND created_at >= ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}, time.Now().Add(-stableBuyClaimWindow)).
		Order("created_at ASC").
		First(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil // no open stablecoin buy — fall through to MCGP
	} else if err != nil {
		return false, err
	}

	// Price BEFORE claiming, so that claiming and pricing are ONE write.
	//
	// Pricing in a second write left a window as wide as an HTTP round-trip to
	// Flutterwave in which the intent sat at payment_received carrying its
	// CREATION-TIME asset_amount. Anything that interrupted that window — a
	// rolling deploy, a panic, a cancelled context — left the watcher to
	// convert the real received_amount while delivering the stale figure: an
	// overpayer silently loses the excess, and an underpayer's delivery is
	// drawn from the POOLED merchant balance, i.e. another user's stablecoin.
	// Quoting first costs one wasted quote if we then lose the claim race,
	// which is far cheaper than the window.
	claim := map[string]any{
		"status":          models.ITStatusPaymentReceived,
		"payment_ref":     chargeID,
		"received_amount": amountMinor,
	}
	q, qErr := s.QuoteStableBuy(ctx, candidate.Asset, amountMinor, candidate.Currency)
	if qErr != nil {
		// The money is here and cannot be handed back, so this must NOT error
		// the webhook — that would leave the charge unprocessed and re-driven
		// forever. Park for an operator with the reason recorded, in the SAME
		// write that claims: the charge is never recorded against an intent
		// without either a price or a park. Covers an unusable rate and an
		// underpayment below the provider floor alike — both mean "money
		// landed, a human must decide".
		//
		// The park status is ITStatusBuyAwaitingRate, NOT the MCGP flow's
		// awaiting_rate. Sharing that status would expose the intent to
		// RepriceAndCreditAwaitingRate, which credits MCGP from company supply
		// and then sets status back to pending_payment — re-opening the intent
		// so the next incoming charge could claim it.
		log.Printf("[it stable buy] ALARM charge=%s landed but could not be priced: %v", chargeID, qErr)
		claim["status"] = models.ITStatusBuyAwaitingRate
		claim["failure_reason"] = "could not price at settlement: " + qErr.Error()
	} else {
		// Priced at the LIVE rate on what actually landed. This is the binding
		// price; the figure quoted at creation was indicative. Pricing on what
		// arrived rather than what was quoted also handles under- and
		// overpayment with no special case — the mirror of the sell leg's
		// received/quoted.
		claim["asset_amount"] = q.AssetUnits.String()
		claim["locked_rate"] = q.RatePerUnitMinor.String()
		claim["rate_minor"] = q.RatePerUnitMinor.String()
		claim["rate_kobo"] = q.RatePerUnitMinor.String()
		claim["repriced"] = true
	}

	// The status in the WHERE is the concurrency guard: two webhooks racing on
	// the same intent, only one wins and the loser sees RowsAffected == 0.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", candidate.ID, models.ITStatusPendingPayment).
		Updates(claim)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race: another call (a concurrent redelivery, most likely)
		// claimed this exact intent first — and may have claimed it with THIS
		// chargeID, in which case falling through to MCGP would hand the
		// winner's charge to the wrong path. Re-check by payment_ref rather
		// than assuming "someone else, unrelated" was the cause.
		var winner models.ITIntent
		wErr := s.db.WithContext(ctx).
			Where("payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).
			First(&winner).Error
		if wErr == nil {
			return IsStableAsset(winner.Asset), nil
		} else if !errors.Is(wErr, gorm.ErrRecordNotFound) {
			return false, wErr
		}
		return false, nil
	}

	if qErr == nil {
		log.Printf("[it stable buy] charge=%s funded %s %s for user=%s",
			chargeID, q.AssetUnits, candidate.Asset, userID)
	}
	return true, nil
}

// priceStableBuy re-prices `intent` on `fiatMinor` at the CURRENT live rate,
// moving it from `fromStatus` to `toStatus` in one status-guarded write.
//
// The status guard is not optional. Unguarded, a price write that lands late —
// the rate call is an HTTP round-trip — drags an intent BACKWARDS out of
// `converting` after the naira has already been spent; the next watcher tick
// then re-enters ConvertStableBuy and converts a second time with the SAME
// reference, leaving nothing but the provider's own idempotency between us and
// moving real money twice.
//
// Returns false when no row matched: something else advanced the intent first,
// and it is no longer ours to price.
//
// Repricing a `delivering` intent additionally requires delivery_id to still
// be empty, and that condition is part of the CAS rather than a caller's prior
// read. The watcher calls DeliverStableBuy on every delivering intent every
// 30s, and the slippage jam this recovery exists for clears the moment the
// balance settles — so "the watcher sends while the operator is mid-reprice"
// is a live sequence, and the rate call is an HTTP round-trip wide. Checked
// outside the write, it would rewrite asset_amount on a transfer already sent
// for the old figure: no double send, but the intent settles recording an
// amount that is not what was paid.
//
// The amount is a parameter rather than a read of intent.ReceivedAmount so
// that the first settlement can price BEFORE it claims — see
// settleStableBuyFromCharge. Callers after the claim pass ReceivedAmount.
func (s *ITService) priceStableBuy(ctx context.Context, intent *models.ITIntent, fiatMinor, fromStatus, toStatus string) (bool, error) {
	q, err := s.QuoteStableBuy(ctx, intent.Asset, fiatMinor, intent.Currency)
	if err != nil {
		return false, err
	}
	cas := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", intent.ID, fromStatus)
	if fromStatus == models.ITStatusDelivering {
		cas = cas.Where("delivery_id = ?", "")
	}
	res := cas.Updates(map[string]any{
		"status":         toStatus,
		"asset_amount":   q.AssetUnits.String(),
		"locked_rate":    q.RatePerUnitMinor.String(),
		"rate_minor":     q.RatePerUnitMinor.String(),
		"rate_kobo":      q.RatePerUnitMinor.String(),
		"repriced":       true,
		"failure_reason": "", // clear a stale "could not price at settlement" from a prior park
	})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	intent.Status = toStatus
	intent.AssetAmount = q.AssetUnits.String()
	intent.LockedRate = q.RatePerUnitMinor.String()
	intent.Repriced = true
	intent.FailureReason = ""
	return true, nil
}

// RepriceParkedStableBuy is the operator recovery for a stablecoin buy that is
// holding the user's money and cannot advance on its own terms. It reprices
// what actually arrived at the CURRENT live rate. The watcher deliberately
// never does this by itself: both states below mean something was wrong, and
// resuming is a human's call.
//
// Two states qualify:
//
//	buy_awaiting_rate            money landed that could not be priced at the
//	                             time. Returns to payment_received, where the
//	                             watcher picks it up again.
//	delivering, delivery_id = "" the naira was converted but the outbound
//	                             transfer has not been sent. AssetAmount came
//	                             from the /transfers/rates PREVIEW at
//	                             settlement, while ConvertBalance executed at
//	                             its own rate less a fee; if that landed worse
//	                             than the spread cushion, DeliverStableBuy's
//	                             balance gate refuses forever and no other
//	                             endpoint can reach the intent. Stays in
//	                             `delivering` — the naira is already spent, so
//	                             sending it back to payment_received would have
//	                             the watcher convert a second time.
//
// Repricing a `delivering` intent uses the rate NOW, not what the conversion
// actually produced (FWTransfer carries no destination amount, so that figure
// is not available to us). If the rate has moved the other way the new amount
// can still exceed the balance; the call is safely repeatable.
func (s *ITService) RepriceParkedStableBuy(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Direction != models.ITDirectionBuy || !IsStableAsset(intent.Asset) {
		return nil, ErrITBadState
	}
	var to string
	switch {
	case intent.Status == models.ITStatusBuyAwaitingRate:
		to = models.ITStatusPaymentReceived
	case intent.Status == models.ITStatusDelivering && intent.DeliveryID == "":
		to = models.ITStatusDelivering
	default:
		return nil, ErrITBadState
	}
	applied, err := s.priceStableBuy(ctx, intent, intent.ReceivedAmount, intent.Status, to)
	if err != nil {
		return nil, err
	}
	if !applied {
		// Someone moved it between our read and our write.
		return nil, ErrITBadState
	}
	return s.getIntent(ctx, intentID)
}
