package services

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Sweep drives every change the requests left unfinished: MCGP debits whose
// outcome was not known, stablecoin payments named but not yet proven, changes
// past their time, and fees a failed call left unsettled, unreturned or
// unrefunded. It runs whether or not new changes may start. Idempotent — every
// move is guarded on the status it read. Returns how many changes moved, and
// the first read error (so a sweep that cannot see its rows is reported as
// failing).
func (s *NameChangeService) Sweep(ctx context.Context) (int, error) {
	moved := 0
	for _, step := range []func(context.Context) (int, error){
		s.sweepPayingMCGP, s.sweepStablePayments, s.sweepClearedPayments, s.sweepExpired, s.sweepUnresolvedFees, s.sweepLateDebits, s.sweepAlerts,
	} {
		n, err := step(ctx)
		moved += n
		if err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// sweepPage is the next page of the changes q matches, walked with a cursor
// per step (key) so a backlog of rows that do not move is not re-read from its
// oldest every tick while the rest wait.
func (s *NameChangeService) sweepPage(ctx context.Context, key string, q *gorm.DB) ([]models.UserNameChange, error) {
	var rows []models.UserNameChange
	if err := s.scan.page(key, "", q.WithContext(ctx).Model(&models.UserNameChange{}), nameChangeSweepBatch).Find(&rows).Error; err != nil {
		return nil, err
	}
	if n := len(rows); n > 0 {
		s.scan.advance(key, n, nameChangeSweepBatch, rows[n-1].UpdatedAt, rows[n-1].ID)
	} else {
		s.scan.advance(key, 0, nameChangeSweepBatch, time.Time{}, uuid.Nil)
	}
	return rows, nil
}

// sweepPayingMCGP settles MCGP fees whose debit's fate was unknown when the
// request returned, from the contract's debit record.
func (s *NameChangeService) sweepPayingMCGP(ctx context.Context) (int, error) {
	if s.debits == nil || !s.debits.IsEnabled() {
		return 0, nil
	}
	paying, err := s.sweepPage(ctx, "paying-mcgp", s.db.Where("status = ? AND pay_method = ?", models.NameChangeStatusPaying, models.NameChangePayMCGP))
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range paying {
		ch := &paying[i]
		status, _, err := s.debits.ResolveDebiting(ctx, ch.ID)
		if errors.Is(err, ErrBillPayNotFound) {
			// No debit row: nothing was sent — unless the request that opened
			// the change is still about to send it, so give it time first.
			if s.now().Sub(ch.CreatedAt) < nameChangeDebitGrace {
				continue
			}
			status, err = models.BillPayStatusFailed, nil
		}
		if err != nil {
			log.Printf("[name-change] change=%s debit check failed: %v", ch.ID, err)
			continue
		}
		switch status {
		case models.BillPayStatusDebited:
			if s.debitLanded(ctx, ch) {
				moved++
			}
		case models.BillPayStatusFailed:
			if s.closeUnpaid(ctx, ch, "the debit never landed") {
				moved++
			}
		}
	}
	return moved, nil
}

// sweepStablePayments proves the stablecoin transactions users named but that
// were not proven yet — on changes still paying, and on changes that closed
// unpaid while their payment was on its way (found and refunded).
func (s *NameChangeService) sweepStablePayments(ctx context.Context) (int, error) {
	now := s.now()
	db := s.db.WithContext(ctx)
	named, err := s.sweepPage(ctx, "stable-named", s.db.Where("pay_method = ? AND pay_ref IS NOT NULL AND fee_status = ? AND (status = ? OR (status = ? AND completed_at > ?))",
		models.NameChangePayStablecoin, models.NameChangeFeeNone, models.NameChangeStatusPaying,
		models.NameChangeStatusCancelled, now.Add(-nameChangeLatePaymentWindow)))
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range named {
		if out, _, _ := s.settleStablePayment(ctx, &named[i]); out == stableProven {
			moved++
		}
	}

	// Paying changes out of time. One with no payment named closes; one whose
	// payment is still unproven waits for it (the proof above decides). A day
	// past the change's time, a payment the chain has never heard of is taken
	// as never sent: the change closes unpaid and the transaction is moved to
	// ClearedPayRef, still watched for the late-payment window and sent back
	// whole should it land after all. One the chain does know of, or that
	// cannot be read, is a human's to look at.
	res := db.Model(&models.UserNameChange{}).
		Where("status = ? AND pay_method = ? AND pay_ref IS NULL AND expires_at < ?", models.NameChangeStatusPaying, models.NameChangePayStablecoin, now).
		Updates(finished(map[string]any{"status": models.NameChangeStatusCancelled, "completed_at": &now,
			"review_note": "fee not paid: no payment was confirmed in time", "user_message": "This name change was closed because its fee wasn't paid."}))
	if res.Error != nil {
		return moved, res.Error
	}
	moved += int(res.RowsAffected)
	waiting, err := s.sweepPage(ctx, "stable-waiting", s.db.Where("status = ? AND pay_method = ? AND pay_ref IS NOT NULL AND fee_status = ? AND expires_at < ?",
		models.NameChangeStatusPaying, models.NameChangePayStablecoin, models.NameChangeFeeNone, now.Add(-nameChangeVanishedAfter)))
	if err != nil {
		return moved, err
	}
	for i := range waiting {
		ch := &waiting[i]
		if s.dropVanishedPayment(ctx, ch) {
			moved++
			continue
		}
		alertf("name-change-payment-unproven:"+ch.ID.String(),
			"[name-change] ALERT name_change_payment_unproven_total=1 change=%s chain=%s tx=%s — the named payment still can't be proven a day after the change expired; check the chain: it is dropped on its own once the chain no longer knows it, and cleared on its own if it mines without paying (POST /api/admin/name-changes/:id/clear-payment refuses while it may still pay)",
			ch.ID, ch.PayChain, *ch.PayRef)
	}
	return moved, nil
}

// dropVanishedPayment closes a paying change whose named transaction the chain
// definitely does not know — not merely unmined: a lagging or failing read
// answers an error and leaves it waiting.
func (s *NameChangeService) dropVanishedPayment(ctx context.Context, ch *models.UserNameChange) bool {
	if s.rail == nil || ch.PayRef == nil {
		return false
	}
	known, _, err := s.rail.TransactionKnown(ctx, ch.PayChain, *ch.PayRef)
	if err != nil {
		log.Printf("[name-change] change=%s could not look up its named payment %s: %v", ch.ID, *ch.PayRef, err)
		return false
	}
	if known {
		return false
	}
	if !s.clearUnprovenPayment(ctx, ch, "the named transaction "+*ch.PayRef+" was not found on-chain a day after the change expired", true, false) {
		return false
	}
	log.Printf("[name-change] change=%s named payment %s never reached %s; closed unpaid", ch.ID, *ch.PayRef, ch.PayChain)
	return true
}

// clearUnprovenPayment forgets a stablecoin change's named transaction that has
// not been proven (no fee recorded) and, if the change is still paying, closes
// it unpaid. Guarded on that very transaction and on no fee having been taken,
// so a payment proven in between is never dropped. The hash stays in the note.
//
// watch: the transaction may yet land (the chain does not know it, which a
// later broadcast could change), so it moves to ClearedPayRef and the sweep
// keeps looking for it through the late-payment window, sending it back if it
// lands — whole when byAdmin, else only from the minimum refund up. Otherwise
// it is refuted and remembered as such.
func (s *NameChangeService) clearUnprovenPayment(ctx context.Context, ch *models.UserNameChange, note string, watch, byAdmin bool) bool {
	if ch.PayRef == nil {
		return false
	}
	now := s.now()
	txHash := *ch.PayRef
	updates := map[string]any{"pay_ref": nil, "review_note": truncateRunes("payment cleared: "+note, 512)}
	if watch {
		updates["cleared_pay_ref"], updates["pay_ref_cleared_at"], updates["pay_ref_cleared_by_admin"] = txHash, &now, byAdmin
	}
	if ch.Status == models.NameChangeStatusPaying {
		updates["status"], updates["completed_at"] = models.NameChangeStatusCancelled, &now
		updates["user_message"] = "This name change was closed because its payment never reached the network. " +
			"If it does reach the network later, it will be sent back to your wallet automatically."
	}
	guarded := func(q *gorm.DB) *gorm.DB {
		return q.Where("id = ? AND status = ? AND pay_method = ? AND pay_ref = ? AND fee_status = ? AND cleared_pay_ref IS NULL",
			ch.ID, ch.Status, models.NameChangePayStablecoin, txHash, models.NameChangeFeeNone)
	}
	var moved bool
	var err error
	if watch {
		res := guarded(s.db.WithContext(ctx).Model(&models.UserNameChange{})).Updates(finished(updates))
		moved, err = res.RowsAffected == 1, res.Error
	} else {
		moved, err = s.updateRefuting(ctx, ch.ID, txHash, guarded, finished(updates))
	}
	if err != nil {
		log.Printf("[name-change] change=%s could not clear its unproven payment: %v", ch.ID, err)
		return false
	}
	return moved
}

// sweepClearedPayments looks again, through the late-payment window, at the
// transactions changes stopped waiting for, and returns any that landed.
func (s *NameChangeService) sweepClearedPayments(ctx context.Context) (int, error) {
	cleared, err := s.sweepPage(ctx, "stable-cleared", s.db.Where("pay_method = ? AND cleared_pay_ref IS NOT NULL AND fee_status = ? AND pay_ref_cleared_at > ?",
		models.NameChangePayStablecoin, models.NameChangeFeeNone, s.now().Add(-nameChangeLatePaymentWindow)))
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range cleared {
		if s.settleClearedPayment(ctx, &cleared[i]) == stableProven {
			moved++
		}
	}
	return moved, nil
}

// sweepExpired lapses changes out of time. One with a session still open gets a
// grace period to hear back from Didit. An unused change's fee is returned —
// unless the user has already had their allowance of returns, when it is kept.
func (s *NameChangeService) sweepExpired(ctx context.Context) (int, error) {
	now := s.now()
	expired, err := s.sweepPage(ctx, "expired", s.db.Where("((status IN ? AND expires_at < ?) OR (status = ? AND expires_at < ?))",
		[]string{models.NameChangeStatusPaid, models.NameChangeStatusDeclined}, now,
		models.NameChangeStatusVerifying, now.Add(-nameChangeVerifyingGrace)))
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range expired {
		ch := &expired[i]
		updates := map[string]any{"status": models.NameChangeStatusLapsed, "completed_at": &now, "didit_session_url": "",
			"user_message": "This name change expired. Your name stays as it was."}
		if ch.Attempts == 0 && ch.PayMethod != models.NameChangePayFree {
			decision := nameChangeFeeReturn
			if next, err := s.refundLimit(ctx, ch.UserID); err != nil {
				// Whether its fee goes back decides how it lapses: left for
				// the next sweep rather than guessed.
				log.Printf("[name-change] change=%s lapse skipped, refund allowance unreadable: %v", ch.ID, err)
				continue
			} else if next != nil {
				decision = nameChangeFeeKeep
			}
			updates["fee_decision"] = decision
		}
		ok, err := s.transition(ctx, ch.ID, []string{ch.Status}, updates)
		if err != nil || !ok {
			continue
		}
		moved++
		lapsed := s.reload(ctx, ch)
		s.resolveFee(ctx, lapsed)
		s.publishLapsed(s.reload(ctx, lapsed))
	}
	return moved, nil
}

// sweepUnresolvedFees retries fees a failed call left unsettled, unreturned or
// unrefunded on finished changes.
func (s *NameChangeService) sweepUnresolvedFees(ctx context.Context) (int, error) {
	// A refund that failed to send waits out its backoff (refund_retry_at).
	unresolved, err := s.sweepPage(ctx, "unresolved-fees", s.db.Where("status IN ? AND (fee_status IN ? OR (fee_status = ? AND (refund_retry_at IS NULL OR refund_retry_at <= ?)))",
		models.NameChangeTerminalStatuses, []string{models.NameChangeFeeHeld, models.NameChangeFeeReceived},
		models.NameChangeFeeRefundFailed, s.now()))
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range unresolved {
		before := unresolved[i].FeeStatus
		s.resolveFee(ctx, &unresolved[i])
		if after := s.reload(ctx, &unresolved[i]); after.FeeStatus != before {
			moved++
		}
	}
	return moved, nil
}

// sweepLateDebits finds cancelled MCGP changes whose debit mined after they were
// given up on — the hold exists after all, and buys nothing, so it is returned.
// A debit that late is rare and every check is a contract read, so this runs
// every nameChangeLateDebitEvery and walks the candidates with a cursor rather
// than re-reading the same page every tick. Once a day it also has the debit
// service report failed payments older than the window whose debit is pending.
func (s *NameChangeService) sweepLateDebits(ctx context.Context) (int, error) {
	if s.debits == nil || !s.debits.IsEnabled() {
		return 0, nil
	}
	now := s.now()
	s.lateMu.Lock()
	due := now.Sub(s.lastLate) >= nameChangeLateDebitEvery
	if due {
		s.lastLate = now
	}
	s.lateMu.Unlock()
	if !due {
		return 0, nil
	}
	late, err := s.sweepPage(ctx, "late", s.db.Where("status = ? AND pay_method = ? AND fee_status = ? AND updated_at > ?",
		models.NameChangeStatusCancelled, models.NameChangePayMCGP, models.NameChangeFeeNone, now.Add(-nameChangeLateDebitWindow)))
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range late {
		adopted, err := s.debits.AdoptLateDebit(ctx, late[i].ID)
		if err != nil || !adopted {
			continue
		}
		log.Printf("[name-change] ALERT name_change_late_debit_total=1 change=%s — debit mined after the change was cancelled; returning it", late[i].ID)
		if s.setFee(ctx, late[i].ID, []string{models.NameChangeFeeNone}, models.NameChangeFeeHeld, "") {
			s.resolveFee(ctx, s.reload(ctx, &late[i]))
			moved++
		}
	}
	if claimDailyRun(ctx, s.db, "name-change-late-debit-audit", 24*time.Hour) {
		if _, err := s.debits.AlertLateDebitsBeyondWindow(ctx); err != nil {
			log.Printf("[name-change] late-debit audit failed: %v", err)
		}
	}
	return moved, nil
}

// sweepAlerts tells a human about refunds the sweep cannot finish on its own:
// one that may have left (never retried — a human reads the chain and records
// the outcome) and one that has kept failing to send for about an hour.
func (s *NameChangeService) sweepAlerts(ctx context.Context) (int, error) {
	now := s.now()
	stuck, err := s.sweepPage(ctx, "alerts", s.db.Where("((fee_status = ? AND updated_at < ?) OR fee_status = ? OR (fee_status = ? AND refund_attempts >= ?))",
		models.NameChangeFeeRefundSending, now.Add(-nameChangeRefundAlertAfter), models.NameChangeFeeRefundUncertain,
		models.NameChangeFeeRefundFailed, nameChangeRefundAlertAttempts))
	if err != nil {
		return 0, err
	}
	for _, ch := range stuck {
		alertf("name-change-refund-stuck:"+ch.ID.String(),
			"[name-change] ALERT name_change_refund_unresolved_total=1 change=%s fee_status=%s chain=%s tx=%s — check the chain, then record the outcome (POST /api/admin/name-changes/:id/refund-outcome)",
			ch.ID, ch.FeeStatus, ch.PayChain, ch.FeeTxHash)
	}
	return 0, nil
}
