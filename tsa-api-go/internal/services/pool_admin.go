package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// refundPoolBuy is S37 (plan C2 P11): only payment_received or
// buy_awaiting_rate, and only with zero slices at the treasury in any
// generation — none releasing, none with a broadcast release not proven gone
// (review C-1) — decided under the pool lock and the intent's row lock, the
// locks the settle engine takes before it releases anything. Every live slice
// is dropped for the canceller; if one of them moved under the decision (its
// release flip landed first), the whole refund rolls back.
func (s *ITService) refundPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	d := s.poolDesk
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
		// Never refund and deliver: a sale leg in flight or done is her delivery.
		if leg, err := liveSaleLegOn(ctx, tx, intentID); err != nil || leg != nil {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: a TSA pool buy with a sale in flight is never refunded", ErrITBadState)
		}
		var slices []models.P2PTrade
		if err := tx.Where("it_intent_id = ?", intentID).Find(&slices).Error; err != nil {
			return err
		}
		at, live := poolRefundSlices(slices)
		if len(at) > 0 {
			return fmt.Errorf("%w: a TSA pool buy with a slice at the treasury (%s) must be delivered, never refunded — re-deliver it",
				ErrITBadState, strings.Join(at, ", "))
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intentID, intent.Status).
			Updates(map[string]any{"status": models.ITStatusRefunded, "failure_reason": "refunded by an operator"})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: the order moved while the refund was recorded", ErrITBadState)
		}
		if err := d.dropAllSlices(tx, live, d.now()); err != nil {
			return err
		}
		if err := markConversion(ctx, tx, intentID, []string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending},
			map[string]any{"status": models.PaycrestStatusCancelled, "failure_reason": "refunded in naira by an operator"}); err != nil {
			return err
		}
		from = intent.Status
		return auditIntent(tx, intentID, adminID, models.ITAuditStableBuyRefunded, note, map[string]any{
			"receivedAmount": intent.ReceivedAmount, "provider": models.ITProviderPool, "fromStatus": intent.Status, "parkedReason": intent.FailureReason})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[liquidity] METRIC ramp_it_refunded intent=%s provider=tsa_pool from=%s admin=%s", intentID, from, adminID)
	return s.getIntent(ctx, intentID)
}

// poolRefundSlices splits an order's slices for S37: the ones at the
// treasury (refusal evidence, "id8:status") and the live ones to drop.
func poolRefundSlices(slices []models.P2PTrade) (at []string, live []uuid.UUID) {
	for _, sl := range slices {
		switch {
		case sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason):
			at = append(at, sl.ID.String()[:8]+":"+sl.Status)
		case sl.DroppedAt == nil && (sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid):
			live = append(live, sl.ID)
		}
	}
	return at, live
}

// PayPoolSliceLP is F22's operator exit (P13): an LP self-released a slice
// that does not back an order holding her naira (poolF22Open, checked under
// the locks in forwardSlice); the operator pays the LP at the snapshot rate. Proven release first; the F22 marker (CAS under the slice
// row lock) and the forward's unique trade_id make a second call a refusal,
// never a second payment.
func (s *ITService) PayPoolSliceLP(ctx context.Context, adminID, intentID, sliceID uuid.UUID, note string) (*models.P2PSettlementForward, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.settleWired() {
		return nil, ErrPoolSettleUnwired
	}
	var sl models.P2PTrade
	if err := s.db.WithContext(ctx).First(&sl, "id = ? AND it_intent_id = ?", sliceID, intentID).Error; err != nil || !sl.IsPoolSlice() {
		return nil, ErrTradeNotFound // review m-8: never a slice of another order
	}
	proven, err := d.proveSliceRelease(ctx, &sl, s.saleDepth())
	if err != nil || !proven {
		return nil, fmt.Errorf("%w: the slice's release is not proven at the treasury yet", ErrITBadState)
	}
	fwd, err := s.forwardSlice(ctx, d, &sl, "pool-f22:"+sl.ID.String(), true)
	if err != nil {
		return nil, err
	}
	if aerr := auditIntent(s.db.WithContext(ctx), *sl.ITIntentID, adminID, models.ITAuditPoolSliceLPPaid, note,
		map[string]any{"slice": sl.ID.String(), "forward": fwd.ID.String(), "naira": fwd.Amount}); aerr != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_audit_unrecorded slice=%s err=%s", sl.ID, RedactAlertText(aerr.Error()))
	}
	return fwd, nil
}

// NotePoolSliceReturned records that an operator handed a self-released
// slice's USDT back to its LP (P13): audit, plus F22's one durable marker —
// cancel_reason = pool_f22:returned and dropped_at — taken under the slice's
// row lock. It refuses when any forward exists for the slice (automatic or
// F22): an LP gets naira OR the USDT back, never both (review I-5).
func (s *ITService) NotePoolSliceReturned(ctx context.Context, adminID, intentID, sliceID uuid.UUID, txHash, note string) error {
	if note = strings.TrimSpace(note); note == "" {
		return ErrITOperatorNoteRequired
	}
	var pre models.P2PTrade
	if err := s.db.WithContext(ctx).Select("id", "it_intent_id").First(&pre, "id = ? AND it_intent_id = ?", sliceID, intentID).Error; err != nil || !pre.IsPoolSlice() {
		return ErrTradeNotFound // review m-8: never a slice of another order
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// C1 lock order: the intent, then the slice. Re-review I-B: a slice
		// that still backs an order holding her naira (paid, parked,
		// delivering, settled) backs her delivery and is never "returned".
		var in models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&in, "id = ?", *pre.ITIntentID).Error; err != nil {
			return err
		}
		var sl models.P2PTrade
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sl, "id = ?", sliceID).Error; err != nil {
			return ErrTradeNotFound
		}
		if !poolF22Open(in.Status, sl.DroppedAt) {
			return fmt.Errorf("%w: the order is %s and this slice backs her delivery; F22 \"returned\" is only for a slice that no longer backs a paid order", ErrITBadState, in.Status)
		}
		if sl.Status != models.P2PTradeStatusReleased && sl.Status != models.P2PTradeStatusResolved {
			return fmt.Errorf("%w: slice %s is %s — only USDT that left escrow can be handed back", ErrITBadState, sl.ID, sl.Status)
		}
		var n int64
		if err := tx.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", sl.ID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: the LP was already paid naira for this slice", ErrITBadState)
		}
		res := tx.Model(&models.P2PTrade{}).Where("id = ? AND COALESCE(cancel_reason, '') = ''", sl.ID).
			Updates(map[string]any{"cancel_reason": poolF22Returned, "dropped_at": gorm.Expr("COALESCE(dropped_at, ?)", time.Now().UTC())})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: this slice's F22 exit was already taken (%s)", ErrITBadState, sl.CancelReason)
		}
		return auditIntent(tx, *sl.ITIntentID, adminID, models.ITAuditPoolSliceReturned, note,
			map[string]any{"slice": sl.ID.String(), "returnTx": strings.TrimSpace(txHash)})
	})
}

// RefundPoolBuyOverride is S37's super-admin override (spec S37, review I-6):
// the exit from a pool_slices_short park that re-delivery cannot fill. Every
// at-treasury slice must already be released and forwarded (the LPs are paid
// first); the buyer's naira is then marked refunded and the USDT those slices
// moved stays with the company in the treasury (booked in the audit row and
// swept like any surplus, R4).
func (s *ITService) RefundPoolBuyOverride(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.settleWired() {
		return nil, ErrPoolSettleUnwired
	}
	held := new(big.Int)
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			return err
		}
		if !isPoolBuy(&intent) || intent.Status != models.ITStatusBuyAwaitingRate ||
			!strings.HasPrefix(intent.FailureReason, models.ITFailurePoolSlicesShort) {
			return fmt.Errorf("%w: the override exits only a %s park", ErrITBadState, models.ITFailurePoolSlicesShort)
		}
		var slices []models.P2PTrade
		if err := tx.Where("it_intent_id = ?", intentID).Find(&slices).Error; err != nil {
			return err
		}
		var live []uuid.UUID
		for _, sl := range slices {
			switch {
			case sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason):
				var n int64
				if err := tx.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", sl.ID).Count(&n).Error; err != nil {
					return err
				}
				if (sl.Status != models.P2PTradeStatusReleased && sl.Status != models.P2PTradeStatusResolved) || n == 0 {
					return fmt.Errorf("%w: slice %s is not yet released and its LP forwarded — wait for the books pass", ErrITBadState, sl.ID)
				}
				held.Add(held, d.toCanonical(sl.TokenAmount))
			case sl.DroppedAt == nil && (sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid):
				live = append(live, sl.ID)
			}
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intentID, models.ITStatusBuyAwaitingRate).
			Updates(map[string]any{"status": models.ITStatusRefunded, "failure_reason": "refunded by a super-admin override"})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: the order moved while the override was recorded", ErrITBadState)
		}
		if err := d.dropAllSlices(tx, live, d.now()); err != nil {
			return err
		}
		if err := markConversion(ctx, tx, intentID, []string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending},
			map[string]any{"status": models.PaycrestStatusCancelled, "failure_reason": "refunded in naira by a super-admin override"}); err != nil {
			return err
		}
		return auditIntent(tx, intentID, adminID, models.ITAuditPoolRefundOverride, note, map[string]any{
			"receivedAmount": intent.ReceivedAmount, "treasuryHeldUnits": held.String()})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[liquidity] ALERT liquidity_pool_refund_override intent=%s admin=%s treasury_held_units=%s — company holds that USDT; sweep it", intentID, adminID, held)
	return s.getIntent(ctx, intentID)
}

// poolF22Open: F22's operator exits (pay the LP, or record the USDT handed
// back) apply to a slice that does not back an order holding her naira —
// the order is unpaid or terminal, or the slice was dropped (review I-1, m-2).
// A slice still backing a paid order is paid by the automatic forward only.
func poolF22Open(orderStatus string, droppedAt *time.Time) bool {
	return droppedAt != nil || !poolOrderHoldsMoney(orderStatus)
}

// poolOrderActions are the operator actions the server allows right now (P20).
func (s *ITService) poolOrderActions(ctx context.Context, intent *models.ITIntent) []string {
	out := []string{}
	var slices []models.P2PTrade
	if err := s.db.WithContext(ctx).Where("it_intent_id = ?", intent.ID).Find(&slices).Error; err != nil {
		return out // unreadable: offer nothing
	}
	atTreasury, live := false, false
	for _, sl := range slices {
		atTreasury = atTreasury || sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason)
		live = live || sl.Status == models.P2PTradeStatusPendingPayment || sl.Status == models.P2PTradeStatusPaid ||
			sl.Status == models.P2PTradeStatusReleasing || sl.Status == models.P2PTradeStatusDisputed
	}
	switch intent.Status {
	case models.ITStatusPaymentReceived:
		if !atTreasury {
			out = append(out, "refund")
		}
	case models.ITStatusBuyAwaitingRate:
		if !atTreasury {
			out = append(out, "refund")
		}
		if strings.HasPrefix(intent.FailureReason, models.ITFailurePoolSlicesShort) && !live {
			out = append(out, "redeliver")
			if atTreasury {
				out = append(out, "refund-override") // S37's super-admin exit (review I-6)
			}
		}
	case models.ITStatusDelivering:
		if leg, err := s.liveSaleLeg(ctx, intent.ID); err == nil && leg != nil && leg.Status == models.RampLegStatusPending &&
			time.Since(leg.CreatedAt) > s.treasuryLimits().sendStall {
			out = append(out, "resend") // allowed by the server only when its evidence allows (resendSale)
		}
	}
	return out
}
