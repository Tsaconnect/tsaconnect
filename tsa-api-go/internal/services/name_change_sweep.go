package services

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
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
		s.sweepPayingMCGP, s.sweepStablePayments, s.sweepExpired, s.sweepUnresolvedFees, s.sweepLateDebits, s.sweepAlerts,
	} {
		n, err := step(ctx)
		moved += n
		if err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// sweepPayingMCGP settles MCGP fees whose debit's fate was unknown when the
// request returned, from the contract's debit record.
func (s *NameChangeService) sweepPayingMCGP(ctx context.Context) (int, error) {
	if s.debits == nil || !s.debits.IsEnabled() {
		return 0, nil
	}
	var paying []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("status = ? AND pay_method = ?", models.NameChangeStatusPaying, models.NameChangePayMCGP).
		Order("created_at asc").Limit(nameChangeSweepBatch).Find(&paying).Error; err != nil {
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
	var named []models.UserNameChange
	if err := db.Where("pay_method = ? AND pay_ref IS NOT NULL AND fee_status = ? AND (status = ? OR (status = ? AND completed_at > ?))",
		models.NameChangePayStablecoin, models.NameChangeFeeNone, models.NameChangeStatusPaying,
		models.NameChangeStatusCancelled, now.Add(-nameChangeLatePaymentWindow)).
		Order("updated_at asc").Limit(nameChangeSweepBatch).Find(&named).Error; err != nil {
		return 0, err
	}
	moved := 0
	for i := range named {
		if out, _, _ := s.settleStablePayment(ctx, &named[i]); out == stableProven {
			moved++
		}
	}

	// Paying changes out of time. One with no payment named closes; one whose
	// payment is still unproven waits for it (the proof above decides), and a
	// human hears about it once it has waited a day past the change's time.
	res := db.Model(&models.UserNameChange{}).
		Where("status = ? AND pay_method = ? AND pay_ref IS NULL AND expires_at < ?", models.NameChangeStatusPaying, models.NameChangePayStablecoin, now).
		Updates(map[string]any{"status": models.NameChangeStatusCancelled, "completed_at": &now,
			"review_note": "fee not paid: no payment was confirmed in time", "user_message": "This name change was closed because its fee wasn't paid."})
	if res.Error != nil {
		return moved, res.Error
	}
	moved += int(res.RowsAffected)
	var waiting []models.UserNameChange
	if err := db.Where("status = ? AND pay_method = ? AND pay_ref IS NOT NULL AND expires_at < ?",
		models.NameChangeStatusPaying, models.NameChangePayStablecoin, now.Add(-24*time.Hour)).
		Limit(nameChangeSweepBatch).Find(&waiting).Error; err != nil {
		return moved, err
	}
	for _, ch := range waiting {
		alertf("name-change-payment-unproven:"+ch.ID.String(),
			"[name-change] ALERT name_change_payment_unproven_total=1 change=%s chain=%s tx=%s — the named payment still can't be proven a day after the change expired", ch.ID, ch.PayChain, *ch.PayRef)
	}
	return moved, nil
}

// sweepExpired lapses changes out of time. One with a session still open gets a
// grace period to hear back from Didit. An unused change's fee is returned —
// unless the user has already had their allowance of returns, when it is kept.
func (s *NameChangeService) sweepExpired(ctx context.Context) (int, error) {
	now := s.now()
	var expired []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("(status IN ? AND expires_at < ?) OR (status = ? AND expires_at < ?)",
		[]string{models.NameChangeStatusPaid, models.NameChangeStatusDeclined}, now,
		models.NameChangeStatusVerifying, now.Add(-nameChangeVerifyingGrace)).
		Order("expires_at asc").Limit(nameChangeSweepBatch).Find(&expired).Error; err != nil {
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
	var unresolved []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("status IN ? AND fee_status IN ?", models.NameChangeTerminalStatuses,
		[]string{models.NameChangeFeeHeld, models.NameChangeFeeReceived, models.NameChangeFeeRefundFailed}).
		Order("updated_at asc").Limit(nameChangeSweepBatch).Find(&unresolved).Error; err != nil {
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
	var late []models.UserNameChange
	q := s.db.WithContext(ctx).Model(&models.UserNameChange{}).
		Where("status = ? AND pay_method = ? AND fee_status = ? AND updated_at > ?",
			models.NameChangeStatusCancelled, models.NameChangePayMCGP, models.NameChangeFeeNone, now.Add(-nameChangeLateDebitWindow))
	if err := s.scan.page("late", "", q, nameChangeSweepBatch).Find(&late).Error; err != nil {
		return 0, err
	}
	if n := len(late); n > 0 {
		s.scan.advance("late", n, nameChangeSweepBatch, late[n-1].UpdatedAt, late[n-1].ID)
	} else {
		s.scan.advance("late", 0, nameChangeSweepBatch, time.Time{}, uuid.Nil)
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
// the outcome) and one that has kept failing to send for an hour.
func (s *NameChangeService) sweepAlerts(ctx context.Context) (int, error) {
	now := s.now()
	var stuck []models.UserNameChange
	if err := s.db.WithContext(ctx).Where("(fee_status = ? AND updated_at < ?) OR fee_status = ? OR (fee_status = ? AND updated_at < ?)",
		models.NameChangeFeeRefundSending, now.Add(-nameChangeRefundAlertAfter), models.NameChangeFeeRefundUncertain,
		models.NameChangeFeeRefundFailed, now.Add(-nameChangeRefundRetryAlertAfter)).
		Limit(nameChangeSweepBatch).Find(&stuck).Error; err != nil {
		return 0, err
	}
	for _, ch := range stuck {
		alertf("name-change-refund-stuck:"+ch.ID.String(),
			"[name-change] ALERT name_change_refund_unresolved_total=1 change=%s fee_status=%s chain=%s tx=%s — check the chain, then record the outcome (POST /api/admin/name-changes/:id/refund-outcome)",
			ch.ID, ch.FeeStatus, ch.PayChain, ch.FeeTxHash)
	}
	return 0, nil
}
