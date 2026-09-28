package services

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Reconcile drives every payment the request path left unfinished and
// returns how many it moved:
//
//   - debiting past the grace: adopt the landed debit, or fail the missing one
//   - failed within the late window whose debit mined anyway: adopt or return it
//   - releasing / reversing past the grace: finish the chain work
//   - released-uncollectable splits whose collect debit mined late: complete them
//   - held past the escrow window + grace, undisputed: resolve as autoRefund would
//
// Rows are driven one at a time, inline. Only database and infrastructure
// errors fail the tick (and, repeated, page as a failing worker); a row's
// chain outcome is logged here and recorded on the row, where the per-order
// alerts (order_mcgp_stuck, …) pick it up.
func (s *OrderSpendablePaymentService) Reconcile(ctx context.Context) (int, error) {
	if !s.IsEnabled() {
		return 0, nil
	}
	moved := 0
	var firstErr error
	keep := func(orderID uuid.UUID, err error) {
		switch {
		case err == nil:
		case isOrderMcgpRowError(err):
			log.Printf("[order-mcgp] reconcile order=%s not finished: %v", orderID, err)
		case firstErr == nil:
			firstErr = err
		}
	}

	stale := time.Now().Add(-orderMcgpGrace)
	var debiting []models.OrderMcgpPayment
	if err := s.scan.page("debiting", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status = ? AND updated_at <= ?", models.OrderMcgpDebiting, stale), orderMcgpScanLimit).
		Find(&debiting).Error; err != nil {
		return 0, err
	}
	s.advance("debiting", debiting)
	for i := range debiting {
		_, won, err := s.resolveDebiting(ctx, &debiting[i])
		keep(debiting[i].OrderID, err)
		if won {
			moved++
		}
	}

	var failed []models.OrderMcgpPayment
	if err := s.scan.page("late", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status = ? AND updated_at > ?", models.OrderMcgpFailed, time.Now().Add(-orderMcgpLateWindow)), orderMcgpScanLimit).
		Find(&failed).Error; err != nil {
		return moved, err
	}
	s.advance("late", failed)
	for i := range failed {
		adopted, err := s.adoptLateDebit(ctx, &failed[i])
		keep(failed[i].OrderID, err)
		if adopted {
			moved++
		}
	}

	var deciding []models.OrderMcgpPayment
	if err := s.scan.page("deciding", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status IN ? AND updated_at <= ?", []string{models.OrderMcgpReleasing, models.OrderMcgpReversing}, stale),
		orderMcgpScanLimit).Find(&deciding).Error; err != nil {
		return moved, err
	}
	s.advance("deciding", deciding)
	for i := range deciding {
		p := &deciding[i]
		err := s.drive(ctx, p)
		keep(p.OrderID, err)
		if err == nil {
			moved++
			continue
		}
		// Measured from the decision, not the payment: an old hold that was
		// only just released is not stuck.
		if p.DecidedAt != nil && time.Since(*p.DecidedAt) > orderMcgpStuckFor {
			alertf("order-mcgp-stuck:"+p.OrderID.String(),
				"[order-mcgp] ALERT order_mcgp_stuck=1 order=%s status=%s decided=%s reason=%q",
				p.OrderID, p.Status, p.DecidedAt.Format(time.RFC3339), p.FailureReason)
		}
	}

	var closed []models.OrderMcgpPayment
	if err := s.scan.page("orphan", "", s.db.WithContext(ctx).Model(&models.OrderMcgpPayment{}).
		Where("status = ? AND failure_reason LIKE ? AND updated_at > ?",
			models.OrderMcgpReleased, orderMcgpUncollectablePrefix+"%", time.Now().Add(-orderMcgpOrphanWindow)),
		orderMcgpScanLimit).Find(&closed).Error; err != nil {
		return moved, err
	}
	s.advance("orphan", closed)
	for i := range closed {
		recovered, err := s.recoverOrphanCollect(ctx, &closed[i])
		keep(closed[i].OrderID, err)
		if recovered {
			moved++
		}
	}

	n, err := s.expireHeld(ctx)
	keep(uuid.Nil, err)
	return moved + n, firstErr
}

func (s *OrderSpendablePaymentService) advance(key string, rows []models.OrderMcgpPayment) {
	if n := len(rows); n > 0 {
		s.scan.advance(key, n, orderMcgpScanLimit, rows[n-1].UpdatedAt, rows[n-1].ID)
		return
	}
	s.scan.advance(key, 0, orderMcgpScanLimit, time.Time{}, uuid.Nil)
}
