package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Marketplace checkout paid from Spendable MCGP.
//
// Owner decision 2026-09-28: the only MCGP usable inside the app is Spendable
// MCGP — the premium balance in McgpEntitlementSwapV2 — never ERC-20 MCGP in
// the user's own wallet. An MCGP order is therefore funded by an operator
// debit of the buyer's Spendable MCGP (services.OrderSpendablePaymentService),
// and that pending debit is the escrow hold. Every place the ProductEscrow
// would pay the seller releases it (settle + credit the seller); every place
// it would refund the buyer reverses it. Orders funded through ProductEscrow
// before this change (PaymentSource == "") keep their on-chain lifecycle.

// orderSpendablePayer is what checkout needs from the Spendable MCGP service.
type orderSpendablePayer interface {
	IsEnabled() bool
	Balance(wallet string) (*big.Int, error)
	CheckLimits(ctx context.Context, amount *big.Int) error
	Fund(ctx context.Context, in services.OrderMcgpFundInput) (*models.OrderMcgpPayment, error)
	Release(ctx context.Context, orderID uuid.UUID, plan services.OrderMcgpReleasePlan, applyOrder func(tx *gorm.DB) error) (*models.OrderMcgpPayment, error)
	Refund(ctx context.Context, orderID uuid.UUID, applyOrder func(tx *gorm.DB) error) (*models.OrderMcgpPayment, error)
}

// SetSpendablePayments wires the Spendable MCGP payment service. nil (no
// operator signer) leaves MCGP checkout unavailable; stablecoins are
// unaffected.
func (ch *CheckoutHandler) SetSpendablePayments(svc *services.OrderSpendablePaymentService) {
	if svc != nil {
		ch.spendable = svc
	}
}

func (ch *CheckoutHandler) spendableEnabled() bool {
	return ch.spendable != nil && ch.spendable.IsEnabled()
}

const mcgpCheckoutUnavailable = "Paying with MCGP is temporarily unavailable. Pay with USDC or USDT, or try again later."

// refuseMcgpUnavailable answers 503 for an MCGP checkout while the Spendable
// MCGP path is not wired: an order created now could never be paid.
func (ch *CheckoutHandler) refuseMcgpUnavailable(c *gin.Context, token string) bool {
	if token != "MCGP" || ch.spendableEnabled() {
		return false
	}
	utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, mcgpCheckoutUnavailable, "mcgp_unavailable")
	return true
}

// refuseSpendableLimit answers 400 (rolling tx back) when one order's MCGP
// total is outside what a single operator debit and credit can carry, before
// the order takes stock.
func (ch *CheckoutHandler) refuseSpendableLimit(c *gin.Context, tx *gorm.DB, token string, amount *big.Int) bool {
	if token != "MCGP" || !ch.spendableEnabled() {
		return false
	}
	err := ch.spendable.CheckLimits(c.Request.Context(), amount)
	if err == nil {
		return false
	}
	if tx != nil {
		tx.Rollback()
	}
	spendableFail(c, err)
	return true
}

// Legacy ERC-20 MCGP funding (approve + ProductEscrow deposit) is closed to
// new orders. An app from before this change still calls prepare-approve for
// an MCGP order; it is told plainly to update rather than being allowed to
// spend wallet MCGP.
const legacyMcgpFundingRefused = "Paying with MCGP now uses your Spendable MCGP balance, not MCGP in your wallet. Update the TSA Connect app to pay this order, or pay with USDC or USDT."

func refuseLegacyMcgpFunding(c *gin.Context, order *models.Order) bool {
	if order.Token != "MCGP" && !order.IsSpendableMCGP() {
		return false
	}
	utils.ErrorResponseWithCode(c, http.StatusConflict, legacyMcgpFundingRefused, "mcgp_spendable_only")
	return true
}

// refuseSpendableOnChainStep answers 409 for an escrow-contract step asked of
// an order that has no escrow — its money is a Spendable MCGP hold.
func refuseSpendableOnChainStep(c *gin.Context, order *models.Order, instead string) bool {
	if !order.IsSpendableMCGP() {
		return false
	}
	utils.ErrorResponseWithCode(c, http.StatusConflict,
		"This order was paid with Spendable MCGP and has no escrow transaction to sign. "+instead, "spendable_order")
	return true
}

// spendableFail maps a Spendable MCGP payment error to a clear response.
func spendableFail(c *gin.Context, err error) {
	var short *services.SpendableShortfallError
	var limit *services.SpendableLimitError
	switch {
	case errors.As(err, &short):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, fmt.Sprintf(
			"Not enough Spendable MCGP. This order needs %s MCGP and your Spendable MCGP balance is %s MCGP.",
			formatTokenBalance(short.Need, 18), formatTokenBalance(short.Have, 18)), "insufficient_balance")
	case errors.Is(err, services.ErrSpendableInsufficient):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"Not enough Spendable MCGP for this order.", "insufficient_balance")
	case errors.As(err, &limit):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, fmt.Sprintf(
			"This order is above the per-payment limit for Spendable MCGP (%s MCGP). Pay with USDC or USDT, or buy fewer items per order.",
			formatTokenBalance(limit.Max, 18)), "spendable_limit")
	case errors.Is(err, services.ErrSpendableOverTxCap):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"This order is above the per-payment limit for Spendable MCGP. Pay with USDC or USDT, or buy fewer items per order.", "spendable_limit")
	case errors.Is(err, services.ErrSpendableOverWindow):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable,
			"Spendable MCGP payments have reached their limit for now. Try again later, or pay with USDC or USDT.", "spendable_window")
	case errors.Is(err, services.ErrOrderMcgpMinimum):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"This amount is below the minimum Spendable MCGP payment. Pay with USDC or USDT.", "spendable_minimum")
	case errors.Is(err, services.ErrOrderMcgpPending):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"Your Spendable MCGP payment is still being confirmed. Check the order in a minute — you will not be charged twice.", "payment_pending")
	case errors.Is(err, services.ErrOrderNotPayable):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"This order can no longer be paid. If any MCGP was taken it is being returned to your Spendable balance.", "order_not_payable")
	case errors.Is(err, services.ErrOrderMcgpSellerWallet):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"The seller has no valid wallet to be paid into, so the payment cannot be released yet. Support can help resolve this order.", "seller_no_wallet")
	case errors.Is(err, services.ErrOrderMcgpPayoutBelowMin):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"This payout is below the minimum Spendable MCGP amount the platform can pay out. Your MCGP is still held; support has been alerted and will resolve the order.", "payout_below_minimum")
	case errors.Is(err, services.ErrOrderMcgpRetry):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"Your last payment attempt did not go through and nothing was taken. Tap Pay again.", "payment_retry")
	case errors.Is(err, services.ErrOrderMcgpWalletChanged):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"Your wallet changed since you first tried to pay this order. Cancel it and place the order again.", "wallet_changed")
	case errors.Is(err, services.ErrOrderMcgpEscrowUnknown):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable,
			"We could not confirm this order has not already been paid. Try again in a minute.", "escrow_unknown")
	case errors.Is(err, services.ErrOrderMcgpDisabled):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, mcgpCheckoutUnavailable, "mcgp_unavailable")
	case errors.Is(err, services.ErrOrderMcgpBadState), errors.Is(err, services.ErrOrderMcgpNotFound):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"This order changed while your request was being handled. Refresh the order and try again.", "order_changed")
	default:
		log.Printf("[checkout] spendable MCGP payment error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "The Spendable MCGP payment could not be completed. Try again.")
	}
}

// respondSpendableErr answers a release/refund error. An order another request
// already decided is not an error: it echoes the order's current status, and
// the caller must not repeat notifications or fee settlement.
func respondSpendableErr(c *gin.Context, orderID uuid.UUID, err error) {
	if !errors.Is(err, services.ErrOrderMcgpAlreadyDecided) {
		spendableFail(c, err)
		return
	}
	var order models.Order
	if dbErr := config.DB.First(&order, "id = ?", orderID).Error; dbErr != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load order")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Already done", gin.H{
		"orderId":        order.ID,
		"status":         order.Status,
		"alreadyDecided": true,
	})
}

// PaySpendable handles POST /api/orders/:id/pay-spendable — the buyer
// authorises paying an MCGP order from their Spendable MCGP. No wallet
// signature and no gas: the app gates this behind its authorization prompt,
// and the operator debit holds the MCGP until the order resolves. Safe to
// retry: the debit is keyed by the order.
func (ch *CheckoutHandler) PaySpendable(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !ch.spendableEnabled() {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, mcgpCheckoutUnavailable, "mcgp_unavailable")
		return
	}
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}
	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}
	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can pay this order")
		return
	}
	if order.Token != "MCGP" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Only MCGP orders are paid from Spendable MCGP")
		return
	}
	if order.Status == models.OrderStatusEscrowed && order.IsSpendableMCGP() {
		respondSpendablePaid(c, &order) // a retry of a payment that already went through
		return
	}
	if order.Status != models.OrderStatusPendingPayment {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			fmt.Sprintf("Cannot pay: order is in %s status", order.Status), "order_not_payable")
		return
	}
	// The operator debits this wallet with no signature from the user.
	buyerWallet, ok := requireProvenWallet(c, user)
	if !ok {
		return
	}
	// The hold is only ever released by crediting the seller: never take the
	// buyer's MCGP for a seller who cannot be paid (the escrow path refuses
	// the same in getOrderForEscrow).
	var seller models.User
	if err := config.DB.First(&seller, "id = ?", order.SellerID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Seller not found")
		return
	}
	if !common.IsHexAddress(strings.TrimSpace(seller.WalletAddress)) {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"This seller cannot receive payments yet. Nothing was charged.", "seller_no_wallet")
		return
	}
	// MCGP pays no platform or gas fee, so the hold is the order's total.
	amount := orderPull(&order, big.NewInt(0))
	if _, err := ch.spendable.Fund(c.Request.Context(), services.OrderMcgpFundInput{
		OrderID:     order.ID,
		BuyerID:     user.ID,
		BuyerWallet: buyerWallet,
		Amount:      amount,
	}); err != nil {
		spendableFail(c, err)
		return
	}
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load order")
		return
	}
	respondSpendablePaid(c, &order)
}

func respondSpendablePaid(c *gin.Context, order *models.Order) {
	utils.SuccessResponse(c, http.StatusOK, "Paid from Spendable MCGP", gin.H{
		"orderId":         order.ID,
		"status":          order.Status,
		"paymentSource":   order.PaymentSource,
		"escrowExpiresAt": order.EscrowExpiresAt,
	})
}

// ConfirmReceipt handles POST /api/orders/:id/confirm-receipt — the buyer
// confirms a Spendable MCGP order, releasing the held MCGP to the seller. The
// escrow counterpart is prepare-confirm + submit-confirm.
func (ch *CheckoutHandler) ConfirmReceipt(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}
	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}
	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can confirm receipt")
		return
	}
	if !order.IsSpendableMCGP() {
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			"This order is held in the escrow contract; confirm it with prepare-confirm.", "escrow_order")
		return
	}
	if order.DisputedAt != nil {
		utils.ErrorResponse(c, http.StatusConflict, "Order is under dispute. Contact admin to resolve.")
		return
	}
	if !isValidTransitionForKind(order.Status, models.OrderStatusCompleted, order.Kind) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot confirm receipt: order is in %s status", order.Status))
		return
	}

	now := time.Now()
	pay, err := ch.releaseSpendableToSeller(c.Request.Context(), &order, models.OrderMcgpReleaseBuyerConfirmed,
		map[string]interface{}{"status": models.OrderStatusCompleted, "buyer_confirmed_at": now}, false)
	if err != nil {
		respondSpendableErr(c, order.ID, err)
		return
	}
	ch.settleCompletedOrderFees(&order, now)
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCompleted,
		UserID:  order.SellerID,
		Title:   "Order Confirmed by Buyer",
		Message: fmt.Sprintf("The buyer confirmed order #%s. The payment is being released to your Spendable MCGP.", order.ID.String()[:8]),
		Data:    map[string]interface{}{"orderId": order.ID.String(), "status": models.OrderStatusCompleted},
	})
	utils.SuccessResponse(c, http.StatusOK, "Order completed", gin.H{
		"orderId":          order.ID,
		"status":           models.OrderStatusCompleted,
		"buyerConfirmedAt": now,
		"payoutStatus":     pay.Status,
	})
}

// releaseSpendableToSeller releases a Spendable MCGP hold to the seller as
// ProductEscrow's _releaseToSeller would: product + shipping to the seller,
// the platform fee (zero for MCGP) left with the platform. updates are applied
// to the order, guarded on the status and dispute state read, in the same
// transaction that claims the payment.
func (ch *CheckoutHandler) releaseSpendableToSeller(ctx context.Context, order *models.Order, reason string, updates map[string]interface{}, allowDisputed bool) (*models.OrderMcgpPayment, error) {
	if !ch.spendableEnabled() {
		return nil, services.ErrOrderMcgpDisabled
	}
	var seller models.User
	if err := config.DB.First(&seller, "id = ?", order.SellerID).Error; err != nil {
		return nil, fmt.Errorf("load seller: %w", err)
	}
	product, _ := new(big.Int).SetString(order.ProductAmount, 10)
	shipping, _ := new(big.Int).SetString(order.ShippingAmount, 10)
	if product == nil || shipping == nil {
		return nil, fmt.Errorf("order %s has non-numeric amounts", order.ID)
	}
	return ch.spendable.Release(ctx, order.ID, services.OrderMcgpReleasePlan{
		Reason:       reason,
		SellerWallet: seller.WalletAddress,
		SellerAmount: new(big.Int).Add(product, shipping),
	}, applyOrderOutcome(order, updates, allowDisputed, false))
}

// refundSpendableOrder returns a Spendable MCGP hold to the buyer (bucket for
// bucket), applying updates to the order in the claiming transaction and
// returning its stock when restock is set.
func (ch *CheckoutHandler) refundSpendableOrder(ctx context.Context, order *models.Order, updates map[string]interface{}, allowDisputed, restock bool) (*models.OrderMcgpPayment, error) {
	if !ch.spendableEnabled() {
		return nil, services.ErrOrderMcgpDisabled
	}
	return ch.spendable.Refund(ctx, order.ID, applyOrderOutcome(order, updates, allowDisputed, restock))
}

// applyOrderOutcome writes an order's outcome only if it is still in the
// status (and dispute state) the handler checked — a concurrent confirm and
// refund cannot both land.
func applyOrderOutcome(order *models.Order, updates map[string]interface{}, allowDisputed, restock bool) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		q := tx.Model(&models.Order{}).Where("id = ? AND status = ?", order.ID, order.Status)
		if !allowDisputed {
			q = q.Where("disputed_at IS NULL")
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return services.ErrOrderMcgpBadState
		}
		if restock {
			return services.RestoreOrderStock(tx, order)
		}
		return nil
	}
}

// settleCompletedOrderFees hands a completed order's realised merchant fee to
// the fee engine and stamps tp_distributed_at. Shared by the escrow confirm
// (SubmitConfirm) and the Spendable MCGP confirm.
func (ch *CheckoutHandler) settleCompletedOrderFees(order *models.Order, now time.Time) {
	if order.TPDistributedAt != nil {
		return
	}
	// v1 (legacy) settles on the BUNDLED system fee (2% merchant + $0.10 gas).
	// v2 carves the two apart: the 2% MERCHANT platform fee carries the
	// product_merchant split (cashback + TP); the buyer's stablecoin gas is a
	// SEPARATE 100%-system fee that must NOT generate TP/cashback. We feed the
	// merchant fee here; the gas component is intentionally not settled into the
	// engine (it would only ever credit system, producing no recipients).
	// A service order's fee is split under service_other's reward table.
	// order.PlatformFee holds what the escrow actually collected — the
	// product_merchant rate, the only one the contract applies
	// (splitSellerFee) — so the USD amount is read the same way for both.
	feeFeature := models.FeatureProductMerchant
	if order.Kind == models.OrderKindService {
		feeFeature = models.FeatureServiceOther
	}
	// Fee settlement: the realized merchant fee is handed to the engine
	// (async, idempotent on checkout/order.ID). tp_distributed_at is stamped as
	// a secondary site-level guard, but it is NOT the authoritative record of
	// distribution: the FeeDistribution guard row (keyed checkout/order.ID) is,
	// and the completed-order status transition already prevents re-entry here.
	// The enqueue retries + alerts on permanent failure
	// (fee_v2_settle_failed_total), so a stamped-but-undistributed window
	// requires all retries to fail — surfaced loudly rather than silent.
	if err := settleFeeEarnings(ch.FeeService, services.FeeEvent{
		Feature:    feeFeature,
		Token:      order.Token,
		FeeUSD:     orderMerchantFeeUSD(order),
		UserID:     order.BuyerID,
		SourceType: "checkout",
		SourceID:   order.ID,
	}); err != nil {
		log.Printf("TP distribution failed for order %s: %v", order.ID, err)
		return
	}
	if err := config.DB.Model(order).Update("tp_distributed_at", now).Error; err != nil {
		log.Printf("Failed to mark TP distributed for order %s: %v", order.ID, err)
	}
}

// notifyOrderResolved tells both parties how an admin resolved an order.
// txHash is empty for a Spendable MCGP order (the operator moved the funds).
func (ch *CheckoutHandler) notifyOrderResolved(order *models.Order, finalStatus string, refundBuyer bool, txHash string) {
	shortID := order.ID.String()[:8]
	outcome := "released to seller"
	if refundBuyer {
		outcome = "refunded to buyer"
	}
	for _, uid := range []uuid.UUID{order.BuyerID, order.SellerID} {
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderDisputeResolved,
			UserID:  uid,
			Title:   "Dispute Resolved",
			Message: fmt.Sprintf("Admin resolved order #%s — funds %s.", shortID, outcome),
			Data: map[string]interface{}{
				"orderId":     order.ID.String(),
				"status":      finalStatus,
				"refundBuyer": refundBuyer,
				"txHash":      txHash,
			},
		})
	}
}

// adminResolveSpendable resolves a Spendable MCGP order directly: there is no
// escrow transaction for an admin wallet to sign, so the decision is executed
// by the operator. Moving a user's MCGP is super_admin only, like the bills
// and IT settle/reverse actions.
func (ch *CheckoutHandler) adminResolveSpendable(c *gin.Context, user *models.User, order *models.Order, refundBuyer bool) {
	if user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Resolving a Spendable MCGP order moves the buyer's MCGP and needs a super admin")
		return
	}
	isDisputed := order.DisputedAt != nil
	finalStatus := models.OrderStatusCompleted
	if refundBuyer {
		finalStatus = models.OrderStatusRefunded
	}
	updates := map[string]interface{}{"status": finalStatus}
	if isDisputed {
		updates["disputed_at"] = nil
		updates["dispute_reason"] = ""
		updates["dispute_raised_by"] = ""
	}
	var err error
	if refundBuyer {
		_, err = ch.refundSpendableOrder(c.Request.Context(), order, updates, true, true)
	} else {
		_, err = ch.releaseSpendableToSeller(c.Request.Context(), order, models.OrderMcgpReleaseAdminResolved, updates, true)
	}
	if err != nil {
		respondSpendableErr(c, order.ID, err)
		return
	}
	log.Printf("[checkout] admin %s resolved spendable MCGP order %s refundBuyer=%v", user.ID, order.ID, refundBuyer)
	ch.notifyOrderResolved(order, finalStatus, refundBuyer, "")
	utils.SuccessResponse(c, http.StatusOK, "Dispute resolved", gin.H{
		"orderId":     order.ID,
		"status":      finalStatus,
		"refundBuyer": refundBuyer,
		"resolved":    true,
	})
}
