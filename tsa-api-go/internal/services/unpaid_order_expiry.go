package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// UnpaidOrderTTL is how long a marketplace order may sit without payment
// activity before it is cancelled and its reserved stock released. It runs
// from the order's updated_at, which every prepare-approve/prepare-escrow
// refreshes (TouchUnpaidOrder): an order whose buyer is paying right now —
// including a retry long after the order was placed — is never old enough.
const UnpaidOrderTTL = time.Hour

// unpaidOrderExpiryFrom bounds the sweep to orders placed since it shipped.
// Older abandoned orders took stock that merchants may have corrected by hand
// since, and some may have been prepared against an earlier escrow contract,
// which the escrow read here does not cover.
var unpaidOrderExpiryFrom = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

// unpaidOrderBatch is one page of the sweep (a var so tests can page).
var unpaidOrderBatch = 200

// orderEscrowReader answers whether the escrow holds an order. *EscrowService
// fills it.
type orderEscrowReader interface {
	OrderEscrowed(ctx context.Context, orderId [32]byte) (bool, error)
}

// TouchUnpaidOrder records payment activity on an order still awaiting
// payment, restarting its UnpaidOrderTTL.
func TouchUnpaidOrder(db *gorm.DB, id uuid.UUID) error {
	return db.Model(&models.Order{}).
		Where("id = ? AND status = ?", id, models.OrderStatusPendingPayment).
		Update("updated_at", time.Now()).Error
}

// RestoreOrderStock returns an order's units to its listing, inside tx.
// Services hold no inventory, so there is nothing to restore.
func RestoreOrderStock(tx *gorm.DB, order *models.Order) error {
	if order.Kind == models.OrderKindService {
		return nil
	}
	if err := tx.Model(&models.Product{}).
		Where("id = ?", order.ProductID).
		UpdateColumn("stock", gorm.Expr("stock + ?", order.Quantity)).Error; err != nil {
		return fmt.Errorf("restore stock: %w", err)
	}
	return nil
}

// CancelUnpaidOrder cancels an order that is still awaiting payment and
// returns its reserved stock, inside tx. It reports false, changing nothing,
// when the order has left pending_payment — a payment that landed first wins.
func CancelUnpaidOrder(tx *gorm.DB, order *models.Order) (bool, error) {
	return cancelPending(tx, order, nil)
}

// cancelPending is CancelUnpaidOrder, optionally also requiring no payment
// activity since idleBefore — checked in the same UPDATE, so a buyer who
// starts paying after the sweep picked the order still keeps it.
func cancelPending(tx *gorm.DB, order *models.Order, idleBefore *time.Time) (bool, error) {
	q := tx.Model(&models.Order{}).
		Where("id = ? AND status = ?", order.ID, models.OrderStatusPendingPayment)
	if idleBefore != nil {
		q = q.Where("updated_at < ?", *idleBefore)
	}
	res := q.Updates(map[string]interface{}{
		"status":     models.OrderStatusCancelled,
		"updated_at": time.Now(),
	})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	if err := RestoreOrderStock(tx, order); err != nil {
		return false, err
	}
	return true, nil
}

// ErrOrderNotPayable means the order is in a state a payment cannot apply to
// (already escrowed, shipped, refunded, or a cancel that cannot be undone).
var ErrOrderNotPayable = errors.New("order cannot take this payment")

// RevivableOnEscrow reports whether a cancelled order may be restored by a
// verified escrow: only a product order that was never escrowed. An order
// escrowed before carries its escrow hash — its cancel may have refunded the
// buyer on-chain, and its original receipt would still verify. A service
// order is excluded: its booking slot may have gone to someone else.
func RevivableOnEscrow(order *models.Order) bool {
	return order.Status == models.OrderStatusCancelled &&
		order.Kind != models.OrderKindService &&
		order.EscrowTxHash == ""
}

// EscrowMark is what MarkOrderEscrowed did beyond the plain transition.
type EscrowMark struct {
	// Revived: the order had been cancelled and was restored.
	Revived bool
	// Oversold: restored, but its units had already been sold again. The
	// seller must cancel it, which refunds the buyer from the escrow.
	Oversold bool
}

// MarkOrderEscrowed applies updates (which set status escrowed) to an order
// whose escrow has been verified on-chain, inside tx. The chain is the truth:
// a never-escrowed product order cancelled before its payment was confirmed
// (RevivableOnEscrow) is restored and takes its stock back, because the
// buyer's money is in escrow either way. Any other state is ErrOrderNotPayable.
func MarkOrderEscrowed(tx *gorm.DB, order *models.Order, updates map[string]interface{}) (EscrowMark, error) {
	res := tx.Model(&models.Order{}).
		Where("id = ? AND status = ?", order.ID, models.OrderStatusPendingPayment).
		Updates(updates)
	if res.Error != nil {
		return EscrowMark{}, res.Error
	}
	if res.RowsAffected == 1 {
		return EscrowMark{}, nil
	}

	res = tx.Model(&models.Order{}).
		Where("id = ? AND status = ? AND kind <> ? AND (escrow_tx_hash IS NULL OR escrow_tx_hash = '')",
			order.ID, models.OrderStatusCancelled, models.OrderKindService).
		Updates(updates)
	if res.Error != nil {
		return EscrowMark{}, res.Error
	}
	if res.RowsAffected == 0 {
		return EscrowMark{}, ErrOrderNotPayable
	}
	mark := EscrowMark{Revived: true}
	log.Printf("[orders] ALERT escrowed_after_cancel=1 order=%s: payment verified for a cancelled order; restored", order.ID)
	taken := tx.Model(&models.Product{}).
		Where("id = ? AND stock >= ?", order.ProductID, order.Quantity).
		UpdateColumn("stock", gorm.Expr("stock - ?", order.Quantity))
	if taken.Error != nil {
		return mark, fmt.Errorf("retake stock: %w", taken.Error)
	}
	if taken.RowsAffected == 0 {
		mark.Oversold = true
		log.Printf("[orders] ALERT oversold=1 order=%s product=%s: paid order restored but its stock was already sold", order.ID, order.ProductID)
	}
	return mark, nil
}

// UnpaidOrderExpiryService releases stock held by orders nobody paid for.
//
// Placing an order takes its stock before the buyer pays, and until now only
// an explicit cancel gave it back — so a payment that failed or was abandoned
// kept a 1-in-stock listing unbuyable for everyone else, indefinitely.
type UnpaidOrderExpiryService struct {
	db     *gorm.DB
	escrow orderEscrowReader
	bus    *events.Bus
	ttl    time.Duration
}

// NewUnpaidOrderExpiryService builds the sweep. bus may be nil (no notices).
func NewUnpaidOrderExpiryService(db *gorm.DB, escrow orderEscrowReader, bus *events.Bus) *UnpaidOrderExpiryService {
	return &UnpaidOrderExpiryService{db: db, escrow: escrow, bus: bus, ttl: UnpaidOrderTTL}
}

// ExpireUnpaidOrders cancels product orders with no payment activity for the
// TTL whose escrow holds nothing, returning how many it cancelled. Service
// orders are left alone: they hold no stock, and one may back a converted
// booking request that cancelling would strand.
//
// An order the escrow does hold was paid without the app confirming it;
// cancelling it would strand the buyer's funds, so it is alerted and left for
// the buyer's submit-escrow to confirm. An unreadable escrow skips the order
// until a later tick and is returned as an error, so a persistent RPC failure
// is visible. Every stale order is visited each tick, so skipped ones cannot
// starve the rest.
func (s *UnpaidOrderExpiryService) ExpireUnpaidOrders(ctx context.Context) (int64, error) {
	cutoff := time.Now().Add(-s.ttl)
	var expired int64
	var readErr error
	after := uuid.Nil
	for {
		var page []models.Order
		if err := s.db.WithContext(ctx).
			Select("id", "buyer_id", "seller_id", "product_id", "quantity", "kind").
			Where("status = ? AND kind <> ? AND created_at >= ? AND updated_at < ? AND id > ?",
				models.OrderStatusPendingPayment, models.OrderKindService, unpaidOrderExpiryFrom, cutoff, after).
			Order("id").
			Limit(unpaidOrderBatch).
			Find(&page).Error; err != nil {
			return expired, err
		}
		for i := range page {
			if err := ctx.Err(); err != nil {
				return expired, err
			}
			order := &page[i]
			cancelled, err := s.expire(ctx, order, cutoff)
			if err != nil {
				readErr = err
				continue
			}
			if cancelled {
				expired++
			}
		}
		if len(page) < unpaidOrderBatch {
			return expired, readErr
		}
		after = page[len(page)-1].ID
	}
}

// expire cancels one stale order when the escrow proves it unpaid.
func (s *UnpaidOrderExpiryService) expire(ctx context.Context, order *models.Order, cutoff time.Time) (bool, error) {
	escrowed, err := s.escrow.OrderEscrowed(ctx, GenerateOrderID(order.ID))
	if err != nil {
		return false, fmt.Errorf("escrow read for order %s: %w", order.ID, err)
	}
	if escrowed {
		log.Printf("[unpaid-order-expiry] ALERT unpaid_order_escrowed=1 order=%s: escrow holds it but the order is still pending_payment", order.ID)
		return false, nil
	}
	var cancelled bool
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		cancelled, err = cancelPending(tx, order, &cutoff)
		return err
	}); err != nil {
		return false, fmt.Errorf("cancel order %s: %w", order.ID, err)
	}
	if cancelled {
		log.Printf("[unpaid-order-expiry] cancelled unpaid order %s (qty=%d)", order.ID, order.Quantity)
		s.notify(order)
	}
	return cancelled, nil
}

// notify tells both sides, as a buyer's own cancel does.
func (s *UnpaidOrderExpiryService) notify(order *models.Order) {
	if s.bus == nil {
		return
	}
	short := order.ID.String()[:8]
	data := map[string]interface{}{
		"orderId":     order.ID.String(),
		"status":      models.OrderStatusCancelled,
		"cancelledBy": "expiry",
	}
	s.bus.Publish(events.Event{
		Type:    events.OrderRefunded,
		UserID:  order.BuyerID,
		Title:   "Order Cancelled",
		Message: fmt.Sprintf("Order #%s was cancelled because it was not paid within an hour. Nothing was charged; place it again to buy.", short),
		Data:    data,
	})
	s.bus.Publish(events.Event{
		Type:    events.OrderRefunded,
		UserID:  order.SellerID,
		Title:   "Order Cancelled",
		Message: fmt.Sprintf("Order #%s was cancelled because the buyer did not pay within an hour. Its stock is back on sale.", short),
		Data:    data,
	})
}
