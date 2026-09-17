package handlers

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// paycrest_handler.go — the app-facing ramp.
//
// The user's own connected wallet is the delivery target on a buy and the
// funding source on a sell, so every mutating route reconciles the address the
// device claims against the one registered to the account (callerWallet). A
// device holding a different key must not be able to point a purchase at it.

type PaycrestHandler struct {
	orders  *services.PaycrestOrderService
	quotes  *services.PaycrestQuoteService
	dir     *services.PaycrestDirectory
	sponsor rampSponsorshipPort // nil unless RAMP_GAS_SPONSORSHIP is on
}

func NewPaycrestHandler(orders *services.PaycrestOrderService, quotes *services.PaycrestQuoteService, dir *services.PaycrestDirectory) *PaycrestHandler {
	return &PaycrestHandler{orders: orders, quotes: quotes, dir: dir}
}

// Enabled reports whether the routes should be registered at all.
func (h *PaycrestHandler) Enabled() bool {
	return h != nil && h.orders != nil && h.quotes != nil
}

// paycrestError maps service sentinels onto status codes. Everything a user
// can act on is a 4xx with the sentinel's own wording — these strings are
// written to be shown, not logged.
func paycrestError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrKYCVerificationRequired):
		// Buy only (money in). The machine code is what the app routes on:
		// verify now, or contact support for a verified user with no legal name.
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), kycRefusalCode(err))
	case errors.Is(err, services.ErrPaycrestDisabled):
		utils.ErrorResponse(c, http.StatusServiceUnavailable,
			"Bank transfers are temporarily unavailable")
	case errors.Is(err, services.ErrPaycrestBelowMinimum):
		utils.ErrorResponse(c, http.StatusBadRequest,
			"That amount is below the minimum for this route. Try a larger amount.")
	case errors.Is(err, services.ErrPaycrestLegClosed):
		// Ordered BEFORE the no-venue case: a closed direction is also a
		// no-venue condition, and the difference is what the user should do
		// about it. Nothing — so do not invite a retry with another amount.
		utils.ErrorResponse(c, http.StatusConflict,
			"This currency can't be traded in that direction right now. Try the other direction, or another currency.")
	case errors.Is(err, services.ErrPaycrestNoVenue), errors.Is(err, services.ErrPaycrestNoProvider):
		// Liquidity, not breakage. Say so plainly so the user retries rather
		// than assuming the feature is broken.
		utils.ErrorResponse(c, http.StatusConflict,
			"No provider can fill that amount right now. Try a different amount or network.")
	case errors.Is(err, services.ErrConversionUnavailable):
		utils.ErrorResponse(c, http.StatusConflict,
			"We can't deliver to that network right now. Try a supported network, or try again shortly.")
	case errors.Is(err, services.ErrPaycrestOrderOpen):
		utils.ErrorResponse(c, http.StatusConflict,
			"You already have an order awaiting payment. Finish it, or cancel it from your orders.")
	case errors.Is(err, services.ErrPaycrestNotCancellable):
		// Deliberately names the payment as the reason, because the common case
		// is a user who already sent and is now trying to undo it — which is
		// the one thing cancelling must not appear to do.
		utils.ErrorResponse(c, http.StatusConflict,
			"This order can no longer be cancelled. If you have already sent the payment, contact support instead.")
	case errors.Is(err, services.ErrPaycrestAccountShape):
		utils.ErrorResponse(c, http.StatusBadRequest,
			"That account number doesn't look right for the destination you chose. Check it and try again.")
	case errors.Is(err, services.ErrPaycrestBankUnsupported):
		utils.ErrorResponse(c, http.StatusBadRequest,
			"That bank can't receive payouts on this route. Choose another account.")
	case errors.Is(err, services.ErrPaycrestOrderNotYours), errors.Is(err, services.ErrPaycrestOrderNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
	default:
		utils.ErrorResponse(c, http.StatusBadGateway,
			"Could not reach the transfer provider. Please try again.")
	}
}

// GET /api/paycrest/quote?side=buy&amount=1000&currency=NGN[&requestedChain=&requestedToken=]
//
// Returns only venues that can actually fill this amount right now, cheapest
// first. The app renders this as the network picker, so a network the user can
// see is always one the order will succeed on.
//
// requestedChain/requestedToken (tsa-app/services/paycrestApi.ts getQuote,
// mirroring the POST /buy body) name the pair the user actually wants
// delivered. When Paycrest cannot fill that pair directly, a converted venue
// (Phase 2) is appended alongside the direct ones — see ConvertedVenues.
// Conversion is a buy-only capability (paycrestSellBody has no requested
// fields), so a sell quote never gains one even if the params are sent.
//
// This is purely additive: with no requestedChain/requestedToken, or with
// RAMP_CONVERSION_BUY off (ConvertedVenues is then a documented no-op, since
// SetLiFiQuoter/SetTreasuryAddress/SetChainRegistry were never called), the
// response is byte-identical to what this handler returned before converted
// buys existed. A LiFi failure or an unservable chain never fails the quote —
// it just means no converted venue is on offer today.
func (h *PaycrestHandler) Quote(c *gin.Context) {
	side := c.DefaultQuery("side", "buy")
	amount := c.Query("amount")
	currency := c.DefaultQuery("currency", "NGN")
	if amount == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount is required")
		return
	}
	res, err := h.quotes.Quote(c.Request.Context(), side, currency, amount)
	if err != nil {
		paycrestError(c, err)
		return
	}

	requestedChain := strings.TrimSpace(c.Query("requestedChain"))
	requestedToken := strings.TrimSpace(c.Query("requestedToken"))
	if res.Side == "buy" && requestedChain != "" && requestedToken != "" {
		converted, cerr := h.quotes.ConvertedVenues(c.Request.Context(), res.Side, res.FiatCode, res.FiatAmount,
			[]services.RequestedAsset{{Chain: requestedChain, Token: requestedToken}})
		if cerr == nil {
			res.Venues = append(res.Venues, converted...)
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Quote", res)
}

// GET /api/paycrest/banks — payout destinations that can actually receive on
// this route. NGN returns NIBSS codes the app already speaks; every other
// currency passes Paycrest's own institution codes through unchanged.
func (h *PaycrestHandler) Banks(c *gin.Context) {
	if h.dir == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Bank list unavailable")
		return
	}
	currency := c.DefaultQuery("currency", "NGN")
	opts, err := h.dir.Institutions(c.Request.Context(), currency)
	if err != nil {
		paycrestError(c, err)
		return
	}
	codes := make([]string, 0, len(opts))
	for _, o := range opts {
		codes = append(codes, o.Code)
	}

	// Unmatched only ever means anything for NGN, where the matcher can fail
	// to tie a Paycrest bank to a NIBSS code — that's deliberately still in
	// the response, not just a log line, so a gap in OUR matcher (e.g. OPay,
	// PalmPay, Moniepoint) is visible rather than silent. Every other
	// currency has no local list to match against, so nothing is ever
	// "unmatched" there and the field stays an empty array rather than
	// implying a matching step that didn't run.
	type bank struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	unmatched := []bank{}
	if strings.EqualFold(strings.TrimSpace(currency), "NGN") || currency == "" {
		m, err := h.dir.BankMap(c.Request.Context(), currency)
		if err != nil {
			paycrestError(c, err)
			return
		}
		for _, u := range m.Unmatched {
			unmatched = append(unmatched, bank{Code: u.Code, Name: u.Name})
		}
		sort.Slice(unmatched, func(i, j int) bool { return unmatched[i].Name < unmatched[j].Name })
	}

	utils.SuccessResponse(c, http.StatusOK, "Supported banks", gin.H{
		"bankCodes": codes, // kept for the existing client
		"banks":     opts,
		"unmatched": unmatched,
	})
}

type paycrestBuyBody struct {
	Amount        string `json:"amount" binding:"required"`
	Currency      string `json:"currency"`
	Chain         string `json:"chain" binding:"required"`
	Token         string `json:"token" binding:"required"`
	WalletAddress string `json:"walletAddress"`
	// RequestedChain/RequestedToken are what the user actually wants
	// delivered — omitted (or equal to Chain/Token) means a direct buy.
	// Defaulted to Chain/Token below so an older app build that has never
	// heard of a converted buy keeps getting exactly what it asks for.
	RequestedChain string `json:"requestedChain"`
	RequestedToken string `json:"requestedToken"`
	// Refund destination — where naira returns if the order fails. The app
	// prefills this from the user's saved payout account.
	RefundBankCode    string `json:"refundBankCode" binding:"required"`
	RefundAccount     string `json:"refundAccount" binding:"required"`
	RefundAccountName string `json:"refundAccountName"`
}

// POST /api/paycrest/buy — opens an onramp and returns the provider's bank
// account for the user to pay.
func (h *PaycrestHandler) Buy(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	var body paycrestBuyBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	wallet, ok := callerWallet(c, user, body.WalletAddress)
	if !ok {
		return
	}
	// Default to the fillable route itself, so an app build that has never
	// heard of a converted buy (or one that simply didn't set these) still
	// gets a plain direct order, unchanged.
	requestedChain := body.RequestedChain
	if requestedChain == "" {
		requestedChain = body.Chain
	}
	requestedToken := body.RequestedToken
	if requestedToken == "" {
		requestedToken = body.Token
	}
	row, err := h.orders.CreateBuy(c.Request.Context(), services.PaycrestBuyRequest{
		UserID:            user.ID,
		WalletAddress:     wallet,
		FiatCode:          body.Currency,
		FiatAmount:        body.Amount,
		ChainKey:          body.Chain,
		Token:             body.Token,
		RequestedChain:    requestedChain,
		RequestedToken:    requestedToken,
		RefundBankCode:    body.RefundBankCode,
		RefundAccount:     body.RefundAccount,
		RefundAccountName: body.RefundAccountName,
	})
	if err != nil {
		paycrestError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Transfer to the account shown to complete your purchase", row)
}

type paycrestSellBody struct {
	Amount        string `json:"amount" binding:"required"`
	Currency      string `json:"currency"`
	Chain         string `json:"chain" binding:"required"`
	Token         string `json:"token" binding:"required"`
	WalletAddress string `json:"walletAddress"`
	BankCode      string `json:"bankCode" binding:"required"`
	AccountNumber string `json:"accountNumber" binding:"required"`
	AccountName   string `json:"accountName"`
}

// POST /api/paycrest/sell — opens an offramp and returns the address the user
// must send stablecoin to from their own wallet.
func (h *PaycrestHandler) Sell(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	var body paycrestSellBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	wallet, ok := callerWallet(c, user, body.WalletAddress)
	if !ok {
		return
	}
	row, err := h.orders.CreateSell(c.Request.Context(), services.PaycrestSellRequest{
		UserID:        user.ID,
		WalletAddress: wallet,
		FiatCode:      body.Currency,
		FiatAmount:    body.Amount,
		ChainKey:      body.Chain,
		Token:         body.Token,
		BankCode:      body.BankCode,
		AccountNo:     body.AccountNumber,
		AccountName:   body.AccountName,
	})
	if err != nil {
		paycrestError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Send the amount shown to complete your sale", row)
}

// GET /api/paycrest/orders/:id — a single order, refreshed from the provider.
//
// The refresh is deliberate: with no webhook endpoint yet, a user opening this
// screen is the fastest signal we get that something has moved.
func (h *PaycrestHandler) GetOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	row, err := h.orders.Get(c.Request.Context(), user.ID, id)
	if err != nil {
		paycrestError(c, err)
		return
	}
	// A sync failure must not hide the order we already have.
	if fresh, err := h.orders.Sync(c.Request.Context(), row); err == nil && fresh != nil {
		row = fresh
	}
	utils.SuccessResponse(c, http.StatusOK, "Order", row)
}

// POST /api/paycrest/orders/:id/claim-payment — the user asserts they
// completed their side (bank transfer on a buy, on-chain send on a sell).
//
// This does not move the order's status — only the next Sync does that,
// via mapPaycrestStatus — it just records the claim so a genuine
// unmatched-deposit ALARM (paid, bank confirmed, protocol never registered
// it) can be told apart from an order the user simply abandoned before
// paying, which otherwise looks identical once it expires. Idempotent and
// scoped to the caller's own order.
func (h *PaycrestHandler) ClaimPayment(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	row, err := h.orders.ClaimPayment(c.Request.Context(), user.ID, id)
	if err != nil {
		paycrestError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payment claim recorded", row)
}

// POST /api/paycrest/orders/:id/cancel — the user abandons an order they have
// not paid or funded.
//
// Local only: Paycrest has no cancel endpoint, so their order runs to its own
// expiry regardless. What this releases is the one-open-order lock, which
// otherwise holds the user for the full validUntil window.
func (h *PaycrestHandler) Cancel(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return
	}
	row, err := h.orders.Cancel(c.Request.Context(), user.ID, id)
	if err != nil {
		paycrestError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Order cancelled", row)
}

// GET /api/paycrest/orders — the user's recent orders.
func (h *PaycrestHandler) ListOrders(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	rows, err := h.orders.List(c.Request.Context(), user.ID, limit)
	if err != nil {
		paycrestError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Orders", rows)
}
