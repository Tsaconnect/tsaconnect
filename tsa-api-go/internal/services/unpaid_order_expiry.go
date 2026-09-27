package services

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// UnpaidOrderTTL is how long a marketplace order may wait for its escrow
// payment before it is cancelled and its reserved stock released. Checkout
// signs the approve and the escrow within a minute, so only an abandoned or
// failed payment is ever this old.
const UnpaidOrderTTL = time.Hour

// unpaidOrderBatch bounds one pass; the next tick picks up the rest.
const unpaidOrderBatch = 200

// orderEscrowReader answers whether the escrow holds an order. *EscrowService
// fills it.
type orderEscrowReader interface {
	OrderEscrowed(ctx context.Context, orderId [32]byte) (bool, error)
}

// CancelUnpaidOrder cancels an order that is still awaiting payment and
// returns its reserved stock, inside tx. It reports false, changing nothing,
// when the order has left pending_payment — a payment that landed first wins.
func CancelUnpaidOrder(tx *gorm.DB, order *models.Order) (bool, error) {
	res := tx.Model(&models.Order{}).
		Where("id = ? AND status = ?", order.ID, models.OrderStatusPendingPayment).
		Updates(map[string]interface{}{
			"status":     models.OrderStatusCancelled,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	// Services hold no inventory, so there is nothing to restore.
	if order.Kind != models.OrderKindService {
		if err := tx.Model(&models.Product{}).
			Where("id = ?", order.ProductID).
			UpdateColumn("stock", gorm.Expr("stock + ?", order.Quantity)).Error; err != nil {
			return false, fmt.Errorf("restore stock: %w", err)
		}
	}
	return true, nil
}

// UnpaidOrderExpiryService releases stock held by orders nobody paid for.
//
// Placing an order takes its stock before the buyer pays, and until now only
// an explicit cancel gave it back — so a payment that failed or was abandoned
// kept a 1-in-stock listing unbuyable for everyone else, indefinitely.
type UnpaidOrderExpiryService struct {
	db     *gorm.DB
	escrow orderEscrowReader
	ttl    time.Duration
}

func NewUnpaidOrderExpiryService(db *gorm.DB, escrow orderEscrowReader) *UnpaidOrderExpiryService {
	return &UnpaidOrderExpiryService{db: db, escrow: escrow, ttl: UnpaidOrderTTL}
}

// ExpireUnpaidOrders cancels orders still in pending_payment after the TTL
// whose escrow holds nothing, returning how many it cancelled. An order the
// escrow does hold was paid without the app confirming it; cancelling it would
// strand the buyer's funds, so it is left alone and alerted. An unreadable
// escrow skips the order until a later tick, and is returned as an error so a
// persistent RPC failure is visible.
func (s *UnpaidOrderExpiryService) ExpireUnpaidOrders(ctx context.Context) (int64, error) {
	var stale []models.Order
	if err := s.db.WithContext(ctx).
		Select("id", "product_id", "quantity", "kind").
		Where("status = ? AND created_at < ?", models.OrderStatusPendingPayment, time.Now().Add(-s.ttl)).
		Order("created_at").
		Limit(unpaidOrderBatch).
		Find(&stale).Error; err != nil {
		return 0, err
	}

	var expired int64
	var readErr error
	for i := range stale {
		if err := ctx.Err(); err != nil {
			return expired, err
		}
		order := &stale[i]
		escrowed, err := s.escrow.OrderEscrowed(ctx, GenerateOrderID(order.ID))
		if err != nil {
			readErr = fmt.Errorf("escrow read for order %s: %w", order.ID, err)
			continue
		}
		if escrowed {
			log.Printf("[unpaid-order-expiry] ALERT unpaid_order_escrowed=1 order=%s: escrow holds it but the order is still pending_payment", order.ID)
			continue
		}
		var cancelled bool
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			cancelled, err = CancelUnpaidOrder(tx, order)
			return err
		}); err != nil {
			return expired, fmt.Errorf("cancel order %s: %w", order.ID, err)
		}
		if cancelled {
			expired++
			log.Printf("[unpaid-order-expiry] cancelled unpaid order %s (kind=%s qty=%d)", order.ID, order.Kind, order.Quantity)
		}
	}
	return expired, readErr
}
