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
	// payDeliveryUnsignedGrace is payUnsignedSwapGrace for a DELIVERY (crypto)
	// intent with no swap recorded. A naira sale's swap that mines late lands
	// USDC in her own wallet, so freeing the payment is harmless; a delivery's
	// pays the MERCHANT, so a Release before it mines lets a second payer pay too.
	// The app persists the swap hash before it broadcasts and re-reports it on
	// resume, which is what closes the gap; this grace outlasts a quote's
	// validity plus mempool time (30 min). RESIDUAL RISK: a swap signed, never
	// reported (app killed between broadcast and report, hash never persisted)
	// and mined after this grace still pays the merchant a second time.
	payDeliveryUnsignedGrace = time.Hour
	// payLandedNoOrderGrace frees a swap that landed but whose payout order
	// she never opened; she keeps the USDC.
	payLandedNoOrderGrace = 2 * time.Hour
	// payUnknownSwapGrace frees a swap LiFi has no record of.
	payUnknownSwapGrace = 30 * time.Minute
	// payPendingSwapMax is how long a swap may stay pending before support
	// takes over: it may still deliver, so the payment is failed, not released.
	payPendingSwapMax = 24 * time.Hour
	// payPollEveryTick is how long a sent swap is asked about on every tick
	// (30 s); past it, payPollDue thins the asking to about once in
	// payPollEvery. Each ask is a LiFi call, made in sequence for every swapped
	// payment, for up to payPendingSwapMax.
	payPollEveryTick = time.Hour
	payPollEvery     = 5 * time.Minute
	// payStuckWaitMax bounds every wait this sweep cannot resolve on its own — a
	// missing service, an unknown provider status, an order with no row — so none
	// is silent forever: each alerts once when it starts and the payment is failed
	// for support once it has gone on this long from the lock.
	payStuckWaitMax = 6 * time.Hour
)

// payPollDue reports whether a payment of this age, whose swap was sent, is
// asked about on this tick. Stateless: within the first payPollEveryTick every
// tick asks; after it, only the ticks that fall in the first minute of each
// payPollEvery counted from the payment's own lock — two ticks at the 30 s
// cadence, and payments spread out by when they were locked.
func payPollDue(age time.Duration) bool {
	if age <= payPollEveryTick {
		return true
	}
	return int64(age/time.Minute)%int64(payPollEvery/time.Minute) == 0
}

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
	// Orders first: settling a direct payment needs only our own table, so a
	// LiFi or RPC outage met on the intents that follow can never stall it.
	ctx = context.WithValue(ctx, payTickKey{}, &payTick{})
	for _, where := range []string{
		"paycrest_order_id IS NOT NULL",
		"paycrest_order_id IS NULL AND sell_intent_id IS NOT NULL",
	} {
		if err := s.sweep(ctx, batch, where, &errs); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

// payTickKey / payTick carry what one sweep has learned to the steps in it: once
// LiFi has failed to answer, the rest of the tick does not ask again (each ask
// waits out a timeout, and they are made one after another).
type (
	payTickKey struct{}
	payTick    struct{ lifiDown bool }
)

// errPayLiFiSkipped is what a step returns for a LiFi read it did not make.
var errPayLiFiSkipped = errors.New("lifi already failed this tick")

// swapStatus is intents.SwapStatus for the sweep: a failure marks LiFi down for
// the rest of the tick, and later reads are skipped without a call.
func (s *PayPaymentService) swapStatus(ctx context.Context, id uuid.UUID) (*LiFiTransferStatus, error) {
	t, _ := ctx.Value(payTickKey{}).(*payTick)
	if t != nil && t.lifiDown {
		return nil, errPayLiFiSkipped
	}
	st, err := s.intents.SwapStatus(ctx, id)
	if err != nil && t != nil {
		t.lifiDown = true
	}
	return st, err
}

// sweep walks the processing payments matching where in id order, batch at a
// time, so a long run of payments waiting on a customer can never starve a newer
// one. A payment whose step fails is logged and appended to errs.
func (s *PayPaymentService) sweep(ctx context.Context, batch int, where string, errs *[]error) error {
	var after *uuid.UUID
	for {
		q := s.db.WithContext(ctx).Model(&models.PayPayment{}).
			Where("status = ? AND "+where, models.PayStatusProcessing)
		if after != nil {
			q = q.Where("id > ?", *after)
		}
		var ids []uuid.UUID
		if err := q.Order("id").Limit(batch).Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			switch err := s.reconcileOne(ctx, id); {
			case err == nil, errors.Is(err, errPayLiFiSkipped):
			default:
				log.Printf("[tsa-pay] reconcile payment %s: %v", id, err)
				*errs = append(*errs, err)
			}
		}
		if len(ids) < batch {
			return nil
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
			return s.waitOrFail(ctx, p, "order_row_missing", "order", *p.PaycrestOrderID, "the payment's order can't be found — support is reviewing it")
		}
		return err
	}
	switch o.Status {
	case models.PaycrestStatusSettled:
		return s.succeedFrom(ctx, p, o.ID.String())
	case models.PaycrestStatusRefunded:
		return s.releaseFrom(ctx, p, "the payout was refunded to the customer — they can try again")
	case models.PaycrestStatusExpired:
		deposit, err := s.expiredOrderDeposit(ctx, &o)
		if err != nil {
			return err
		}
		switch deposit {
		case depositSeen:
			payAlert("order_expired_after_deposit", p, "order", o.ID)
			return s.failFrom(ctx, p, "the order expired after a deposit may have been sent — support is reviewing it")
		case depositUnknown:
			// Nothing says money arrived, and nothing could rule it out: a
			// released payment lets a second payer pay while the first's funds
			// sit stranded. Wait, and say so.
			return s.waitOrFail(ctx, p, "expired_order_unverified", "order", o.ID,
				"we couldn't confirm whether a deposit reached the order — support is reviewing it")
		}
		return s.releaseFrom(ctx, p, "the order lapsed before payment")
	case models.PaycrestStatusUnmatched:
		payAlert("order_unmatched", p, "order", o.ID)
		return s.failFrom(ctx, p, "the deposit couldn't be matched — support is reviewing it")
	case models.PaycrestStatusFailed:
		if payAmountPositive(o.AmountPaid) {
			payAlert("order_failed_with_deposit", p, "order", o.ID)
		}
		return s.failFrom(ctx, p, "the payment wasn't completed")
	case models.PaycrestStatusCancelled:
		// Cancelled on OUR side only: Paycrest's copy may still take a deposit,
		// so the payment is never handed back.
		return s.failFrom(ctx, p, "the payment wasn't completed")
	case models.PaycrestStatusAwaitingDeposit, models.PaycrestStatusPending, models.PaycrestStatusSettling:
		return nil
	default:
		return s.waitOrFail(ctx, p, "order_status_unknown", "order", o.ID, "the payment's order is in a state we don't recognise — support is reviewing it")
	}
}

// waitOrFail is the ending for a state this sweep can only wait on: it alerts
// once, and fails the payment for support once the wait has run payStuckWaitMax
// from the lock, so nothing waits in silence forever. It never releases: what it
// waits on may still move money.
func (s *PayPaymentService) waitOrFail(ctx context.Context, p *models.PayPayment, name, attempt string, attemptID uuid.UUID, reason string) error {
	s.alertOnce(name, p, attempt, attemptID)
	if s.now().Sub(payLockedAt(p)) <= payStuckWaitMax {
		return nil
	}
	payAlert(name+"_timeout", p, attempt, attemptID)
	return s.failFrom(ctx, p, reason)
}

// depositEvidence is what is known of whether the customer's money reached an
// order that expired.
type depositEvidence int

const (
	depositNone    depositEvidence = iota // every place it could show was read, and it is not there
	depositSeen                           // some sign of money
	depositUnknown                        // nothing seen, but the chain could not be read
)

// expiredOrderDeposit reads every sign that money reached an expired order, in
// order of cost: what Paycrest and the customer say (orderShowsDeposit), then the
// chain itself — the deposit address's token balance. Paycrest reported the
// 2026-09-03 order as expired with amountPaid=0 after a full payment, and she had
// not tapped "I've sent it", so the sources above can all read empty while the
// money sits at the address. With no reader wired, or one that fails, the answer
// is depositUnknown, never depositNone.
func (s *PayPaymentService) expiredOrderDeposit(ctx context.Context, o *models.PaycrestOrderRecord) (depositEvidence, error) {
	seen, err := s.orderShowsDeposit(ctx, o)
	if err != nil {
		return 0, err
	}
	if seen {
		return depositSeen, nil
	}
	if strings.TrimSpace(o.DepositAddress) == "" {
		return depositNone, nil // nowhere to have sent it
	}
	if s.deposits == nil {
		return depositUnknown, nil
	}
	bal, err := s.deposits.DepositBalance(o.ChainKey, o.Token, o.DepositAddress)
	if err != nil {
		log.Printf("[tsa-pay] cannot read the deposit address of order %s: %v", o.ID, err)
		return depositUnknown, nil
	}
	if bal != nil && bal.Sign() > 0 {
		return depositSeen, nil
	}
	return depositNone, nil
}

// orderShowsDeposit reports any sign in our own records that the customer's
// money reached, or may have reached, the order: a paid amount, a deposit
// transaction Paycrest reported, her "I've sent it" claim, or a deposit we
// relayed for her. An unreadable sign is an error, never "none".
func (s *PayPaymentService) orderShowsDeposit(ctx context.Context, o *models.PaycrestOrderRecord) (bool, error) {
	if payAmountPositive(o.AmountPaid) || o.PaymentClaimedAt != nil || strings.TrimSpace(o.TxHash) != "" {
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
		return s.waitOrFail(ctx, p, "no_intent_service", "intent", *p.SellIntentID,
			"the payment's swap can't be followed right now — support is reviewing it")
	}
	var in models.RampSellIntent
	if err := s.db.WithContext(ctx).First(&in, "id = ?", *p.SellIntentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.waitOrFail(ctx, p, "intent_row_missing", "intent", *p.SellIntentID,
				"the payment's swap can't be found — support is reviewing it")
		}
		return err
	}
	switch in.Status {
	case models.SellIntentOrdered:
		if in.PaycrestOrderID == nil {
			return s.waitOrFail(ctx, p, "ordered_without_order", "intent", in.ID,
				"the payment's payout order isn't recorded — support is reviewing it")
		}
		return s.handOverToOrder(ctx, p, *in.PaycrestOrderID)
	case models.SellIntentOrdering:
		// Her payout order is being opened right now — unless the request
		// opening it died holding the claim.
		if !sellClaimIsStale(&in, s.now()) {
			return nil
		}
		return s.reconcileStaleOrdering(ctx, p, &in)
	}
	facts := swapFacts{
		Delivery:     in.IsDelivery(),
		IntentStatus: in.Status,
		HasHash:      in.SwapTxHash != nil,
		Age:          s.now().Sub(payLockedAt(p)),
	}
	if !facts.HasHash {
		return s.applyVerdict(ctx, p, &in, swapVerdictOf(facts))
	}
	if !payPollDue(facts.Age) {
		return nil
	}
	if facts.Delivery {
		return s.reconcileDelivery(ctx, p, &in, facts)
	}
	return s.decideBySwapStatus(ctx, p, &in, facts)
}

// reconcileStaleOrdering follows an intent whose ordering claim is past the
// window OpenOrder itself treats as dead. The holder may have opened her payout
// order and died before recording it, or its short order may not have been
// cancelled: an order that pays this merchant may exist, so this never releases
// the payment and never ends the intent.
//
//   - An order marked as this payment's, opened by her since the lock (and not
//     one cancelled with its deposit address withdrawn), is adopted: the intent is
//     recorded as ordered with it, exactly as OpenOrder would have, and the
//     payment follows the order from there.
//   - With none, the claim is dead with nothing to show for it: the payment is
//     failed for support with an alert.
func (s *PayPaymentService) reconcileStaleOrdering(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent) error {
	var o models.PaycrestOrderRecord
	err := s.db.WithContext(ctx).
		Where("pay_payment_id = ? AND user_id = ? AND created_at >= ?", p.ID, in.UserID, payLockedAt(p)).
		Where("NOT (status = ? AND deposit_address = '')", models.PaycrestStatusCancelled).
		Order("created_at DESC").First(&o).Error
	switch {
	case err == nil:
		res := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
			Where("id = ? AND status = ? AND paycrest_order_id IS NULL", in.ID, models.SellIntentOrdering).
			Updates(models.WithBankDetailsCleared(map[string]any{
				"status": models.SellIntentOrdered, "paycrest_order_id": o.ID, "updated_at": s.now()}))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // it moved on while we looked; the next tick follows it
		}
		payAlert("ordering_order_adopted", p, "order", o.ID)
		return s.handOverToOrder(ctx, p, o.ID)
	case errors.Is(err, gorm.ErrRecordNotFound):
		payAlert("ordering_claim_stale", p, "intent", in.ID)
		return s.failFrom(ctx, p, "the payout order was never opened — support is reviewing it")
	}
	return err
}

// reconcileDelivery settles a crypto payment (its intent delivers a token to the
// merchant's wallet). The chain, not LiFi, says whether the merchant was paid in
// full: ConfirmDelivery proves it, and only that makes the payment succeed.
// Anything short of a proof is decided by swapVerdictOf from LiFi's status.
func (s *PayPaymentService) reconcileDelivery(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent, facts swapFacts) error {
	if in.Status == models.SellIntentSwapped || in.Status == models.SellIntentDelivered {
		destTx, err := s.intents.ConfirmDelivery(ctx, in.ID)
		switch {
		case err == nil:
			return s.succeedFrom(ctx, p, destTx)
		case errors.Is(err, ErrSwapNotLanded):
			// Not delivered yet, or never going to be: decided from LiFi's status —
			// the one ConfirmDelivery just read, when it says which.
			var nl *SwapNotLandedError
			if errors.As(err, &nl) {
				return s.decideWithStatus(ctx, p, in, facts, nl.Status)
			}
		case errors.Is(err, ErrSwapMismatch):
			facts.Mismatch = true
			return s.applyVerdict(ctx, p, in, swapVerdictOf(facts))
		case errors.Is(err, ErrSellUnavailable):
			// No verifier for this chain (yet) is not a verdict on the payment.
			return s.waitOrFail(ctx, p, "delivery_unverifiable", "intent", in.ID,
				"the transfer can't be confirmed right now — support is reviewing it")
		default:
			return err
		}
	}
	return s.decideBySwapStatus(ctx, p, in, facts)
}

// decideBySwapStatus reads what LiFi says became of the payment's swap and
// decides it (decideWithStatus).
func (s *PayPaymentService) decideBySwapStatus(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent, facts swapFacts) error {
	st, err := s.swapStatus(ctx, in.ID)
	if err != nil {
		return err
	}
	return s.decideWithStatus(ctx, p, in, facts, st)
}

// decideWithStatus decides a payment whose swap was sent from a LiFi status
// already read — nil when LiFi has no record of the swap, which is an answer,
// not a reason to ask again.
func (s *PayPaymentService) decideWithStatus(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent, facts swapFacts, st *LiFiTransferStatus) error {
	if st != nil {
		facts.LiFi, facts.Sub = st.Status, st.Substatus
	}
	return s.applyVerdict(ctx, p, in, swapVerdictOf(facts))
}

// applyVerdict is the one place a swap verdict is carried out, through the
// guarded transitions: when the verdict says so the intent is ended first (and
// nothing follows if it could not be), then the payment is released, or failed
// with the alert for a human.
func (s *PayPaymentService) applyVerdict(ctx context.Context, p *models.PayPayment, in *models.RampSellIntent, v swapVerdict) error {
	var act func() error
	switch v.kind {
	case verdictRelease:
		act = func() error { return s.releaseFrom(ctx, p, v.reason) }
	case verdictFail:
		act = func() error {
			if v.alert != "" {
				payAlert(v.alert, p, "intent", in.ID)
			}
			return s.failFrom(ctx, p, v.reason)
		}
	default:
		return nil
	}
	if v.end {
		return s.endIntentThen(ctx, in, act)
	}
	return act()
}

// endIntentThen ends the payment's sell intent and, only if it is ended, runs
// next. The abandon is guarded on the swap this sweep read (in.SwapTxHash), so a
// swap she signs in between is never abandoned: the write matches nothing, and
// next does not run. An intent that moved on to ordering or ordered while we
// looked is left alone too (the next tick follows it); one already failed is
// already ended.
func (s *PayPaymentService) endIntentThen(ctx context.Context, in *models.RampSellIntent, next func() error) error {
	if in.Status != models.SellIntentFailed {
		err := s.intents.AbandonForPay(ctx, in.ID, in.SwapTxHash)
		if errors.Is(err, ErrSellIntentState) {
			var cur models.RampSellIntent
			if rerr := s.db.WithContext(ctx).First(&cur, "id = ?", in.ID).Error; rerr != nil {
				return rerr
			}
			if cur.Status != models.SellIntentFailed {
				if in.SwapTxHash == nil && cur.SwapTxHash != nil {
					// Her swap was sent after we read the intent. The payment
					// stays with the swap now: the next tick decides it from
					// LiFi's answer, as for any sent swap.
					pid := uuid.Nil // a pay intent always names its payment
					if in.PayPaymentID != nil {
						pid = *in.PayPaymentID
					}
					payAlertf("swap_sent_during_abandon", pid, "intent="+in.ID.String())
				}
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

// alertOnce is payAlert at most once per process for one attempt and name: for
// a state that lasts, so the log carries one line rather than one a tick.
func (s *PayPaymentService) alertOnce(name string, p *models.PayPayment, attempt string, attemptID uuid.UUID) {
	if _, seen := s.alerted.LoadOrStore(name+"/"+attemptID.String(), struct{}{}); !seen {
		payAlert(name, p, attempt, attemptID)
	}
}

// payAlertf is the one writer of the log line an operator alerts on for a payment
// that needs a human, in the codebase's `ALERT name_total=1` form:
// `[tsa-pay] ALERT pay_<name>_total=1 payment=<id> <fields...>`.
func payAlertf(name string, payment uuid.UUID, fields ...string) {
	line := fmt.Sprintf("[tsa-pay] ALERT pay_%s_total=1 payment=%s", name, payment)
	for _, f := range fields {
		line += " " + f
	}
	log.Print(line)
}

// payAlert is payAlertf for a payment and the attempt (order or intent) it is
// about.
func payAlert(name string, p *models.PayPayment, attempt string, attemptID uuid.UUID) {
	payAlertf(name, p.ID, "merchant="+p.MerchantID.String(), attempt+"="+attemptID.String())
}
