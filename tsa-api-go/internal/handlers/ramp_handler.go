package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// rampRoutes is the slice of *ramp.Router the HTTP layer uses.
type rampRoutes interface {
	Quote(ctx context.Context, r ramp.Request) (*ramp.QuoteResult, error)
	Open(ctx context.Context, r ramp.OpenRequest) (*ramp.Order, error)
	Get(ctx context.Context, userID uuid.UUID, id string) (*ramp.Order, error)
	List(ctx context.Context, userID uuid.UUID, limit int) ([]ramp.Order, error)
	ClaimPayment(ctx context.Context, userID uuid.UUID, id string) (*ramp.Order, error)
	Cancel(ctx context.Context, userID uuid.UUID, id string) (*ramp.Order, error)
}

// RampHandler serves /api/ramp/* — provider-neutral buy/sell. Order bodies
// are exactly /api/paycrest/*'s (orderForCaller), ids unchanged.
type RampHandler struct {
	router rampRoutes
}

// The screens keep /paycrest/banks in v1 (payout institutions are Paycrest's
// vocabulary today), so there is no /ramp/banks yet.
func NewRampHandler(router rampRoutes) *RampHandler {
	if router == nil {
		return nil
	}
	return &RampHandler{router: router}
}

// The existing bodies plus the quote she confirmed. Embedded so the field
// list is not copied: JSON flattens it and binding still validates it.
type rampBuyBody struct {
	paycrestBuyBody
	QuotedRate string `json:"quotedRate"`
	Handle     string `json:"handle"`
}

type rampSellBody struct {
	paycrestSellBody
	QuotedRate string `json:"quotedRate"`
	Handle     string `json:"handle"`
}

// fiatCodeRE: a currency reaches log lines and provider calls verbatim, so
// anything but three letters is refused at the door. Empty means NGN.
var fiatCodeRE = regexp.MustCompile(`^[A-Za-z]{3}$`)

func validFiatCode(code string) bool {
	code = strings.TrimSpace(code)
	return code == "" || fiatCodeRE.MatchString(code)
}

const badFiatCodeMsg = "currency must be a three-letter code"

// GET /api/ramp/quote — public, like /paycrest/quote.
func (h *RampHandler) Quote(c *gin.Context) {
	side := ramp.Side(c.DefaultQuery("side", "buy"))
	if side != ramp.Buy && side != ramp.Sell {
		utils.ErrorResponse(c, http.StatusBadRequest, "side must be buy or sell")
		return
	}
	amount := strings.TrimSpace(c.Query("amount"))
	if amount == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount is required")
		return
	}
	currency := c.DefaultQuery("currency", "NGN")
	if !validFiatCode(currency) {
		utils.ErrorResponse(c, http.StatusBadRequest, badFiatCodeMsg)
		return
	}
	res, err := h.router.Quote(c.Request.Context(), ramp.Request{
		Side: side, FiatCode: currency, FiatAmount: amount,
		Token: c.Query("token"), ChainKey: c.Query("chain"),
	})
	if err != nil {
		rampError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Quote", res)
}

// POST /api/ramp/buy — a DIRECT buy only; the converted path stays on
// /api/paycrest/buy in v1.
func (h *RampHandler) Buy(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	var body rampBuyBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if !validFiatCode(body.Currency) {
		utils.ErrorResponse(c, http.StatusBadRequest, badFiatCodeMsg)
		return
	}
	if strings.TrimSpace(body.QuotedRate) == "" {
		rampError(c, ramp.ErrInvalidQuote)
		return
	}
	if isConvertedBuyBody(body.paycrestBuyBody) {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"This purchase needs the conversion route. Update the app and try again.", "use_paycrest_route")
		return
	}
	wallet, ok := callerWallet(c, user, body.WalletAddress)
	if !ok {
		return
	}
	o, err := h.router.Open(c.Request.Context(), ramp.OpenRequest{
		Request: ramp.Request{Side: ramp.Buy, FiatCode: body.Currency, FiatAmount: body.Amount,
			Token: body.Token, ChainKey: body.Chain},
		Handle: body.Handle, QuotedRate: body.QuotedRate,
		Input: ramp.OrderInput{UserID: user.ID, WalletAddress: wallet,
			RefundBankCode: body.RefundBankCode, RefundAccount: body.RefundAccount, RefundAccountName: body.RefundAccountName},
	})
	if err != nil {
		rampError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Transfer to the account shown to complete your purchase", rampOrderView(o))
}

// isConvertedBuyBody reports a body asking for something other than what the venue
// fills — the converted buy, which v1 leaves on /api/paycrest/buy.
func isConvertedBuyBody(b paycrestBuyBody) bool {
	return b.RequestedChainID != 0 ||
		strings.TrimSpace(b.RequestedTokenAddress) != "" ||
		strings.TrimSpace(b.DestinationAddress) != "" ||
		(b.RequestedChain != "" && !strings.EqualFold(b.RequestedChain, b.Chain)) ||
		(b.RequestedToken != "" && !strings.EqualFold(b.RequestedToken, b.Token))
}

// POST /api/ramp/sell.
func (h *RampHandler) Sell(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	var body rampSellBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if !validFiatCode(body.Currency) {
		utils.ErrorResponse(c, http.StatusBadRequest, badFiatCodeMsg)
		return
	}
	if strings.TrimSpace(body.QuotedRate) == "" {
		rampError(c, ramp.ErrInvalidQuote)
		return
	}
	wallet, ok := callerWallet(c, user, body.WalletAddress)
	if !ok {
		return
	}
	o, err := h.router.Open(c.Request.Context(), ramp.OpenRequest{
		Request: ramp.Request{Side: ramp.Sell, FiatCode: body.Currency, FiatAmount: body.Amount,
			Token: body.Token, ChainKey: body.Chain},
		Handle: body.Handle, QuotedRate: body.QuotedRate,
		Input: ramp.OrderInput{UserID: user.ID, WalletAddress: wallet,
			BankCode: body.BankCode, AccountNo: body.AccountNumber, AccountName: body.AccountName},
	})
	if err != nil {
		rampError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Send the amount shown to complete your sale", rampOrderView(o))
}

func (h *RampHandler) GetOrder(c *gin.Context) {
	h.orderAction(c, "Order", h.router.Get)
}

func (h *RampHandler) ClaimPayment(c *gin.Context) {
	h.orderAction(c, "Payment claim recorded", h.router.ClaimPayment)
}

func (h *RampHandler) Cancel(c *gin.Context) {
	h.orderAction(c, "Order cancelled", h.router.Cancel)
}

func (h *RampHandler) orderAction(c *gin.Context, msg string,
	fn func(context.Context, uuid.UUID, string) (*ramp.Order, error)) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	o, err := fn(c.Request.Context(), user.ID, c.Param("id"))
	if err != nil {
		rampError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, msg, rampOrderView(o))
}

func (h *RampHandler) ListOrders(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	orders, err := h.router.List(c.Request.Context(), user.ID, limit)
	if err != nil {
		rampError(c, err)
		return
	}
	rows := make([]models.PaycrestOrderRecord, 0, len(orders))
	for _, o := range orders {
		if o.Paycrest != nil {
			rows = append(rows, *o.Paycrest)
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Orders", ordersForCaller(rows))
}

// rampOrderView is the order body: today's Paycrest view (TSA Pay masking
// included), id unchanged — or the IT view below.
func rampOrderView(o *ramp.Order) any {
	switch {
	case o == nil:
		return nil
	case o.IT != nil:
		return itOrderBody(o)
	case o.Paycrest != nil:
		return orderForCaller(o.Paycrest)
	}
	return nil
}

// rampPayInto is where to send the naira for an IT-backed buy.
type rampPayInto struct {
	Bank             string `json:"bank"`
	AccountName      string `json:"accountName"`
	AccountNumber    string `json:"accountNumber"`
	AmountNgn        string `json:"amountNgn,omitempty"`
	PayerInstruction string `json:"payerInstruction,omitempty"`
}

// itOrderView has the Paycrest view's shape (spec §4.4) plus the provider, the
// pay-into card, the honour window, whether it was re-priced, the delivery
// hash, and the quoted breakdown (§14). The id is the bare intent UUID.
type itOrderView struct {
	ID            string          `json:"id"`
	Provider      string          `json:"provider"`
	Direction     string          `json:"direction"`
	Status        string          `json:"status"`
	Currency      string          `json:"currency"`
	Amount        string          `json:"amount"`
	Token         string          `json:"token"`
	Chain         string          `json:"chain"`
	Rate          string          `json:"rate"`
	EffectiveRate string          `json:"effectiveRate"`
	TokenAmount   string          `json:"tokenAmount"`
	Fees          []ramp.VenueFee `json:"fees"`
	WalletAddress string          `json:"walletAddress"`
	FailureReason string          `json:"failureReason,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	PayInto       *rampPayInto    `json:"payInto,omitempty"`
	HonourUntil   *time.Time      `json:"honourUntil,omitempty"`
	Repriced      bool            `json:"repriced"`
	TxHash        string          `json:"txHash,omitempty"`
}

func itOrderBody(o *ramp.Order) itOrderView {
	i := o.IT
	v := itOrderView{
		ID:            i.ID.String(),
		Provider:      models.ITIntentProvider(i),
		Direction:     i.Direction,
		Status:        i.Status,
		Currency:      i.Currency,
		Amount:        services.FormatMinor(i.AmountMinor),
		Token:         i.Asset,
		Chain:         strings.ToLower(i.DestChain),
		Rate:          services.FormatMinor(services.StableBuyDisplayRate(i)),
		EffectiveRate: services.FormatMinor(i.LockedRate),
		TokenAmount:   services.FormatStableUnits(i.AssetAmount),
		Fees:          ramp.VenueFees(services.StableBuyFees(i)),
		WalletAddress: i.DestAddress,
		FailureReason: services.UserFacingITFailure(i.FailureReason),
		CreatedAt:     i.CreatedAt,
		HonourUntil:   services.StableBuyHonourUntil(i),
		Repriced:      services.StableBuyRateUpdated(i),
		TxHash:        i.TxHash,
	}
	if p := o.PayInto; p != nil {
		v.PayInto = &rampPayInto{Bank: p.BankName, AccountName: p.AccountName, AccountNumber: p.AccountNumber,
			AmountNgn: p.AmountNGN, PayerInstruction: p.PayerInstruction}
	}
	return v
}

const (
	rampOrderOpenCode  = "ramp_order_open"
	rampAbandonCapCode = "ramp_abandon_cap"
	rampDailyLimitCode = "ramp_daily_limit"
)

// rampError maps router outcomes; everything else (refusals keep their
// cause) goes through paycrestError, so wording and statuses match today.
// User copy never names a provider.
func rampError(c *gin.Context, err error) {
	var moved *ramp.PriceMovedError
	var open *services.StableBuyOpenError
	switch {
	case errors.As(err, &moved):
		utils.ErrorResponseWithCodeData(c, http.StatusConflict,
			"The price changed since your quote. Check the new rate and confirm again.", "price_moved", moved.Quote)
	case errors.As(err, &open) || errors.Is(err, services.ErrStableBuyAlreadyOpen):
		// A purchase waiting for payment: name it so the app offers View and
		// Cancel instead of a dead end (D26).
		var data any
		if open != nil {
			data = gin.H{"orderId": open.IntentID.String()}
		}
		utils.ErrorResponseWithCodeData(c, http.StatusConflict,
			"You have a purchase waiting for payment. Pay into it, or cancel it to start a new one.",
			rampOrderOpenCode, data)
	case errors.Is(err, ramp.ErrInvalidQuote):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest,
			"Your quote is missing or out of date. Refresh the rate and try again.", "invalid_quote")
	case errors.Is(err, ramp.ErrAbandonCapReached):
		utils.ErrorResponseWithCode(c, http.StatusTooManyRequests,
			"You've cancelled too many unpaid purchases today. Try again tomorrow.", rampAbandonCapCode)
	case errors.Is(err, services.ErrTreasuryDailyCapReached):
		utils.ErrorResponseWithCode(c, http.StatusTooManyRequests,
			"You've reached today's limit for instant purchases. Try a smaller amount, or come back tomorrow.", rampDailyLimitCode)
	case errors.Is(err, services.ErrPaycrestBankUnsupported), errors.Is(err, services.ErrPaycrestBelowMinimum):
		// A provider's own reason the user can act on (carried on an
		// unavailable outcome): today's 400 wording, not a generic outage.
		paycrestError(c, err)
	case errors.Is(err, services.ErrITBuyPaymentArrived):
		utils.ErrorResponseWithCode(c, http.StatusConflict, itBuyPaymentArrivedMessage, itBuyPaymentArrivedCode)
	case errors.Is(err, services.ErrITBadState):
		paycrestError(c, services.ErrPaycrestNotCancellable)
	case errors.Is(err, ramp.ErrUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable,
			"Bank transfers are temporarily unavailable. Please try again shortly.", "ramp_unavailable")
	case errors.Is(err, ramp.ErrOrderNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
	case errors.Is(err, ramp.ErrLegClosed):
		paycrestError(c, services.ErrPaycrestLegClosed)
	case errors.Is(err, ramp.ErrBelowMinimum):
		paycrestError(c, services.ErrPaycrestBelowMinimum)
	case errors.Is(err, ramp.ErrAboveMaximum):
		utils.ErrorResponse(c, http.StatusBadRequest, "That amount is above the maximum for this route. Try a smaller amount.")
	case errors.Is(err, services.ErrBadDeliveryAddress), errors.Is(err, services.ErrStableBuyNoWallet):
		// Classified as no-venue or refused-for-user by the provider, but the
		// user's fix is the same: save a wallet address. Keep it above ErrNoVenue.
		paycrestError(c, err)
	case errors.Is(err, ramp.ErrNoVenue):
		paycrestError(c, services.ErrPaycrestNoVenue)
	default:
		paycrestError(c, err)
	}
}
