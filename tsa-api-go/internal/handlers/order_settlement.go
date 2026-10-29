package handlers

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// order_settlement.go — recording an order OrderEscrow has paid out, shared by the submit endpoints
// (the buyer's confirm, a refund claim) and the deadline sweep (services.OrderTimeoutClaimer), so an
// order settled by anyone's claimTimeout gets the same fee settlement, stock and notices.

// openEscrowStatuses are the order statuses an on-chain payout can still move.
var openEscrowStatuses = services.OrderOpenStatuses

// recordReleased marks order completed (the escrow paid the seller) and settles its fees. The move
// is conditional on an open status, so of two recorders racing only one acts. moved is false when
// the order had already left them.
func (ch *CheckoutHandler) recordReleased(ctx context.Context, order *models.Order, txHash string, buyerConfirmed bool) (moved bool, err error) {
	now := time.Now()
	updates := map[string]interface{}{"status": models.OrderStatusCompleted}
	if txHash != "" {
		updates["release_tx_hash"] = txHash
	}
	if buyerConfirmed {
		updates["buyer_confirmed_at"] = now
	}
	res := config.DB.WithContext(ctx).Model(&models.Order{}).Where("id = ? AND status IN ?", order.ID, openEscrowStatuses).Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		return false, res.Error
	}
	ch.settleCompletedOrderFees(order, now)
	msg := fmt.Sprintf("The buyer confirmed receipt for order #%s. Funds have been released to your wallet.", order.ID.String()[:8])
	if !buyerConfirmed {
		msg = fmt.Sprintf("Order #%s's confirm window passed, so its payment was released to your wallet.", order.ID.String()[:8])
	}
	ch.EventBus.Publish(events.Event{
		Type: events.OrderCompleted, UserID: order.SellerID, Title: "Payment Released", Message: msg,
		Data: map[string]interface{}{"orderId": order.ID.String(), "status": models.OrderStatusCompleted},
	})
	return true, nil
}

// recordRefunded marks order refunded (the escrow returned product + shipping to the buyer) and
// restores its stock, in one transaction, then tells both parties.
func (ch *CheckoutHandler) recordRefunded(ctx context.Context, order *models.Order, txHash string) (moved bool, err error) {
	updates := map[string]interface{}{"status": models.OrderStatusRefunded}
	if txHash != "" {
		updates["release_tx_hash"] = txHash
	}
	err = config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Order{}).Where("id = ? AND status IN ?", order.ID, openEscrowStatuses).Updates(updates)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		moved = true
		return services.RestoreOrderStock(tx, order)
	})
	if err != nil || !moved {
		return false, err
	}
	ch.notifyOrderResolved(order, models.OrderStatusRefunded, true, txHash)
	return true, nil
}

// SettleOrderFromChain records an order the escrow has already paid out — by anyone's claimTimeout,
// or a call whose submit never reached the API. The sweep records its own claim's hash on the order
// first, so recording it here keeps that hash. A resolver's split is recorded by its own submit;
// one that never was is flagged for a person, since only its log says how it was split.
func (ch *CheckoutHandler) SettleOrderFromChain(ctx context.Context, order *models.Order, state services.OrderState) (bool, error) {
	switch state {
	case services.OrderStateReleased:
		return ch.recordReleased(ctx, order, "", false)
	case services.OrderStateRefunded:
		return ch.recordRefunded(ctx, order, "")
	case services.OrderStateResolved:
		log.Printf("[checkout] ALERT order_resolved_unrecorded=1 order=%s: resolved on-chain but never submitted — record it from the OrderResolved log", order.ID)
	}
	return false, nil
}
