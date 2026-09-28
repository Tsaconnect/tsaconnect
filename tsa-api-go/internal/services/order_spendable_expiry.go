package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// heldOrder is a held payment with its order's resolution-relevant fields.
type heldOrder struct {
	models.OrderMcgpPayment
	OrderStatus       string
	OrderKind         string
	OrderProductID    uuid.UUID
	OrderQuantity     int
	SellerDeliveredAt *time.Time
	SellerWalletAddr  string
}

// expireHeld resolves holds whose escrow window has run out the way
// ProductEscrowV2.autoRefund resolves an ERC-20 order: in full to the buyer if
// the seller never marked it delivered; otherwise the buyer keeps 90%, the
// seller gets 3% and 7% stays with the platform. Disputed orders and orders
// awaiting a refund/cancel decision are left for a human. Paged with a
// cursor, so rows that keep failing cannot starve newer expiries; each row's
// chain work runs inline, one after another.
func (s *OrderSpendablePaymentService) expireHeld(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-orderMcgpExpiryGrace)
	var rows []heldOrder
	q := s.db.WithContext(ctx).Table("order_mcgp_payments").
		Select(`order_mcgp_payments.*, orders.status AS order_status, orders.kind AS order_kind,
			orders.product_id AS order_product_id, orders.quantity AS order_quantity,
			orders.seller_delivered_at AS seller_delivered_at,
			sellers.wallet_address AS seller_wallet_addr`).
		Joins("JOIN orders ON orders.id = order_mcgp_payments.order_id").
		Joins("LEFT JOIN users AS sellers ON sellers.id = orders.seller_id").
		Where("order_mcgp_payments.status = ? AND orders.escrow_expires_at IS NOT NULL AND orders.escrow_expires_at < ? AND orders.disputed_at IS NULL AND orders.status IN ?",
			models.OrderMcgpHeld, cutoff,
			[]string{models.OrderStatusEscrowed, models.OrderStatusShipped, models.OrderStatusDelivered})
	if err := s.scan.page("expiry", "order_mcgp_payments", q, orderMcgpScanLimit).Scan(&rows).Error; err != nil {
		return 0, err
	}
	if n := len(rows); n > 0 {
		s.scan.advance("expiry", n, orderMcgpScanLimit, rows[n-1].UpdatedAt, rows[n-1].ID)
	} else {
		s.scan.advance("expiry", 0, orderMcgpScanLimit, time.Time{}, uuid.Nil)
	}

	moved := 0
	var firstErr error
	for i := range rows {
		if err := ctx.Err(); err != nil {
			return moved, err
		}
		r := &rows[i]
		order := models.Order{ID: r.OrderID, Kind: r.OrderKind, ProductID: r.OrderProductID, Quantity: r.OrderQuantity}
		fromStatus := r.OrderStatus
		markRefunded := func(restock bool) func(tx *gorm.DB) error {
			return func(tx *gorm.DB) error {
				res := tx.Model(&models.Order{}).Where("id = ? AND status = ? AND disputed_at IS NULL", r.OrderID, fromStatus).
					Updates(map[string]any{"status": models.OrderStatusRefunded, "updated_at": time.Now()})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return ErrOrderMcgpBadState // the order moved; decide next tick
				}
				if restock {
					return RestoreOrderStock(tx, &order)
				}
				return nil
			}
		}
		delivered := r.SellerDeliveredAt != nil
		var err error
		if !delivered {
			_, err = s.refund(ctx, r.OrderID, models.OrderMcgpReleaseEscrowExpired, markRefunded(true), true)
		} else {
			amount, _ := new(big.Int).SetString(r.McgpAmount, 10)
			if amount == nil {
				continue
			}
			buyerShare := new(big.Int).Div(new(big.Int).Mul(amount, big.NewInt(orderMcgpExpiryBuyerBPS)), big.NewInt(10000))
			platform := new(big.Int).Div(new(big.Int).Mul(amount, big.NewInt(orderMcgpExpiryPlatformBP)), big.NewInt(10000))
			sellerShare := new(big.Int).Sub(new(big.Int).Sub(amount, buyerShare), platform) // remainder, as on-chain
			wallet := r.SellerWalletAddr
			if !common.IsHexAddress(wallet) {
				// A seller with no wallet cannot be credited: their share stays
				// with the platform rather than blocking the buyer's 90%.
				sellerShare.SetInt64(0)
				wallet = ""
			}
			_, err = s.release(ctx, r.OrderID, OrderMcgpReleasePlan{
				Reason:       models.OrderMcgpReleaseEscrowExpired,
				SellerWallet: wallet,
				SellerAmount: sellerShare,
				BuyerAmount:  buyerShare,
			}, markRefunded(false), true)
		}
		if err != nil {
			if !errors.Is(err, ErrOrderMcgpBadState) && !errors.Is(err, ErrOrderMcgpAlreadyDecided) {
				alertf("order-mcgp-expiry:"+r.OrderID.String(),
					"[order-mcgp] ALERT order_mcgp_expiry_failed=1 order=%s: %v", r.OrderID, err)
				// A refusal about this row (payout below the minimum, no
				// seller wallet) is alerted above and waits for an admin; it
				// is not the worker failing.
				rowRefusal := errors.Is(err, ErrOrderMcgpPayoutBelowMin) || errors.Is(err, ErrOrderMcgpSellerWallet) || isOrderMcgpRowError(err)
				if firstErr == nil && !rowRefusal {
					firstErr = err
				}
			}
			continue
		}
		moved++
		log.Printf("[order-mcgp] order=%s escrow window expired → decided (delivered=%v)", r.OrderID, delivered)
	}
	return moved, firstErr
}

// notifyExpired tells both parties how an escrow-window expiry actually
// ended. It is sent when the payment reaches its terminal state, from the
// amounts recorded on the row, so it matches what moved on-chain.
func (s *OrderSpendablePaymentService) notifyExpired(pay *models.OrderMcgpPayment, uncollectable bool) {
	if s.bus == nil {
		return
	}
	var order models.Order
	if err := s.db.First(&order, "id = ?", pay.OrderID).Error; err != nil {
		return
	}
	short := order.ID.String()[:8]
	total := formatMcgp(pay.McgpAmount)
	var buyerMsg, sellerMsg string
	switch {
	case !pay.IsSplit():
		buyerMsg = fmt.Sprintf("Order #%s was not completed within the escrow window, so your %s MCGP was returned to your Spendable balance in full.", short, total)
		sellerMsg = fmt.Sprintf("Order #%s was not completed within the escrow window and has been refunded to the buyer.", short)
	case uncollectable:
		buyerMsg = fmt.Sprintf("Order #%s was marked delivered but never confirmed within the escrow window. Your %s MCGP stays in your Spendable balance.", short, total)
		sellerMsg = fmt.Sprintf("Order #%s was not confirmed within the escrow window, and your share could not be collected from the buyer. Support has been alerted.", short)
	default:
		kept := formatMcgp(pay.BuyerAmount)
		sellerShare := formatMcgp(pay.SellerAmount)
		if pay.SellerAmount == "" || pay.SellerAmount == "0" {
			buyerMsg = fmt.Sprintf("Order #%s was marked delivered but never confirmed within the escrow window. You keep %s of your %s MCGP; the seller's share was below the minimum payout and stays with the platform.", short, kept, total)
			sellerMsg = fmt.Sprintf("Order #%s was not confirmed within the escrow window. Your share was below the minimum payout, so nothing could be paid to you.", short)
		} else {
			buyerMsg = fmt.Sprintf("Order #%s was marked delivered but never confirmed within the escrow window. You keep %s of your %s MCGP; %s MCGP went to the seller.", short, kept, total, sellerShare)
			sellerMsg = fmt.Sprintf("Order #%s was not confirmed within the escrow window. As it was marked delivered, you received %s MCGP; the buyer kept %s MCGP.", short, sellerShare, kept)
		}
	}
	data := map[string]any{"orderId": order.ID.String(), "status": order.Status, "resolvedBy": "escrow_expiry"}
	s.bus.Publish(events.Event{
		Type: events.OrderRefunded, UserID: order.BuyerID, Title: "Order Refunded", Message: buyerMsg, Data: data,
	})
	s.bus.Publish(events.Event{
		Type: events.OrderRefunded, UserID: order.SellerID, Title: "Order Closed", Message: sellerMsg, Data: data,
	})
}

// formatMcgp renders an 18dp base amount as MCGP with up to 4 decimals.
func formatMcgp(base string) string {
	v, ok := new(big.Rat).SetString(strings.TrimSpace(base))
	if !ok {
		return "0"
	}
	v.Quo(v, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
	out := v.FloatString(4)
	out = strings.TrimRight(out, "0")
	return strings.TrimSuffix(out, ".")
}
