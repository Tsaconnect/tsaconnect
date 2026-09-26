package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
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
// live but live payments are disabled (globally or for this merchant), or (live only) the payer is not
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
		// Both switches: a merchant switched off after a live payment was created
		// must not be paid through it.
		if !m.LiveAllowed(liveEnabled) {
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
			// A released payment keeps the reason it was released for; a new
			// attempt must not carry it into whatever it ends as.
			"failure_reason": "",
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

// payAttempt is the attempt a payment carried when a caller read it: the
// paycrest_order_id and sell_intent_id it saw, nil meaning "none". A transition
// made from that reading only matches while the payment still carries exactly
// that, so a reconciler that read, then made a slow provider call, cannot
// Release or Fail an attempt a new payer linked in the meantime.
//
// It also carries the lock the caller read (locked_at): a payment released and
// locked again by someone else between the read and the write is a different
// payment attempt altogether, whatever its links say.
type payAttempt struct {
	order, intent *uuid.UUID
	lockedAt      *time.Time
}

// attemptOf is the attempt p carries.
func attemptOf(p *models.PayPayment) *payAttempt {
	return &payAttempt{order: p.PaycrestOrderID, intent: p.SellIntentID, lockedAt: p.LockedAt}
}

// payGuard is what a transition requires of the row it updates: its status, the
// merchant and mode it belongs to (Cancel), where it came from (CancelLink), and
// — when attempt is set — the attempt it was decided from. It replaces
// free-form WHERE strings, so every guard is spelled the same way and none can
// be forgotten.
type payGuard struct {
	statuses   []string
	merchantID *uuid.UUID
	mode       string
	source     string
	attempt    *payAttempt
}

// payProcessing is the guard every settle/release/fail transition starts from.
func payProcessing(at *payAttempt) payGuard {
	return payGuard{statuses: []string{models.PayStatusProcessing}, attempt: at}
}

// scope narrows q to the rows g allows.
func (g payGuard) scope(q *gorm.DB, id uuid.UUID) *gorm.DB {
	q = q.Where("id = ? AND status IN ?", id, g.statuses)
	if g.merchantID != nil {
		q = q.Where("merchant_id = ?", *g.merchantID)
	}
	if g.mode != "" {
		q = q.Where("mode = ?", g.mode)
	}
	if g.source != "" {
		q = q.Where("source = ?", g.source)
	}
	if g.attempt != nil {
		q = attemptColumn(q, "paycrest_order_id", g.attempt.order)
		q = attemptColumn(q, "sell_intent_id", g.attempt.intent)
		if g.attempt.lockedAt == nil {
			q = q.Where("locked_at IS NULL")
		} else {
			q = q.Where("locked_at = ?", *g.attempt.lockedAt)
		}
	}
	return q
}

// attemptColumn requires col to equal v, or to be NULL when v is nil.
func attemptColumn(q *gorm.DB, col string, v *uuid.UUID) *gorm.DB {
	if v == nil {
		return q.Where(col + " IS NULL")
	}
	return q.Where(col+" = ?", *v)
}

// Release moves processing → pending and clears the payer, locked_at and
// both attempt links in one guarded UPDATE, so another customer can pay a
// payment whose attempt died. See the Global Constraints for when a caller
// may call this. Guarded on status only; a caller acting on a reading of the
// payment uses releaseFrom.
func (s *PayPaymentService) Release(ctx context.Context, id uuid.UUID, reason string) error {
	return s.release(ctx, id, nil, reason)
}

// releaseFrom is Release, but only while the payment still carries the attempt
// p was read with.
func (s *PayPaymentService) releaseFrom(ctx context.Context, p *models.PayPayment, reason string) error {
	return s.release(ctx, p.ID, attemptOf(p), reason)
}

func (s *PayPaymentService) release(ctx context.Context, id uuid.UUID, at *payAttempt, reason string) error {
	return s.transition(ctx, id, payProcessing(at),
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
	return s.succeed(ctx, id, nil, settlementRef)
}

// succeedFrom is Succeed, guarded on the attempt p was read with.
func (s *PayPaymentService) succeedFrom(ctx context.Context, p *models.PayPayment, settlementRef string) error {
	return s.succeed(ctx, p.ID, attemptOf(p), settlementRef)
}

func (s *PayPaymentService) succeed(ctx context.Context, id uuid.UUID, at *payAttempt, settlementRef string) error {
	now := s.now()
	err := s.transition(ctx, id, payProcessing(at),
		map[string]any{
			"status":         models.PayStatusSucceeded,
			"settlement_ref": settlementRef,
			"succeeded_at":   &now,
			// A payment released and retried succeeds with no memory of why
			// the earlier attempt was released.
			"failure_reason": "",
		}, models.PayEventSucceeded)
	if err == nil {
		s.announceReceived(ctx, id)
	}
	return err
}

// announceReceived tells the merchant's owner that payment id came in: every
// source, live and test. Every success — Succeed, and the reconciler's settle —
// comes through succeed, and this runs only once its guarded transition has
// committed; a second settle of the same payment finds it no longer processing
// and never reaches here, so each payment is announced once. The payment has
// already succeeded, so a failed read here is logged, never returned.
func (s *PayPaymentService) announceReceived(ctx context.Context, id uuid.UUID) {
	if s.events == nil {
		return
	}
	p, err := s.Get(ctx, id)
	if err != nil {
		log.Printf("[tsa-pay] payment received alert for %s: %v", id, err)
		return
	}
	m, err := s.merchants.Get(ctx, p.MerchantID)
	if err != nil {
		log.Printf("[tsa-pay] payment received alert for %s: %v", id, err)
		return
	}
	who := "A customer"
	switch payer, err := s.Payer(ctx, p); {
	case err != nil:
		log.Printf("[tsa-pay] payment received alert for %s: payer: %v", id, err)
	case payer != nil:
		who = payer.Name
	}
	// What the merchant receives, which is the price unless they take part of
	// the fee on themselves.
	title := "₦" + koboAsNairaText(p.MerchantNetKobo) + " received"
	// What it was for: the description, or — for an open-link payment whose
	// customer left no note — the link's label.
	what := p.Description
	if what == "" && p.OpenLinkID != nil {
		if labels, err := s.openLinkLabels(ctx, []uuid.UUID{*p.OpenLinkID}); err == nil {
			what = labels[*p.OpenLinkID]
		}
	}
	message := who + " paid " + m.BusinessName
	if what != "" {
		message += " · " + what
	}
	if p.Mode == models.PayModeTest {
		title = "Test: " + title // no real money moved
	}
	s.events.Publish(events.Event{Type: events.PayPaymentReceived, UserID: m.UserID, Title: title, Message: message,
		Data: map[string]any{"paymentId": p.ID.String(), "merchantId": m.ID.String(), "mode": p.Mode, "source": p.Source}})
}

// Fail moves processing → failed and queues the payment.failed webhook in
// the same transaction as the state change.
func (s *PayPaymentService) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	return s.fail(ctx, id, nil, reason)
}

// failFrom is Fail, guarded on the attempt p was read with.
func (s *PayPaymentService) failFrom(ctx context.Context, p *models.PayPayment, reason string) error {
	return s.fail(ctx, p.ID, attemptOf(p), reason)
}

func (s *PayPaymentService) fail(ctx context.Context, id uuid.UUID, at *payAttempt, reason string) error {
	return s.transition(ctx, id, payProcessing(at),
		map[string]any{
			"status":         models.PayStatusFailed,
			"failure_reason": reason,
		}, models.PayEventFailed)
}

// PayReasonMerchantCancelled is the failure_reason of a payment its merchant
// cancelled. Views read it back to tell a cancelled payment from one that
// simply ran out of time — both end expired.
const PayReasonMerchantCancelled = "cancelled by the merchant"

// Cancel moves a merchant's own pending payment straight to expired — the
// merchant changed its mind before any customer started paying — and queues
// the payment.expired webhook in the same transaction.
func (s *PayPaymentService) Cancel(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) error {
	return s.cancel(ctx, id, payGuard{statuses: []string{models.PayStatusPending}, merchantID: &merchantID, mode: mode})
}

// CancelLink is Cancel for the owner's own payment links only. A payment the
// merchant's server created belongs to that integration — it may be the
// checkout a customer is on right now — so only the server, with its API key,
// calls it off.
func (s *PayPaymentService) CancelLink(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) error {
	return s.cancel(ctx, id, payGuard{statuses: []string{models.PayStatusPending}, merchantID: &merchantID, mode: mode,
		source: models.PaySourceLink})
}

func (s *PayPaymentService) cancel(ctx context.Context, id uuid.UUID, g payGuard) error {
	return s.transition(ctx, id, g,
		map[string]any{
			"status":         models.PayStatusExpired,
			"failure_reason": PayReasonMerchantCancelled,
		}, models.PayEventExpired)
}

// ExpirePendingForMerchant expires the pending payments of merchantID — the
// side effect of a suspension (every mode) or of switching live payments off
// (live only): a merchant that can no longer be paid that way should not leave
// payments sitting around pretending it still can be.
//
// mode narrows it to one mode's payments ("" is every mode) and reason is the
// failure_reason each one ends with. Only pending payments are touched: one a
// payer is already paying (processing) or that has finished is left alone.
func (s *PayPaymentService) ExpirePendingForMerchant(ctx context.Context, merchantID uuid.UUID, mode, reason string) (int, error) {
	var ids []uuid.UUID
	q := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("merchant_id = ? AND status = ?", merchantID, models.PayStatusPending)
	if mode != "" {
		q = q.Where("mode = ?", mode)
	}
	if err := q.Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	return s.expireEach(ctx, ids, reason)
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
		err := s.transition(ctx, id, payGuard{statuses: []string{models.PayStatusPending}},
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

// payReconcileLockKey is the Postgres advisory lock one instance holds while it
// runs a reconcile tick ("TSAPAYC").
const payReconcileLockKey int64 = 0x54534150415943

// payReconcileSkipAlertAfter is how many ticks in a row may be skipped because
// another instance holds the lock before it is ALERTed (and again every that
// many): at the 30 s cadence, five minutes. One instance running a tick is
// normal; nobody having run one for this long is payments not settling.
const payReconcileSkipAlertAfter = 10

// ReconcileTick is the periodic reconciler's unit of work: the expiry sweep,
// the stuck-test-payment release, and settlement of every in-flight payment
// (reconcileProcessing). Each step runs whatever the others do, so a failing
// expiry sweep never holds up settling a payment; the errors are joined for
// the worker's failure-streak alert.
//
// On Postgres one instance runs a tick at a time: a second instance (a deploy
// overlap, a scale-out) finds the advisory lock held and skips the tick, loudly
// (noteTick). The guarded transitions already refuse a stale write; this stops
// two instances making the same slow provider calls and racing each other at
// all. SQLite (the tests) has no such lock and always runs.
//
// The lock is transaction-scoped: a short-lived transaction does nothing but
// hold it, on its own connection, while the tick runs on others — so no LiFi or
// RPC call happens inside a DB transaction, and the lock's release is tied to
// that transaction's end (commit, rollback, a dropped connection, a cancelled
// context). A session lock would need an explicit unlock, and one that failed on
// a healthy pooled connection would silence every instance's tick for good.
func (s *PayPaymentService) ReconcileTick(ctx context.Context) error {
	if s.db.Dialector == nil || s.db.Dialector.Name() != "postgres" {
		return s.reconcileTick(ctx)
	}
	var tickErr error
	ran := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var got bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", payReconcileLockKey).Scan(&got).Error; err != nil {
			return err
		}
		if !got {
			return nil // another instance is reconciling
		}
		ran = true
		tickErr = s.reconcileTick(ctx)
		return nil
	})
	s.noteTick(ran)
	return errors.Join(tickErr, err)
}

// noteTick keeps the count of ticks skipped in a row: every skip is logged, the
// Nth (and every Nth after) is an ALERT, and a tick that ran resets the count.
func (s *PayPaymentService) noteTick(ran bool) {
	if ran {
		s.skipped.Store(0)
		return
	}
	n := s.skipped.Add(1)
	log.Printf("[tsa-pay] reconcile tick skipped: another instance holds the lock (consecutive=%d)", n)
	if n%payReconcileSkipAlertAfter == 0 {
		log.Printf("[tsa-pay] ALERT pay_reconcile_skipped_total=1 consecutive=%d", n)
	}
}

func (s *PayPaymentService) reconcileTick(ctx context.Context) error {
	s.pruneAlerted()
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
// `UPDATE ... WHERE <guard>` that only ever matches the payment's current
// legal predecessor (status, and the attempt when the caller read one, see
// payGuard) — and, when event != "", the webhook delivery that transition
// owes, in one database transaction: a failure to write the delivery rolls the
// state change back with it.
func (s *PayPaymentService) transition(ctx context.Context, id uuid.UUID, g payGuard, fields map[string]any, event string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.transitionIn(tx, id, g, fields, event)
	})
}

// transitionIn is transition inside a transaction the caller owns, for a change
// that must commit or roll back together with another write.
func (s *PayPaymentService) transitionIn(tx *gorm.DB, id uuid.UUID, g payGuard, fields map[string]any, event string) error {
	res := g.scope(tx.Model(&models.PayPayment{}), id).Updates(fields)
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
}

// enqueueWebhook inserts the delivery p's transition to event owes, inside
// tx — the same transaction as the state-changing UPDATE that called it, so
// a failure here (e.g. the deliveries table is unreachable) rolls the
// transition back too. A merchant with no webhook URL configured for p's
// mode is not queued at all — WebhookURLFor returns "" — and nor is an
// open-link payment nobody started paying.
func (s *PayPaymentService) enqueueWebhook(tx *gorm.DB, p *models.PayPayment, event string) error {
	// An open-link payment nobody went on to pay is one visitor entering an
	// amount; its merchant is not told when it lapses. (One that succeeds was
	// always locked first.)
	if p.Source == models.PaySourceOpen && p.LockedAt == nil {
		return nil
	}
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
