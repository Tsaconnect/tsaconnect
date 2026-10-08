package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
			// Set once, on the first lock ever, and never cleared by a release:
			// a payment someone tried stays tried (PayPayment.Untried).
			"attempted_at": gorm.Expr("COALESCE(attempted_at, ?)", now),
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
// and never reaches here. So each payment is announced at most once: the bus
// drops an event when its buffer is full, and nothing retries it. The payment
// has already succeeded, so a failed read here is logged, never returned.
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
	// The payer by first name and last initial: this text goes out by push and
	// email, and the merchant sees the full name in the dashboard.
	who := "A customer"
	switch payer, err := s.Payer(ctx, p); {
	case err != nil:
		log.Printf("[tsa-pay] payment received alert for %s: payer: %v", id, err)
	case payer != nil && payer.Name != payerDeletedName:
		who = payShortName(payer.Name)
	}
	// What the merchant receives, which is the price unless they take part of
	// the fee on themselves.
	title := "₦" + koboAsNairaText(p.MerchantNetKobo) + " received"
	message := who + " paid " + m.BusinessName
	switch text, kind, err := s.payPurpose(ctx, p); {
	case err != nil:
		log.Printf("[tsa-pay] payment received alert for %s: purpose: %v", id, err)
	case kind == payPurposeCustomerNote:
		// Words a customer typed, marked as theirs, with any link in them made
		// harmless: this goes out by push and email under our name.
		message += " · Customer's note: “" + text + "”"
	case kind != payPurposeNone:
		message += " · " + text
	}
	if p.Mode == models.PayModeTest {
		title = "Test: " + title // no real money moved
	}
	s.events.Publish(events.Event{Type: events.PayPaymentReceived, UserID: m.UserID, Title: title, Message: message,
		Data: map[string]any{"paymentId": p.ID.String(), "merchantId": m.ID.String(), "mode": p.Mode, "source": p.Source}})
}

// payPurposeKind says whose words a payment's purpose text is.
type payPurposeKind int

const (
	payPurposeNone         payPurposeKind = iota
	payPurposeMerchantText                // the merchant's description, on a link or an API payment
	payPurposeCustomerNote                // an open-link customer's own note, web addresses already removed
	payPurposeLinkLabel                   // the open link's label, when the customer left no note
)

// payPurpose is what a payment was for, and whose words they are — the one
// rule behind the merchant's "received" alert and (as payPublicPurpose) the
// public status page and receipt.
func (s *PayPaymentService) payPurpose(ctx context.Context, p *models.PayPayment) (string, payPurposeKind, error) {
	switch {
	case p.Source == models.PaySourceOpen && p.Description != "":
		return payWithoutLinks(p.Description), payPurposeCustomerNote, nil
	case p.Description != "":
		return p.Description, payPurposeMerchantText, nil
	case p.OpenLinkID != nil:
		labels, err := s.openLinkLabels(ctx, []uuid.UUID{*p.OpenLinkID})
		if err != nil || labels[*p.OpenLinkID] == "" {
			return "", payPurposeNone, err
		}
		return labels[*p.OpenLinkID], payPurposeLinkLabel, nil
	}
	return "", payPurposeNone, nil
}

// payPublicPurpose is payPurpose as anyone holding the payment's id or code
// sees it — the public status page and the receipt: never an API payment's
// description, which is the merchant's own (booking codes, order details).
func (s *PayPaymentService) payPublicPurpose(ctx context.Context, p *models.PayPayment) (string, payPurposeKind, error) {
	if p.Source == models.PaySourceAPI {
		return "", payPurposeNone, nil
	}
	return s.payPurpose(ctx, p)
}

// payNameEnds is the first and last words of a name; last is empty for a
// one-word name.
func payNameEnds(name string) (first, last string) {
	parts := strings.Fields(name)
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return parts[0], ""
	}
	return parts[0], parts[len(parts)-1]
}

// payShortName is a name as first name and last initial — "Ada Okafor" is
// "Ada O." — or the one word it is.
func payShortName(name string) string {
	first, last := payNameEnds(name)
	if last == "" {
		return first
	}
	r, _ := utf8.DecodeRuneInString(last)
	return first + " " + string(r) + "."
}

// payLinkInText is an http(s) address or a bare www. one.
var payLinkInText = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)

// payWithoutLinks is s with every web address in it replaced, so text a
// customer typed can't carry a link into a push or an email.
func payWithoutLinks(s string) string {
	return payLinkInText.ReplaceAllString(s, "[link removed]")
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
// the payment.expired webhook in the same transaction. It is the merchant
// API's, so it reaches only payments the merchant's server created: a link the
// owner sent, or a payment a customer started at an open link, is not the
// server's to call off.
func (s *PayPaymentService) Cancel(ctx context.Context, merchantID uuid.UUID, mode string, id uuid.UUID) error {
	return s.cancel(ctx, id, payGuard{statuses: []string{models.PayStatusPending}, merchantID: &merchantID, mode: mode,
		source: models.PaySourceAPI})
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

// payUntriedExpireBatch is how many untried open-link payments one sweep
// expires in its single UPDATE.
const payUntriedExpireBatch = 1000

// ExpireDue expires pending payments whose expires_at has passed — the
// periodic expiry sweep's unit of work — and returns how many it expired.
//
// Untried open-link payments go first, in bulk (expireUntried): there can be
// many, and they owe no webhook. The rest, oldest first up to limit, each go
// through their own guarded transition and queue their webhook. Selecting ids
// then expiring each (rather than an UPDATE ... LIMIT) is what keeps this
// portable across SQLite and Postgres.
func (s *PayPaymentService) ExpireDue(ctx context.Context, limit int) (int, error) {
	bulk, err := s.expireUntried(ctx)
	if err != nil {
		return bulk, err
	}
	var ids []uuid.UUID
	if err := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("status = ? AND expires_at < ?", models.PayStatusPending, s.now()).
		Where("NOT ("+models.PayUntriedOpenSQL+")").
		Order("expires_at").
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return bulk, err
	}
	n, err := s.expireEach(ctx, ids, "expired")
	return bulk + n, err
}

// expireUntried expires, in one UPDATE, up to payUntriedExpireBatch untried
// open-link payments past their expiry. The outer WHERE repeats the pending
// and untried conditions, so a payment locked between the subquery and the
// update is left to its payer. Nothing is queued: an untried payment owes its
// merchant no webhook (enqueueWebhook).
//
// The rows are kept. Deleting untried payments after some retention period
// would bound the table, but deleting payment rows at all is the owner's
// decision to make, not this sweep's.
func (s *PayPaymentService) expireUntried(ctx context.Context) (int, error) {
	now := s.now()
	due := s.db.Model(&models.PayPayment{}).Select("id").
		Where("status = ? AND expires_at < ? AND "+models.PayUntriedOpenSQL, models.PayStatusPending, now).
		Limit(payUntriedExpireBatch)
	res := s.db.WithContext(ctx).Model(&models.PayPayment{}).
		Where("id IN (?) AND status = ? AND "+models.PayUntriedOpenSQL, due, models.PayStatusPending).
		Updates(map[string]any{"status": models.PayStatusExpired, "failure_reason": "expired", "updated_at": now})
	return int(res.RowsAffected), res.Error
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
	return s.enqueueWebhookWith(tx, p, event, nil)
}

// enqueueWebhookWith is enqueueWebhook with extra top-level keys on the body,
// beside data — payment.refunded's `refund` (contract ruling: a top-level key).
func (s *PayPaymentService) enqueueWebhookWith(tx *gorm.DB, p *models.PayPayment, event string, extra map[string]any) error {
	// An untried open-link payment is one visitor entering an amount; its
	// merchant is not told when it lapses. (One that succeeds was always tried.)
	if p.Untried() {
		return nil
	}
	if err := requirePayWebhookURL(tx, p.MerchantID, p.Mode); errors.Is(err, ErrPayWebhookNoURL) {
		return nil
	} else if err != nil {
		return err
	}
	delivery, err := s.webhookDelivery(tx, p, event, extra)
	if err != nil {
		return err
	}
	return tx.Create(&delivery).Error
}

// requirePayWebhookURL loads the merchant and returns ErrPayWebhookNoURL when
// it has no webhook URL for mode: nothing is to be queued for it.
func requirePayWebhookURL(tx *gorm.DB, merchantID uuid.UUID, mode string) error {
	var m models.PayMerchant
	if err := tx.First(&m, "id = ?", merchantID).Error; err != nil {
		return err
	}
	if m.WebhookURLFor(mode) == "" {
		return ErrPayWebhookNoURL
	}
	return nil
}

// webhookDelivery is one event about p, as p stands now in tx, under a fresh
// delivery id: the id merchants dedupe on is also the body's "id". extra is
// top-level keys beside data (nil for none). Every webhook body is built here.
func (s *PayPaymentService) webhookDelivery(tx *gorm.DB, p *models.PayPayment, event string, extra map[string]any) (models.PayWebhookDelivery, error) {
	now := s.now()
	deliveryID := uuid.New()
	data, err := s.webhookData(tx, p)
	if err != nil {
		return models.PayWebhookDelivery{}, err
	}
	body := map[string]any{
		"id":       deliveryID,
		"type":     event,
		"livemode": p.Mode == models.PayModeLive,
		"created":  now.Unix(),
		"data":     data,
	}
	// Extra top-level keys beside data — payment.refunded's `refund`. The
	// envelope's own keys are never overwritten.
	for k, v := range extra {
		if _, taken := body[k]; !taken {
			body[k] = v
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return models.PayWebhookDelivery{}, err
	}
	return models.PayWebhookDelivery{
		ID:            deliveryID,
		PaymentID:     p.ID,
		MerchantID:    p.MerchantID,
		Mode:          p.Mode,
		Event:         event,
		Payload:       string(payload),
		NextAttemptAt: &now,
	}, nil
}

// webhookData is a webhook's data: the payment view with its refund totals as
// they stand in tx (so a refund confirmed in the same transaction counts).
// Without refunds wired nothing reads pay_refunds.
func (s *PayPaymentService) webhookData(tx *gorm.DB, p *models.PayPayment) (PayPaymentView, error) {
	view := s.View(p)
	if s.refundAgg != nil {
		aggs, err := payRefundAggregates(tx, []uuid.UUID{p.ID})
		if err != nil {
			return PayPaymentView{}, err
		}
		view.withRefunds(aggs[p.ID])
	}
	return view, nil
}

// redeliver queues d's event again as a new delivery of its payment as it is
// now, leaving d (already delivered) as it was. A receiver deduping on
// X-TSA-Delivery still processes it. ErrPayWebhookNoURL when the merchant has
// no webhook URL for the mode any more; ErrPayDeliveryNotFound when its payment
// is gone.
func (s *PayPaymentService) redeliver(ctx context.Context, d *models.PayWebhookDelivery) (models.PayWebhookDelivery, error) {
	var out models.PayWebhookDelivery
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p models.PayPayment
		err := tx.First(&p, "id = ?", d.PaymentID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPayDeliveryNotFound
		}
		if err != nil {
			return err
		}
		if err := requirePayWebhookURL(tx, p.MerchantID, p.Mode); err != nil {
			return err
		}
		extra, err := redeliveryExtra(d)
		if err != nil {
			return err
		}
		row, err := s.webhookDelivery(tx, &p, d.Event, extra)
		if err != nil {
			return err
		}
		out = row
		return tx.Create(&out).Error
	})
	return out, err
}

// redeliveryExtra is the top-level keys a resend of d must carry again beside
// data. A payment.refunded body's `refund` is copied from d's own payload: a
// confirmed refund never changes, so the copy is exact, and a receiver keyed on
// refund.id still finds it. Every other event has none.
func redeliveryExtra(d *models.PayWebhookDelivery) (map[string]any, error) {
	if d.Event != models.PayEventRefunded {
		return nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(d.Payload), &raw); err != nil {
		return nil, fmt.Errorf("resend %s: read the original payload: %w", d.ID, err)
	}
	refund, ok := raw["refund"]
	if !ok {
		return nil, nil
	}
	return map[string]any{"refund": refund}, nil
}
