package services

import (
	"context"
	"errors"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// How long the reconciler waits before it gives up on a converted attempt.
// Every one of them ends by releasing or failing the payment ONLY after the
// intent is ended, so a swap that lands later can never pay the merchant.
const (
	// payLockNoAttemptGrace frees a live lock with neither an order nor an
	// intent behind it — the customer locked, then abandoned before an attempt
	// started. Far longer than any provider call a start makes.
	payLockNoAttemptGrace = 10 * time.Minute
	// payUnsignedSwapGrace frees an intent whose swap she never sent.
	payUnsignedSwapGrace = 10 * time.Minute
	// payLandedNoOrderGrace frees a swap that landed but whose payout order
	// she never opened; she keeps the USDC.
	payLandedNoOrderGrace = 2 * time.Hour
	// payUnknownSwapGrace frees a swap LiFi has no record of.
	payUnknownSwapGrace = 30 * time.Minute
	// payPendingSwapMax is how long a swap may stay pending before support
	// takes over: it may still deliver, so the payment is failed, not released.
	payPendingSwapMax = 24 * time.Hour
)

// reconcileProcessing moves every in-flight payment along its one attempt. It
// walks all processing payments in id order, `batch` at a time, so a long run
// of payments waiting on a customer's deposit can never starve a newer one.
// A payment whose step fails is logged and the sweep goes on; the joined
// error is what the worker's failure-streak alert reads.
func (s *PayPaymentService) reconcileProcessing(ctx context.Context, batch int) error {
	var errs []error
	if _, err := s.releaseUnattempted(ctx, models.PayModeLive, payLockNoAttemptGrace, batch,
		"released: no payment was started"); err != nil {
		errs = append(errs, err)
	}
	var after *uuid.UUID
	for {
		q := s.db.WithContext(ctx).Model(&models.PayPayment{}).
			Where("status = ? AND (paycrest_order_id IS NOT NULL OR sell_intent_id IS NOT NULL)", models.PayStatusProcessing)
		if after != nil {
			q = q.Where("id > ?", *after)
		}
		var ids []uuid.UUID
		if err := q.Order("id").Limit(batch).Pluck("id", &ids).Error; err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, id := range ids {
			if err := s.reconcileOne(ctx, id); err != nil {
				log.Printf("[tsa-pay] reconcile payment %s: %v", id, err)
				errs = append(errs, err)
			}
		}
		if len(ids) < batch {
			return errors.Join(errs...)
		}
		after = &ids[len(ids)-1]
	}
}

// reconcileOne re-reads the payment (an earlier step in this tick may have
// moved it) and applies the rule for its one attempt.
func (s *PayPaymentService) reconcileOne(ctx context.Context, id uuid.UUID) error {
	p, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != models.PayStatusProcessing {
		return nil
	}
	var stepErr error
	switch {
	case p.PaycrestOrderID != nil:
		stepErr = s.reconcileOrder(ctx, p)
	case p.SellIntentID != nil:
		stepErr = s.reconcileIntent(ctx, p)
	}
	// A guarded transition that matched nothing means someone else already
	// moved the payment on: not this sweep's problem.
	if errors.Is(stepErr, ErrPayTransition) {
		return nil
	}
	return stepErr
}

// reconcileOrder settles a payment by its Paycrest order's status. Only two
// endings free the payment for a retry — refunded, and expired with no sign
// of a deposit: everything else either waits or is failed for support.
func (s *PayPaymentService) reconcileOrder(ctx context.Context, p *models.PayPayment) error {
	var o models.PaycrestOrderRecord
	if err := s.db.WithContext(ctx).First(&o, "id = ?", *p.PaycrestOrderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[tsa-pay] payment %s links order %s which has no row; waiting", p.ID, *p.PaycrestOrderID)
			return nil
		}
		return err
	}
	switch o.Status {
	case models.PaycrestStatusSettled:
		return s.Succeed(ctx, p.ID, o.ID.String())
	case models.PaycrestStatusRefunded:
		return s.Release(ctx, p.ID, "the payout was refunded to the customer — they can try again")
	case models.PaycrestStatusExpired:
		deposit, err := s.orderShowsDeposit(ctx, &o)
		if err != nil {
			return err
		}
		if deposit {
			payAlert("order_expired_after_deposit", p, "order", o.ID)
			return s.Fail(ctx, p.ID, "the order expired after a deposit may have been sent — support is reviewing it")
		}
		return s.Release(ctx, p.ID, "the order lapsed before payment")
	case models.PaycrestStatusUnmatched:
		payAlert("order_unmatched", p, "order", o.ID)
		return s.Fail(ctx, p.ID, "the deposit couldn't be matched — support is reviewing it")
	case models.PaycrestStatusFailed:
		if payAmountPositive(o.AmountPaid) {
			payAlert("order_failed_with_deposit", p, "order", o.ID)
		}
		return s.Fail(ctx, p.ID, "the payment wasn't completed")
	case models.PaycrestStatusCancelled:
		// Cancelled on OUR side only: Paycrest's copy may still take a deposit,
		// so the payment is never handed back.
		return s.Fail(ctx, p.ID, "the payment wasn't completed")
	case models.PaycrestStatusAwaitingDeposit, models.PaycrestStatusPending, models.PaycrestStatusSettling:
		return nil
	default:
		log.Printf("[tsa-pay] payment %s: order %s is %q; waiting", p.ID, o.ID, o.Status)
		return nil
	}
}

// orderShowsDeposit reports any sign the customer's money reached, or may
// have reached, the order: a paid amount, her "I've sent it" claim, or a
// deposit we relayed for her. An unreadable sign is an error, never "none".
func (s *PayPaymentService) orderShowsDeposit(ctx context.Context, o *models.PaycrestOrderRecord) (bool, error) {
	if payAmountPositive(o.AmountPaid) || o.PaymentClaimedAt != nil {
		return true, nil
	}
	var relayed int64
	err := s.db.WithContext(ctx).Model(&models.GasSponsorship{}).
		Where("order_id = ? AND tx_hash IS NOT NULL AND tx_hash <> ''", o.ID).Count(&relayed).Error
	return relayed > 0, err
}

// payAmountPositive reads Paycrest's amount strings: empty and zero are "no
// money", a positive number is money, and anything unparsable is treated as
// money — the safe reading when the answer decides whether a retry is allowed.
func payAmountPositive(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	r, ok := new(big.Rat).SetString(v)
	return !ok || r.Sign() != 0
}

// reconcileIntent moves a converted payment along its sell intent.
func (s *PayPaymentService) reconcileIntent(ctx context.Context, p *models.PayPayment) error {
	if s.intents == nil {
		log.Printf("[tsa-pay] payment %s links intent %s but no intent service is wired; waiting", p.ID, *p.SellIntentID)
		return nil
	}
	var in models.RampSellIntent
	if err := s.db.WithContext(ctx).First(&in, "id = ?", *p.SellIntentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[tsa-pay] payment %s links intent %s which has no row; waiting", p.ID, *p.SellIntentID)
			return nil
		}
		return err
	}
	switch in.Status {
	case models.SellIntentOrdered:
		if in.PaycrestOrderID == nil {
			return nil
		}
		return s.handOverToOrder(ctx, p, *in.PaycrestOrderID)
	case models.SellIntentOrdering:
		return nil // her payout order is being opened right now
	}
	age := s.now().Sub(payLockedAt(p))
	if in.SwapTxHash == nil {
		// No swap was ever sent, so no money left her wallet.
		if in.Status == models.SellIntentFailed {
			return s.Release(ctx, p.ID, "the swap was cancelled before it was sent")
		}
		if age <= payUnsignedSwapGrace {
			return nil
		}
		return s.endIntentThen(ctx, &in, func() error {
			return s.Release(ctx, p.ID, "the swap was never sent")
		})
	}
	if in.DestAddress != "" {
		return s.reconcileDelivery(ctx, p, &in, age)
	}
	return s.reconcileSwap(ctx, p, &in, age)
}

// reconcileDelivery settles a crypto payment (its intent delivers a token to the
// merchant's wallet). The chain, not LiFi, says whether the merchant was paid in
// full: ConfirmDelivery proves it, and only that makes the payment succeed.
// Anything short of a proof follows the swap's own rules (reconcileSwap).
func (s *PayPaymentService) reconcileDelivery(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent, age time.Duration) error {
	if in.Status == models.SellIntentSwapped || in.Status == models.SellIntentDelivered {
		destTx, err := s.intents.ConfirmDelivery(ctx, in.ID)
		switch {
		case err == nil:
			return s.Succeed(ctx, p.ID, destTx)
		case errors.Is(err, ErrSwapNotLanded):
			// Not delivered yet, or never going to be: decided below from LiFi's status.
		case errors.Is(err, ErrSwapMismatch):
			// LiFi says the swap is done but the merchant's wallet was not credited
			// in full, or was credited elsewhere. The value may have reached them,
			// so the payment is never handed back and the swap is not abandoned.
			payAlert("delivery_mismatch", p, "intent", in.ID)
			return s.Fail(ctx, p.ID, "the transfer didn't deliver the full amount — support is reviewing it")
		case errors.Is(err, ErrSellUnavailable):
			// No verifier for this chain (yet) is not a verdict on the payment.
			log.Printf("[tsa-pay] payment %s: cannot prove delivery for intent %s yet: %v; waiting", p.ID, in.ID, err)
			return nil
		default:
			return err
		}
	}
	return s.reconcileSwap(ctx, p, in, age)
}

// reconcileSwap decides a payment whose swap was sent by what LiFi says became
// of it. Every ending first ends the intent, so a swap that lands later can no
// longer open a payout for a payment that has moved on.
func (s *PayPaymentService) reconcileSwap(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent, age time.Duration) error {
	st, err := s.intents.SwapStatus(ctx, in.ID)
	if err != nil {
		return err
	}
	var status, sub string
	if st != nil {
		status, sub = st.Status, st.Substatus
	}
	release := func(reason string) error {
		return s.endIntentThen(ctx, in, func() error { return s.Release(ctx, p.ID, reason) })
	}
	switch {
	case status == LiFiStatusFailed || (status == LiFiStatusDone && sub == LiFiSubstatusRefunded):
		return release("the swap didn't go through — your funds are back in your wallet")
	case status == LiFiStatusDone && sub == LiFiSubstatusPartial:
		return s.endIntentThen(ctx, in, func() error {
			payAlert("swap_partial", p, "intent", in.ID)
			return s.Fail(ctx, p.ID, "the swap only partly completed — support is reviewing it")
		})
	case status == LiFiStatusDone && in.DestAddress != "":
		// A delivery LiFi calls done that ConfirmDelivery could not prove, or one
		// already ended: the merchant may have been paid, so it is never released.
		return s.endIntentThen(ctx, in, func() error {
			payAlert("delivery_unproven", p, "intent", in.ID)
			return s.Fail(ctx, p.ID, "the transfer couldn't be confirmed — support is reviewing it")
		})
	case status == LiFiStatusDone:
		if age <= payLandedNoOrderGrace {
			return nil
		}
		// The USDC landed in her wallet and she never paid with it; it stays
		// hers, and nothing can reach the merchant once the intent is ended.
		return release("the payment wasn't completed — the swapped funds are in your wallet")
	case status == LiFiStatusPending:
		if age <= payPendingSwapMax {
			return nil
		}
		return s.endIntentThen(ctx, in, func() error {
			payAlert("swap_pending_too_long", p, "intent", in.ID)
			return s.Fail(ctx, p.ID, "the swap is taking too long — support is reviewing it")
		})
	default: // NOT_FOUND, INVALID, or no answer
		if age <= payUnknownSwapGrace {
			return nil
		}
		return release("the swap could not be found")
	}
}

// endIntentThen ends the payment's sell intent and, only if it is ended, runs
// next. An intent that moved on to ordering or ordered while we looked is left
// alone (the next tick follows it); one already failed is already ended.
func (s *PayPaymentService) endIntentThen(ctx context.Context, in *models.RampSellIntent, next func() error) error {
	if in.Status != models.SellIntentFailed {
		err := s.intents.AbandonForPay(ctx, in.ID)
		if errors.Is(err, ErrSellIntentState) {
			var cur models.RampSellIntent
			if rerr := s.db.WithContext(ctx).First(&cur, "id = ?", in.ID).Error; rerr != nil {
				return rerr
			}
			if cur.Status != models.SellIntentFailed {
				return nil
			}
			err = nil
		}
		if err != nil {
			return err
		}
	}
	return next()
}

// handOverToOrder makes a converted payment's payout order its own attempt:
// the intent opened it, so it continues the same attempt and the order rules
// take over next tick.
func (s *PayPaymentService) handOverToOrder(ctx context.Context, p *models.PayPayment, orderID uuid.UUID) error {
	res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("id = ? AND status = ? AND sell_intent_id = ?", p.ID, models.PayStatusProcessing, *p.SellIntentID).
		Updates(map[string]any{"paycrest_order_id": orderID, "sell_intent_id": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPayTransition
	}
	return nil
}

// payLockedAt is when the customer took the payment; a payment somehow
// without a lock time falls back to its last update rather than never aging.
func payLockedAt(p *models.PayPayment) time.Time {
	if p.LockedAt != nil {
		return *p.LockedAt
	}
	return p.UpdatedAt
}

// payAlert writes the log line an operator alerts on for a payment that needs
// a human, in the codebase's `ALERT name_total=1` form.
func payAlert(name string, p *models.PayPayment, attempt string, attemptID uuid.UUID) {
	log.Printf("[tsa-pay] ALERT pay_%s_total=1 payment=%s merchant=%s %s=%s", name, p.ID, p.MerchantID, attempt, attemptID)
}
