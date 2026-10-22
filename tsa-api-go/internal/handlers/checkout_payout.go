package handlers

import (
	"context"
	"errors"
	"log"
	"math/big"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// sellerPayoutPlanner prices and records a marketplace order's fiat payout
// (services.SellerPayoutPlanner). Nil leaves every order paying the seller's
// wallet, exactly as before.
type sellerPayoutPlanner interface {
	Plan(ctx context.Context, sellerID uuid.UUID, token string, net *big.Int) (*services.PayoutPlan, error)
	Persist(tx *gorm.DB, order *models.Order, sellerWallet string, plan *services.PayoutPlan, expected *big.Int) error
	TreasuryAddress() string
}

// WithPayouts enables fiat payouts to sellers who set a bank account.
func (ch *CheckoutHandler) WithPayouts(p sellerPayoutPlanner) *CheckoutHandler {
	ch.payouts = p
	return ch
}

// planLinePayout prices the line's payout, if its seller has one, and adds the
// offramp costs to the line's shippingAmount: the escrow pays that to the
// seller (the treasury) in full and takes no fee on it, so the buyer's total
// carries the costs while the platform fee stays on the product alone.
func (ch *CheckoutHandler) planLinePayout(ctx context.Context, line *pricedCartLine, token string) *cartPricingError {
	if ch.payouts == nil {
		return nil
	}
	net := new(big.Int).Add(line.productAmount, line.shippingAmount)
	plan, err := ch.payouts.Plan(ctx, line.seller.ID, token, net)
	if err != nil {
		// An unreadable payout method must not quietly divert the order to the
		// wallet: the buyer retries.
		return &cartPricingError{500, "Failed to resolve the seller's payout"}
	}
	if plan == nil {
		return nil
	}
	line.payout = plan
	line.shippingAmount = new(big.Int).Add(line.shippingAmount, plan.GrossUp)
	return nil
}

// escrowSellerFor is the address the escrow must be created against for
// order: the wallet recorded when the order was made (the treasury for a
// payout order — fixed then, whatever the config says now), else the seller's
// current wallet.
func escrowSellerFor(ctx context.Context, db *gorm.DB, order *models.Order, sellerWallet string) (string, error) {
	p, err := services.PayoutForOrder(ctx, db, order.ID)
	if err != nil {
		return "", err
	}
	if p != nil && order.EscrowSellerWallet != "" {
		return order.EscrowSellerWallet, nil
	}
	return sellerWallet, nil
}

// sellerEscrowCaller makes the escrow calls only the order's on-chain seller
// may make, for orders whose seller is the treasury (services.SellerEscrowCalls).
type sellerEscrowCaller interface {
	MarkDelivered(ctx context.Context, orderID uuid.UUID, escrowAddr string) (string, error)
	CancelOrder(ctx context.Context, orderID uuid.UUID, escrowAddr string) (string, error)
}

// WithSellerCalls lets payout orders be delivered and cancelled by the server.
func (ch *CheckoutHandler) WithSellerCalls(c sellerEscrowCaller) *CheckoutHandler {
	ch.sellerCalls = c
	return ch
}

// isPayoutOrder reports whether order's on-chain seller is the treasury, which
// signs the seller-only escrow calls on the seller's behalf.
func isPayoutOrder(ctx context.Context, db *gorm.DB, order *models.Order) (bool, error) {
	p, err := services.PayoutForOrder(ctx, db, order.ID)
	return p != nil, err
}

// callAsTreasurySeller runs one treasury-signed seller call and answers the
// request when it cannot be completed now. true means fn returned a hash.
func (ch *CheckoutHandler) callAsTreasurySeller(c *gin.Context, doing string, fn func() (string, error)) (string, bool) {
	if ch.sellerCalls == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "This order can't be updated right now — try again later")
		return "", false
	}
	hash, err := fn()
	switch {
	case err == nil:
		return hash, true
	case errors.Is(err, services.ErrSellerCallUncertain):
		utils.ErrorResponse(c, http.StatusConflict, "Your request is being confirmed on-chain — please retry in a moment")
	default:
		log.Printf("[checkout] treasury could not %s: %v", doing, err)
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "This order can't be updated right now — try again later")
	}
	return "", false
}

// cancelAsTreasury cancels a payout order on-chain as the treasury and
// completes it once the escrow has proven the cancel. It returns true when it
// answered the request (payout order or an error); false for an ordinary order.
func (ch *CheckoutHandler) cancelAsTreasury(c *gin.Context, order *models.Order) bool {
	payout, err := isPayoutOrder(c.Request.Context(), config.DB, order)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cancel order")
		return true
	}
	if !payout {
		return false
	}
	hash, ok := ch.callAsTreasurySeller(c, "cancel", func() (string, error) {
		return ch.sellerCalls.CancelOrder(c.Request.Context(), order.ID, ch.orderEscrowContract(order))
	})
	if !ok {
		return true
	}
	if _, ok := ch.requireOrderEvent(c, order, hash, "OrderCancelled"); !ok {
		return true
	}
	ch.finishSellerCancel(c, order, hash)
	return true
}
