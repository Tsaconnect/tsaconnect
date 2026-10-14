package services

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// refundPoolBuy refunds a paid, undelivered pool order in naira — only while
// no fill is recorded (vaultRefundable), decided under the pool lock and the
// intent's row lock, the locks the settle engine takes before it fills.
func (s *ITService) refundPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	d := s.poolDesk
	if d == nil {
		return nil, ErrPoolSettleUnwired
	}
	var from string
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStableIntentNotFound
			}
			return err
		}
		if intent.Status != models.ITStatusPaymentReceived && intent.Status != models.ITStatusBuyAwaitingRate {
			return fmt.Errorf("%w: a TSA pool buy is refundable only while paid and undelivered (status %s)", ErrITBadState, intent.Status)
		}
		if err := vaultRefundable(tx, intentID); err != nil {
			return err
		}
		from = intent.Status
		return s.writePoolRefund(ctx, tx, &intent, adminID, models.ITAuditStableBuyRefunded, note, "an operator", map[string]any{
			"receivedAmount": intent.ReceivedAmount, "provider": models.ITProviderPool, "fromStatus": intent.Status, "parkedReason": intent.FailureReason})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[liquidity] METRIC ramp_it_refunded intent=%s provider=tsa_pool from=%s admin=%s", intentID, from, adminID)
	return s.getIntent(ctx, intentID)
}

// writePoolRefund, under the caller's pool lock and intent row lock: never
// with a live sale leg (her delivery), the intent CAS to refunded from the
// status the caller read, the conversion record cancelled, audited.
func (s *ITService) writePoolRefund(ctx context.Context, tx *gorm.DB, intent *models.ITIntent,
	adminID uuid.UUID, action, note, by string, meta map[string]any) error {
	if leg, err := liveSaleLegOn(ctx, tx, intent.ID); err != nil || leg != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: a TSA pool buy with a sale in flight is never refunded", ErrITBadState)
	}
	res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, intent.Status).
		Updates(map[string]any{"status": models.ITStatusRefunded, "failure_reason": "refunded by " + by})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: the order moved while the refund was recorded", ErrITBadState)
	}
	if err := markConversion(ctx, tx, intent.ID, []string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending},
		map[string]any{"status": models.PaycrestStatusCancelled, "failure_reason": "refunded in naira by " + by}); err != nil {
		return err
	}
	return auditIntent(tx, intent.ID, adminID, action, note, meta)
}

// poolOrderActions are the operator actions the server allows right now.
func (s *ITService) poolOrderActions(ctx context.Context, intent *models.ITIntent) []string {
	return s.vaultOrderActions(ctx, intent)
}
