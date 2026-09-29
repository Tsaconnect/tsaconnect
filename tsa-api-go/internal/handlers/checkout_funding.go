package handlers

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// checkout_funding.go — "pay with another token" at marketplace checkout.
//
// The routes drive services.MarketplaceFundingService: a quote, a start that
// returns the LiFi route she signs, the swap's hash, a confirm that proves the
// delivery to her own wallet on Sonic, and the two gas top-ups. The escrow is
// then paid through the unchanged prepare-approve / prepare-escrow /
// submit-escrow routes. Every amount is the service's own, sized from the
// checkout group's orders; the body names only the token she pays with.

// marketplaceFunding is the port *services.MarketplaceFundingService fills.
type marketplaceFunding interface {
	Enabled() bool
	Quote(ctx context.Context, user *models.User, groupID uuid.UUID, src services.FundingSource) (*services.FundingQuote, error)
	QuoteTotal(ctx context.Context, user *models.User, symbol string, total *big.Int, orders int, src services.FundingSource) (*services.FundingQuote, error)
	CanStartCheckout(ctx context.Context, userID uuid.UUID) error
	Start(ctx context.Context, user *models.User, groupID uuid.UUID, src services.FundingSource) (*services.FundingStart, error)
	Get(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error)
	Open(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error)
	RecordSwap(ctx context.Context, user *models.User, id uuid.UUID, txHash string) error
	Confirm(ctx context.Context, user *models.User, id uuid.UUID) (*services.FundingStatus, error)
	Abandon(ctx context.Context, user *models.User, id uuid.UUID) error
	SourceGasTopUp(ctx context.Context, user *models.User, id uuid.UUID) (services.GasTopUpResult, error)
	SonicGasTopUp(ctx context.Context, user *models.User, id uuid.UUID) (services.GasTopUpResult, error)
}

// WithFunding turns the funding routes on. Pass a nil interface to leave them
// off, never a typed nil.
func (ch *CheckoutHandler) WithFunding(f marketplaceFunding) *CheckoutHandler {
	ch.funding = f
	return ch
}

// FundingEnabled reports the funding routes are registered.
func (ch *CheckoutHandler) FundingEnabled() bool {
	return ch != nil && ch.funding != nil && ch.funding.Enabled()
}

// GroupPull is what the escrow pulls from the buyer for a checkout's orders:
// each total plus the escrow's gas fee, read exactly as prepare-approve reads
// it, with the rates resolved once per token (services.OrderPullPricer).
func (ch *CheckoutHandler) GroupPull(ctx context.Context, orders []models.Order) (*big.Int, error) {
	gasFee := map[string]*big.Int{}
	total := new(big.Int)
	for i := range orders {
		token := strings.ToUpper(orders[i].Token)
		fee, ok := gasFee[token]
		if !ok {
			rates, perr := ch.resolvePricingRates(ctx, token)
			if perr != nil {
				return nil, errors.New(perr.msg)
			}
			fee = rates.gasFee
			gasFee[token] = fee
		}
		total.Add(total, orderPull(&orders[i], fee))
	}
	return total, nil
}

type fundingBody struct {
	GroupID       string `json:"groupId"`
	SourceChainID int64  `json:"sourceChainId"`
	SourceToken   string `json:"sourceToken"`
	// SourceAddress is her address on the source chain — her EVM wallet, the
	// same one the escrow pulls from. Optional: unset, the proven wallet.
	SourceAddress string `json:"sourceAddress"`
	// WalletAddress is the device wallet, reconciled like every write
	// (callerWallet); it must be the account's PROVEN wallet.
	WalletAddress string `json:"walletAddress"`
}

// fundingCaller authenticates, and requires the proven linked wallet the swap
// is sent from and the funds land in. A source address other than it is
// refused: she pays from her own wallet, and nothing else.
func (ch *CheckoutHandler) fundingCaller(c *gin.Context, body *fundingBody) (*models.User, uuid.UUID, services.FundingSource, bool) {
	var src services.FundingSource
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, uuid.Nil, src, false
	}
	if err := c.ShouldBindJSON(body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return nil, uuid.Nil, src, false
	}
	// Reconcile and prove the wallet before anything else is read.
	groupID, err := uuid.Parse(strings.TrimSpace(body.GroupID))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "groupId is required")
		return nil, uuid.Nil, src, false
	}
	src, ok := fundingSource(c, user, body)
	if !ok {
		return nil, uuid.Nil, src, false
	}
	return user, groupID, src, true
}

// fundingSource is the token she pays with, from her PROVEN wallet: the device
// wallet is reconciled (callerWallet — links only with proof), the account's
// wallet must be proven (requireProvenWallet, as for every operator debit),
// and a source address other than it is refused.
func fundingSource(c *gin.Context, user *models.User, body *fundingBody) (services.FundingSource, bool) {
	var src services.FundingSource
	if body.SourceChainID <= 0 || strings.TrimSpace(body.SourceToken) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "sourceChainId and sourceToken are required")
		return src, false
	}
	if _, ok := callerWallet(c, user, body.WalletAddress); !ok {
		return src, false
	}
	wallet, ok := requireProvenWallet(c, user)
	if !ok {
		return src, false
	}
	addr := strings.TrimSpace(body.SourceAddress)
	if addr == "" {
		addr = wallet
	}
	if !strings.EqualFold(addr, wallet) {
		utils.ErrorResponse(c, http.StatusForbidden, services.ErrFundingSource.Error())
		return src, false
	}
	return services.FundingSource{ChainID: body.SourceChainID, Token: strings.TrimSpace(body.SourceToken), Address: wallet}, true
}

// POST /api/orders/funding/quote — what paying the checkout with one token costs.
func (ch *CheckoutHandler) FundingQuote(c *gin.Context) {
	var body fundingBody
	user, groupID, src, ok := ch.fundingCaller(c, &body)
	if !ok {
		return
	}
	q, err := ch.funding.Quote(c.Request.Context(), user, groupID, src)
	if err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Funding quote", q)
}

// POST /api/orders/funding/cart-quote — what paying the ACTIVE CART with one
// token would cost, before any order exists (and takes stock). The body is the
// cart quote's (token, location, tiers, delivery address) plus the source; the
// total is priced by the same code that will create the orders.
func (ch *CheckoutHandler) FundingCartQuote(c *gin.Context) {
	in, ok := loadCartCheckout(c)
	if !ok {
		return
	}
	var body fundingBody
	if err := c.ShouldBindBodyWith(&body, binding.JSON); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if in.token == "MCGP" {
		fundingError(c, services.ErrFundingGroup)
		return
	}
	src, ok := fundingSource(c, in.user, &body)
	if !ok {
		return
	}
	lines, rates, ok := ch.priceActiveCart(c, in)
	if !ok {
		return
	}
	q, err := ch.funding.QuoteTotal(c.Request.Context(), in.user, in.token, cartTotal(lines, rates.gasFee), len(lines), src)
	if err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Funding quote", q)
}

// POST /api/orders/funding/intents — start the funding: returns the route she
// signs. gasTopUp (source chain) is the server's verdict, as for a sale.
func (ch *CheckoutHandler) StartFunding(c *gin.Context) {
	var body fundingBody
	user, groupID, src, ok := ch.fundingCaller(c, &body)
	if !ok {
		return
	}
	started, err := ch.funding.Start(c.Request.Context(), user, groupID, src)
	if err != nil {
		if errors.Is(err, services.ErrSellIntentOpen) {
			open, _ := ch.funding.Open(c.Request.Context(), user.ID)
			c.JSON(http.StatusConflict, gin.H{
				"success": false, "message": "You're already paying for a checkout with another token", "code": "funding_in_progress",
				"data": gin.H{"intent": fundingIntentView(open)},
			})
			return
		}
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Funding started — sign the swap", gin.H{
		"intent":    fundingIntentView(started.Intent),
		"lifiQuote": started.Swap.LiFiQuote,
		"gasTopUp":  started.Swap.GasTopUp,
		"quote":     started.Quote,
	})
}

// GET /api/orders/funding/intents/open — her funding still in progress, if any.
func (ch *CheckoutHandler) OpenFunding(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	in, err := ch.funding.Open(c.Request.Context(), user.ID)
	if err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Funding in progress", gin.H{"intent": fundingIntentView(in)})
}

// GET /api/orders/funding/intents/:id
func (ch *CheckoutHandler) GetFunding(c *gin.Context) {
	user, id, ok := fundingTarget(c)
	if !ok {
		return
	}
	in, err := ch.funding.Get(c.Request.Context(), user.ID, id)
	if err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Funding", gin.H{"intent": fundingIntentView(in)})
}

// POST /api/orders/funding/intents/:id/swap
func (ch *CheckoutHandler) RecordFundingSwap(c *gin.Context) {
	user, id, ok := fundingTarget(c)
	if !ok {
		return
	}
	var body struct {
		TxHash string `json:"txHash"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := ch.funding.RecordSwap(c.Request.Context(), user, id, body.TxHash); err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Swap recorded", nil)
}

// POST /api/orders/funding/intents/:id/confirm — has it landed in her wallet?
// 200 delivered (with needsGas when the S top-up is still owed); 409
// swap_pending to wait; 409 swap_failed / swap_refunded / swap_partial when it
// ended short (the intent is closed, the orders simply expire).
func (ch *CheckoutHandler) ConfirmFunding(c *gin.Context) {
	user, id, ok := fundingTarget(c)
	if !ok {
		return
	}
	st, err := ch.funding.Confirm(c.Request.Context(), user, id)
	if err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Your funds are in your wallet", gin.H{
		"intent": fundingIntentView(st.Intent), "destTxHash": st.DestTxHash, "needsGas": st.NeedsGas,
	})
}

// POST /api/orders/funding/intents/:id/abandon — only before the swap is sent.
func (ch *CheckoutHandler) AbandonFunding(c *gin.Context) {
	user, id, ok := fundingTarget(c)
	if !ok {
		return
	}
	if err := ch.funding.Abandon(c.Request.Context(), user, id); err != nil {
		fundingError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Cancelled", nil)
}

// POST /api/orders/funding/intents/:id/gas-topup — source-chain gas for her
// swap, exactly as a sale's (only when the quote budgeted it). Blocks until
// the gas lands (<=30s).
func (ch *CheckoutHandler) FundingSourceGas(c *gin.Context) {
	user, id, ok := fundingTarget(c)
	if !ok {
		return
	}
	res, err := ch.funding.SourceGasTopUp(c.Request.Context(), user, id)
	if err != nil {
		fundingGasError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Gas ready", res)
}

// POST /api/orders/funding/intents/:id/sonic-gas — S for her escrow txs once
// the funds have landed. Skipped when she already holds enough.
func (ch *CheckoutHandler) FundingSonicGas(c *gin.Context) {
	user, id, ok := fundingTarget(c)
	if !ok {
		return
	}
	res, err := ch.funding.SonicGasTopUp(c.Request.Context(), user, id)
	if err != nil {
		fundingGasError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Gas ready", res)
}

func fundingTarget(c *gin.Context) (*models.User, uuid.UUID, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid funding id")
		return nil, uuid.Nil, false
	}
	return user, id, true
}

// fundingIntentView is what the app is told about a funding intent: its own
// progress, nothing of the bank fields a sale's row carries.
func fundingIntentView(in *models.RampSellIntent) gin.H {
	if in == nil {
		return nil
	}
	return gin.H{
		"id": in.ID, "status": in.Status, "purpose": in.Purpose, "groupId": in.ReferenceID,
		"sourceChainId": in.SourceChainID, "sourceTokenAddress": in.SourceTokenAddress, "sourceAmount": in.SourceAmount,
		"sourceAddress": in.SourceAddress, "destChainId": in.DestChainID, "destTokenAddress": in.DestTokenAddress,
		"destAmount": in.DestAmount, "swapTxHash": in.SwapTxHash, "destGasTopUp": in.DestTopUpBudgeted,
		"destGasSent": in.DestTopUpTxHash != "", "createdAt": in.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// fundingError maps the funding's refusals. The codes are what the app acts
// on; the messages are copy.
func fundingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrFundingTooCostly):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "funding_too_costly")
	case errors.Is(err, services.ErrFundingNotNeeded):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "funding_not_needed")
	case errors.Is(err, services.ErrFundingNeedsGas):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, services.ErrFundingNeedsGas.Error(), "funding_needs_gas")
	case errors.Is(err, services.ErrFundingTooSmall):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "funding_too_small")
	case errors.Is(err, services.ErrFundingKeepS):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "funding_keep_s")
	case errors.Is(err, services.ErrFundingKYC):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), services.KYCVerificationRequiredCode)
	case errors.Is(err, services.ErrFundingInProgress), errors.Is(err, services.ErrSellIntentOpen):
		utils.ErrorResponseWithCode(c, http.StatusConflict, services.ErrFundingInProgress.Error(), "funding_in_progress")
	case errors.Is(err, services.ErrFundingSameToken), errors.Is(err, services.ErrFundingSource),
		errors.Is(err, services.ErrSellWalletMCGP):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrFundingGroup):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "order_not_payable")
	case errors.Is(err, services.ErrSwapNotLanded):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Still swapping — try again in a moment", sellCodeSwapPending)
	case errors.Is(err, services.ErrSwapFailed):
		utils.ErrorResponseWithCode(c, http.StatusConflict,
			err.Error()+" — nothing was paid; whatever did not land is in or back in your wallet", swapEndedCode(err))
	case errors.Is(err, services.ErrSwapMismatch):
		utils.ErrorResponse(c, http.StatusConflict, "This swap doesn't match your checkout — contact support")
	case errors.Is(err, services.ErrSellIntentNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "That payment can't be found")
	case errors.Is(err, services.ErrSellIntentState):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrKYCVerificationRequired):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), "kyc_verification_required")
	case errors.Is(err, services.ErrFundingUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrFundingUnavailable.Error(), "funding_unavailable")
	default:
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrFundingUnavailable.Error(), "funding_unavailable")
	}
}

// fundingGasError maps a top-up's refusal. A destination top-up we cannot
// send is said plainly: her funds are in her wallet whatever happens.
func fundingGasError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrDestGasUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrDestGasUnavailable.Error(), "gas_unavailable")
	case errors.Is(err, services.ErrSellIntentNotFound), errors.Is(err, services.ErrSellIntentState),
		errors.Is(err, services.ErrFundingUnavailable):
		fundingError(c, err)
	default:
		sponsorshipError(c, err)
	}
}
