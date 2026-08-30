package services

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

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
			if err := s.priceStableBuy(ctx, &existing); err != nil {
				log.Printf("[it stable buy] ALARM charge=%s redelivery could not price: %v", chargeID, err)
				if uErr := s.db.WithContext(ctx).Model(&existing).Updates(map[string]any{
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
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Order("created_at ASC").
		First(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil // no open stablecoin buy — fall through to MCGP
	} else if err != nil {
		return false, err
	}

	// The status in the WHERE is the concurrency guard: two webhooks racing on
	// the same intent, only one wins and the loser sees RowsAffected == 0.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", candidate.ID, models.ITStatusPendingPayment).
		Updates(map[string]any{
			"status":          models.ITStatusPaymentReceived,
			"payment_ref":     chargeID,
			"received_amount": amountMinor,
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}

	var intent models.ITIntent
	if err := s.db.WithContext(ctx).First(&intent, "id = ?", candidate.ID).Error; err != nil {
		return false, err
	}

	// Price at the LIVE rate on what actually landed. This is the binding
	// price; the figure quoted at creation was indicative. Pricing on what
	// arrived rather than what was quoted also handles under- and overpayment
	// with no special case — the mirror of the sell leg's received/quoted.
	if err := s.priceStableBuy(ctx, &intent); err != nil {
		// The money is here and cannot be handed back, so this must NOT error
		// the webhook — that would leave the charge unprocessed and re-driven
		// forever. Park for an operator with the reason recorded. Covers an
		// unusable rate and an underpayment below the provider floor alike:
		// both mean "money landed, a human must decide".
		//
		// The park status is ITStatusBuyAwaitingRate, NOT the MCGP flow's
		// awaiting_rate. Sharing that status would expose the intent to
		// RepriceAndCreditAwaitingRate, which credits MCGP from company supply
		// and then sets status back to pending_payment — re-opening the intent
		// so the next incoming charge could claim it.
		log.Printf("[it stable buy] ALARM charge=%s landed but could not be priced: %v", chargeID, err)
		if uErr := s.db.WithContext(ctx).Model(&intent).Updates(map[string]any{
			"status":         models.ITStatusBuyAwaitingRate,
			"failure_reason": "could not price at settlement: " + err.Error(),
		}).Error; uErr != nil {
			return false, uErr
		}
		return true, nil
	}

	log.Printf("[it stable buy] charge=%s funded %s %s for user=%s",
		chargeID, intent.AssetAmount, intent.Asset, userID)
	return true, nil
}

// priceStableBuy prices a claimed buy on what actually arrived
// (intent.ReceivedAmount) and moves it to payment_received. Shared by first
// settlement, by redelivery completing an interrupted settlement, and by the
// operator recovery RepriceParkedStableBuy.
func (s *ITService) priceStableBuy(ctx context.Context, intent *models.ITIntent) error {
	q, err := s.QuoteStableBuy(ctx, intent.Asset, intent.ReceivedAmount, intent.Currency)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"status":       models.ITStatusPaymentReceived,
		"asset_amount": q.AssetUnits.String(),
		"locked_rate":  q.RatePerUnitMinor.String(),
		"rate_minor":   q.RatePerUnitMinor.String(),
		"rate_kobo":    q.RatePerUnitMinor.String(),
		"repriced":     true,
	}).Error; err != nil {
		return err
	}
	intent.Status = models.ITStatusPaymentReceived
	intent.AssetAmount = q.AssetUnits.String()
	intent.Repriced = true
	return nil
}

// RepriceParkedStableBuy is the operator recovery for a stablecoin buy parked
// as `buy_awaiting_rate` — money landed that could not be priced at the time.
//
// It reprices the naira that actually arrived at the CURRENT live rate and
// returns the intent to payment_received, where the watcher picks it up again.
// The watcher deliberately never does this by itself: the intent parked because
// something was wrong, and resuming it is a human's call.
func (s *ITService) RepriceParkedStableBuy(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Direction != models.ITDirectionBuy ||
		intent.Status != models.ITStatusBuyAwaitingRate ||
		!IsStableAsset(intent.Asset) {
		return nil, ErrITBadState
	}
	if err := s.priceStableBuy(ctx, intent); err != nil {
		return nil, err
	}
	return s.getIntent(ctx, intentID)
}
