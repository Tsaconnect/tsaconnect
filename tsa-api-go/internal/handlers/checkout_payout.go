package handlers

import (
	"context"
	"errors"
	"fmt"
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
	// What the escrow will release to the seller: product + shipping less the merchant fee.
	net := new(big.Int).Add(line.productAmount, line.shippingAmount)
	if line.platformFee != nil {
		net.Sub(net, line.platformFee)
	}
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
	MarkDelivered(ctx context.Context, orderID uuid.UUID, key [32]byte, escrowAddr string) (string, error)
	CancelOrder(ctx context.Context, orderID uuid.UUID, key [32]byte, escrowAddr string) (string, error)
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
//
// The treasury signs on the seller's behalf, so the chain's own seller check no
// longer protects the order: only the order's seller may ask, checked here and
// not left to the caller.
func (ch *CheckoutHandler) cancelAsTreasury(c *gin.Context, user *models.User, order *models.Order) bool {
	payout, err := isPayoutOrder(c.Request.Context(), config.DB, order)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cancel order")
		return true
	}
	if !payout {
		return false
	}
	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can cancel this order")
		return true
	}
	hash, ok := ch.callAsTreasurySeller(c, "cancel", func() (string, error) {
		return ch.sellerCalls.CancelOrder(c.Request.Context(), order.ID, services.OrderKeyOf(order), ch.orderEscrowContract(order))
	})
	if !ok {
		return true
	}
	if !ch.requireSellerCancel(c, order, hash) {
		return true
	}
	ch.finishSellerCancel(c, order, hash)
	return true
}

// sellerPayoutStatus is how a seller sees their bank payout on the order: a
// stage in plain terms, never the machine's internals or the failure reason.
type sellerPayoutStatus struct {
	// Stage: waiting_for_confirmation, sending, paid, paid_wallet, review, cancelled.
	Stage        string `json:"stage"`
	AmountNGN    string `json:"amountNgn"`
	Bank         string `json:"bank"`
	AccountLast4 string `json:"accountLast4"`
}

var payoutStages = map[string]string{
	models.SellerPayoutAwaitingRelease: "waiting_for_confirmation",
	models.SellerPayoutReceived:        "sending",
	models.SellerPayoutBridging:        "sending",
	models.SellerPayoutBridged:         "sending",
	models.SellerPayoutSelling:         "sending",
	models.SellerPayoutOrdered:         "sending",
	models.SellerPayoutDepositing:      "sending",
	models.SellerPayoutPayingCompany:   "sending",
	models.SellerPayoutDeposited:       "sending",
	models.SellerPayoutPayingWallet:    "sending",
	models.SellerPayoutPaid:            "paid",
	models.SellerPayoutPaidWallet:      "paid_wallet",
	models.SellerPayoutNeedsReview:     "review",
	models.SellerPayoutCancelled:       "cancelled",
}

// orderDetailView is an order with, for its seller only, the status of the
// bank payout the order carries.
type orderDetailView struct {
	orderPreview
	Payout *sellerPayoutStatus `json:"payout,omitempty"`
}

// payoutStatusFor is the payout status to show user on order, or nil when the
// order has no payout or user is not its seller.
func payoutStatusFor(ctx context.Context, db *gorm.DB, order *models.Order, user *models.User) *sellerPayoutStatus {
	if order.SellerID != user.ID {
		return nil
	}
	p, err := services.PayoutForOrder(ctx, db, order.ID)
	if err != nil || p == nil {
		return nil
	}
	last4 := p.AccountNumber
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	return &sellerPayoutStatus{
		Stage: payoutStages[p.Status], AmountNGN: fmt.Sprintf("%d.%02d", p.FiatTargetKobo/100, p.FiatTargetKobo%100),
		Bank: p.InstitutionName, AccountLast4: last4,
	}
}
