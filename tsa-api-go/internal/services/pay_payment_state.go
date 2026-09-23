package services

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Lock moves pending → processing for user, stamping locked_at, and is the
// single entry point that decides whether a payment may be paid right now.
// fresh is false when user already held the lock (a resume — the app
// re-opened the checkout screen, or came back from a failed attempt) rather
// than a brand-new start; a resume succeeds even past expires_at, since a
// payment already in flight must never be pulled out from under its payer by
// the clock.
//
// Every OTHER caller (a fresh lock) is refused when: the payment is not
// pending (another payer already holds it, or it finished/expired), there is
// less than payMinTimeToStart left, the merchant is suspended, the mode is
// live but live payments are disabled, or (live only) the payer is not
// KYC-verified.
func (s *PayPaymentService) Lock(ctx context.Context, user *models.User, id uuid.UUID, liveEnabled bool) (*models.PayPayment, bool, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}

	if p.Status == models.PayStatusProcessing && p.PayerUserID != nil && *p.PayerUserID == user.ID {
		return p, false, nil
	}
	if p.Status != models.PayStatusPending {
		return nil, false, ErrPayNotPayable
	}

	now := s.now()
	if now.Add(payMinTimeToStart).After(p.ExpiresAt) {
		return nil, false, ErrPayNotPayable
	}

	m, err := s.merchants.Get(ctx, p.MerchantID)
	if err != nil {
		return nil, false, err
	}
	if m.Status != models.PayMerchantActive {
		return nil, false, ErrPayNotPayable
	}
	if p.Mode == models.PayModeLive {
		if !liveEnabled {
			return nil, false, ErrPayNotPayable
		}
		if err := requireVerifiedForMoneyIn(ctx, s.db, user.ID); err != nil {
			return nil, false, err
		}
	}

	res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("id = ? AND status = ?", id, models.PayStatusPending).
		Updates(map[string]any{
			"status":        models.PayStatusProcessing,
			"payer_user_id": user.ID,
			"locked_at":     &now,
		})
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, false, ErrPayNotPayable
	}

	locked, err := s.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return locked, true, nil
}

// Release moves processing → pending and clears the payer, locked_at and
// both attempt links in one guarded UPDATE, so another customer can pay a
// payment whose attempt died. See the Global Constraints for when a caller
// may call this.
func (s *PayPaymentService) Release(ctx context.Context, id uuid.UUID, reason string) error {
	return s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusProcessing}},
		map[string]any{
			"status":            models.PayStatusPending,
			"payer_user_id":     nil,
			"locked_at":         nil,
			"sell_intent_id":    nil,
			"paycrest_order_id": nil,
			"failure_reason":    reason,
		}, "")
}

// LinkOrder attaches a Paycrest order as the payment's one attempt.
func (s *PayPaymentService) LinkOrder(ctx context.Context, id, orderID uuid.UUID) error {
	return s.linkAttempt(ctx, id, "paycrest_order_id", orderID)
}

// LinkIntent attaches a sell intent as the payment's one attempt.
func (s *PayPaymentService) LinkIntent(ctx context.Context, id, intentID uuid.UUID) error {
	return s.linkAttempt(ctx, id, "sell_intent_id", intentID)
}

// linkAttempt guards on the payment being processing with NEITHER attempt
// column set yet — a processing payment carries at most one attempt at a
// time, so a second start by the same or another caller is refused rather
// than silently replacing the first.
func (s *PayPaymentService) linkAttempt(ctx context.Context, id uuid.UUID, column string, val uuid.UUID) error {
	res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("id = ? AND status = ? AND paycrest_order_id IS NULL AND sell_intent_id IS NULL", id, models.PayStatusProcessing).
		Update(column, val)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPayInProgress
	}
	return nil
}

// Succeed moves processing → succeeded and queues the payment.succeeded
// webhook in the same transaction as the state change.
func (s *PayPaymentService) Succeed(ctx context.Context, id uuid.UUID, settlementRef string) error {
	now := s.now()
	return s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusProcessing}},
		map[string]any{
			"status":         models.PayStatusSucceeded,
			"settlement_ref": settlementRef,
			"succeeded_at":   &now,
		}, models.PayEventSucceeded)
}

// Fail moves processing → failed and queues the payment.failed webhook in
// the same transaction as the state change.
func (s *PayPaymentService) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	return s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusProcessing}},
		map[string]any{
			"status":         models.PayStatusFailed,
			"failure_reason": reason,
		}, models.PayEventFailed)
}

// Cancel moves a merchant's own pending payment straight to expired — the
// merchant changed its mind before any customer started paying — and queues
// the payment.expired webhook in the same transaction.
func (s *PayPaymentService) Cancel(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) error {
	return s.transition(ctx, id, "id = ? AND merchant_id = ? AND mode = ? AND status IN ?",
		[]any{id, merchantID, mode, []string{models.PayStatusPending}},
		map[string]any{
			"status":         models.PayStatusExpired,
			"failure_reason": "cancelled by the merchant",
		}, models.PayEventExpired)
}

// ExpirePendingForMerchant expires every pending payment of merchantID — the
// suspension side effect: a suspended merchant can no longer be paid, so
// nothing should sit around pretending it still can be.
func (s *PayPaymentService) ExpirePendingForMerchant(ctx context.Context, merchantID uuid.UUID) (int, error) {
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("merchant_id = ? AND status = ?", merchantID, models.PayStatusPending).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	return s.expireEach(ctx, ids, "merchant suspended")
}

// ExpireDue expires pending payments whose expires_at has passed, oldest
// first, up to limit — the periodic expiry sweep's unit of work. Selecting
// ids then expiring each through its own guarded transition (rather than an
// UPDATE ... LIMIT) is what keeps this portable across SQLite and Postgres.
func (s *PayPaymentService) ExpireDue(ctx context.Context, limit int) (int, error) {
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("status = ? AND expires_at < ?", models.PayStatusPending, s.now()).
		Order("expires_at").
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	return s.expireEach(ctx, ids, "expired")
}

// expireEach transitions each id pending → expired through its own guarded
// UPDATE. A row already moved on by someone else between the select and now
// (ErrPayTransition) is simply skipped, not an error for the whole sweep.
func (s *PayPaymentService) expireEach(ctx context.Context, ids []uuid.UUID, reason string) (int, error) {
	n := 0
	for _, id := range ids {
		err := s.transition(ctx, id, "id = ? AND status IN ?", []any{id, []string{models.PayStatusPending}},
			map[string]any{
				"status":         models.PayStatusExpired,
				"failure_reason": reason,
			}, models.PayEventExpired)
		switch {
		case err == nil:
			n++
		case errors.Is(err, ErrPayTransition):
			// already moved on; not this sweep's problem
		default:
			return n, err
		}
	}
	return n, nil
}

// ReconcileTick is the periodic reconciler's unit of work: the expiry sweep,
// the stuck-test-payment release, and settlement of every in-flight payment
// (reconcileProcessing). Each step runs whatever the others do, so a failing
// expiry sweep never holds up settling a payment; the errors are joined for
// the worker's failure-streak alert.
func (s *PayPaymentService) ReconcileTick(ctx context.Context) error {
	var errs []error
	if _, err := s.ExpireDue(ctx, 100); err != nil {
		errs = append(errs, err)
	}
	if _, err := s.ReleaseStuckTestPayments(ctx, payStuckTestReleaseAfter, 100); err != nil {
		errs = append(errs, err)
	}
	if err := s.reconcileProcessing(ctx, 50); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

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
		err := s.Release(ctx, p.ID, reason)
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
	var in models.RampSellIntent
	switch err := s.db.WithContext(ctx).Where("pay_payment_id = ? AND status <> ?", p.ID, models.SellIntentFailed).
		Order("created_at DESC").First(&in).Error; {
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

// SimulateTestOutcome is the in-app "simulate" button for a test-mode
// payment: locks it for user (refusing exactly as a real attempt would —
// another payer, expiry, a suspended merchant) and then resolves it. Only the
// merchant's own user or an admin may simulate: the outcome fires signed
// webhooks at the merchant's test URL, so anyone else holding the payment id
// must not be able to trigger them.
func (s *PayPaymentService) SimulateTestOutcome(ctx context.Context, user *models.User, id uuid.UUID, approve bool) (*models.PayPayment, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Mode != models.PayModeTest {
		return nil, ErrPayNotPayable
	}
	if user == nil {
		return nil, ErrPayForbidden
	}
	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		m, err := s.merchants.Get(ctx, p.MerchantID)
		if err != nil {
			return nil, err
		}
		if m.UserID != user.ID {
			return nil, ErrPayForbidden
		}
	}
	_, fresh, err := s.Lock(ctx, user, id, false)
	if err != nil {
		return nil, err
	}
	if approve {
		err = s.Succeed(ctx, id, "test")
	} else {
		err = s.Fail(ctx, id, "declined in test mode")
	}
	if err != nil {
		// Lock and resolve are two statements: a failed resolve after a fresh
		// lock would strand the payment in processing, so hand it back
		// (best effort; ReconcileTick catches it if this fails too).
		if fresh {
			_ = s.Release(ctx, id, "test outcome failed to resolve")
		}
		return nil, err
	}
	return s.Get(ctx, id)
}

// transition performs one guarded state change — a single
// `UPDATE ... WHERE <where>` that only ever matches the payment's current
// legal predecessor state(s) — and, when event != "", the webhook delivery
// that transition owes, in one database transaction: a failure to write the
// delivery rolls the state change back with it. where/args must include
// `status IN ?` (every caller here follows `"id = ? AND status IN ?"` /
// `[]any{id, from}`, Cancel additionally scoping to merchant_id + mode).
func (s *PayPaymentService) transition(ctx context.Context, id uuid.UUID, where string, args []any, fields map[string]any, event string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.PayPayment{}).Where(where, args...).Updates(fields)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrPayTransition
		}
		if event == "" {
			return nil
		}
		var p models.PayPayment
		if err := tx.First(&p, "id = ?", id).Error; err != nil {
			return err
		}
		return s.enqueueWebhook(tx, &p, event)
	})
}

// enqueueWebhook inserts the delivery p's transition to event owes, inside
// tx — the same transaction as the state-changing UPDATE that called it, so
// a failure here (e.g. the deliveries table is unreachable) rolls the
// transition back too. A merchant with no webhook URL configured for p's
// mode is not queued at all — WebhookURLFor returns "".
func (s *PayPaymentService) enqueueWebhook(tx *gorm.DB, p *models.PayPayment, event string) error {
	var m models.PayMerchant
	if err := tx.First(&m, "id = ?", p.MerchantID).Error; err != nil {
		return err
	}
	url := m.WebhookURLFor(p.Mode)
	if url == "" {
		return nil
	}

	now := s.now()
	deliveryID := uuid.New()
	payload, err := json.Marshal(map[string]any{
		"id":       deliveryID,
		"type":     event,
		"livemode": p.Mode == models.PayModeLive,
		"created":  now.Unix(),
		"data":     s.View(p),
	})
	if err != nil {
		return err
	}

	delivery := models.PayWebhookDelivery{
		ID:            deliveryID,
		PaymentID:     p.ID,
		MerchantID:    p.MerchantID,
		Mode:          p.Mode,
		Event:         event,
		Payload:       string(payload),
		NextAttemptAt: &now,
	}
	return tx.Create(&delivery).Error
}
