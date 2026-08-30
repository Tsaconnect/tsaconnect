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
		return IsStableAsset(existing.Asset), nil
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
	q, err := s.QuoteStableBuy(ctx, intent.Asset, amountMinor, intent.Currency)
	if err != nil {
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
		// so the next incoming charge could claim it. See Task 6a.
		log.Printf("[it stable buy] ALARM charge=%s landed but could not be priced: %v", chargeID, err)
		if uErr := s.db.WithContext(ctx).Model(&intent).Updates(map[string]any{
			"status":         models.ITStatusBuyAwaitingRate,
			"failure_reason": "could not price at settlement: " + err.Error(),
		}).Error; uErr != nil {
			return false, uErr
		}
		return true, nil
	}

	if err := s.db.WithContext(ctx).Model(&intent).Updates(map[string]any{
		"asset_amount": q.AssetUnits.String(),
		"locked_rate":  q.RatePerUnitMinor.String(),
		"rate_minor":   q.RatePerUnitMinor.String(),
		"rate_kobo":    q.RatePerUnitMinor.String(),
	}).Error; err != nil {
		return false, err
	}

	log.Printf("[it stable buy] charge=%s funded %s %s for user=%s",
		chargeID, q.AssetUnits, intent.Asset, userID)
	return true, nil
}
