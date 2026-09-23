package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// paycrest_sell_intents.go — the five steps of one swapped sale (design §5.1).
//
// Create returns the executable LiFi quote the app signs; the app reports the
// hash; the app asks for the offramp once LiFi says DONE. Nothing here decides
// an amount — every figure is the service's own, re-quoted and re-proven.

// sellIntents is the port *services.SellIntentService fills.
type sellIntents interface {
	Create(ctx context.Context, user *models.User, in services.CreateSellIntentInput) (*models.RampSellIntent, *services.SellSwapQuote, error)
	RecordSwap(ctx context.Context, userID, id uuid.UUID, txHash string) error
	OpenOrder(ctx context.Context, user *models.User, id uuid.UUID) (*models.PaycrestOrderRecord, error)
	Abandon(ctx context.Context, userID, id uuid.UUID) error
	Open(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error)
	Resumable(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error)
	Get(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error)
	// The top-up's caps and once-per-sale claim live on the sale's own row.
	services.IntentTopUpStore
}

// WithSellIntents turns the sale routes on. Nil (RAMP_CONVERSION_SELL unset)
// leaves them unregistered.
func (h *PaycrestHandler) WithSellIntents(s sellIntents) *PaycrestHandler {
	h.sellIntents = s
	return h
}

func (h *PaycrestHandler) SellIntentsEnabled() bool { return h != nil && h.sellIntents != nil }

type paycrestSellIntentBody struct {
	SourceChainID int64  `json:"sourceChainId"`
	SourceToken   string `json:"sourceToken"`
	SourceAmount  string `json:"sourceAmount"`
	SourceAddress string `json:"sourceAddress"`
	// WalletAddress is the EVM wallet the USDC lands in. Optional: unset, the
	// account's registered wallet is used. Either way it goes through
	// callerWallet, never straight into the intent.
	WalletAddress string `json:"walletAddress"`
	BankCode      string `json:"bankCode"`
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
	Currency      string `json:"currency"`
}

// POST /api/paycrest/sell-intents
func (h *PaycrestHandler) CreateSellIntent(c *gin.Context) {
	user, ok := h.sellIntentCaller(c)
	if !ok {
		return
	}
	var body paycrestSellIntentBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if body.SourceChainID <= 0 || strings.TrimSpace(body.SourceToken) == "" ||
		strings.TrimSpace(body.SourceAmount) == "" || strings.TrimSpace(body.SourceAddress) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest,
			"sourceChainId, sourceToken, sourceAmount and sourceAddress are required")
		return
	}
	// The USDC receiver is the account's own reconciled wallet, never the
	// body's word for it — the same rule the sell quote follows. sourceAddress
	// is different: it is where the swap is SENT from, may be a Solana
	// address, and the service validates it for that chain's family.
	wallet, ok := callerWallet(c, user, body.WalletAddress)
	if !ok {
		return
	}
	user.WalletAddress = wallet

	intent, quote, err := h.sellIntents.Create(c.Request.Context(), user, services.CreateSellIntentInput{
		SourceChainID: body.SourceChainID,
		SourceToken:   body.SourceToken,
		SourceAmount:  body.SourceAmount,
		SourceAddress: body.SourceAddress,
		FiatCode:      body.Currency,
		BankCode:      body.BankCode,
		AccountNumber: body.AccountNumber,
		AccountName:   body.AccountName,
	})
	if err != nil {
		h.sellIntentError(c, err, user)
		return
	}
	// gasTopUp is the server's own verdict on whether this sale needs gas on
	// the source chain: the app asks for a top-up exactly when it is set,
	// because only then was one priced into the budget the sale was admitted on.
	utils.SuccessResponse(c, http.StatusCreated, "Sale started — sign the swap", gin.H{
		"intent": intent, "lifiQuote": quote.LiFiQuote, "gasTopUp": quote.GasTopUp,
	})
}

// GET /api/paycrest/sell-intents/open — the sale "Finish your sale" resumes:
// one in progress, or an ordered one whose payout still awaits her deposit.
// A 404 (the routes unregistered) is how the app learns swapped sells are off.
func (h *PaycrestHandler) OpenSellIntent(c *gin.Context) {
	user, ok := h.sellIntentCaller(c)
	if !ok {
		return
	}
	intent, err := h.sellIntents.Resumable(c.Request.Context(), user.ID)
	if err != nil {
		h.sellIntentError(c, err, user)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sale in progress", gin.H{"intent": intent})
}

// POST /api/paycrest/sell-intents/:id/swap
func (h *PaycrestHandler) RecordSellIntentSwap(c *gin.Context) {
	user, id, ok := h.sellIntentTarget(c)
	if !ok {
		return
	}
	var body struct {
		TxHash string `json:"txHash"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.sellIntents.RecordSwap(c.Request.Context(), user.ID, id, body.TxHash); err != nil {
		h.sellIntentError(c, err, user)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Swap recorded", nil)
}

// POST /api/paycrest/sell-intents/:id/order
func (h *PaycrestHandler) OpenSellIntentOrder(c *gin.Context) {
	user, id, ok := h.sellIntentTarget(c)
	if !ok {
		return
	}
	order, err := h.sellIntents.OpenOrder(c.Request.Context(), user, id)
	if err != nil {
		h.sellIntentError(c, err, user)
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Your payout is on its way", order)
}

// POST /api/paycrest/sell-intents/:id/abandon
func (h *PaycrestHandler) AbandonSellIntent(c *gin.Context) {
	user, id, ok := h.sellIntentTarget(c)
	if !ok {
		return
	}
	if err := h.sellIntents.Abandon(c.Request.Context(), user.ID, id); err != nil {
		h.sellIntentError(c, err, user)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sale cancelled", nil)
}

// SellIntentGasTopUpEnabled: the route needs a sale service
// (RAMP_CONVERSION_SELL) AND a live sponsor (RAMP_GAS_SPONSORSHIP).
func (h *PaycrestHandler) SellIntentGasTopUpEnabled() bool {
	return h.SellIntentsEnabled() && h.SponsorshipEnabled()
}

// POST /api/paycrest/sell-intents/:id/gas-topup — source-chain gas for a sale
// of an EVM token she holds no gas coin for. Blocks until the gas lands
// (<=30s), like the order top-up, so the swap she signs next is priced
// against a real balance. The amount is the service's, from the sale's quote.
func (h *PaycrestHandler) SellIntentGasTopUp(c *gin.Context) {
	if !h.SellIntentGasTopUpEnabled() {
		utils.ErrorResponse(c, http.StatusNotFound, "Gas sponsorship is not available")
		return
	}
	user, id, ok := h.sellIntentTarget(c)
	if !ok {
		return
	}
	intent, err := h.sellIntents.Get(c.Request.Context(), user.ID, id)
	if err != nil {
		h.sellIntentError(c, err, user)
		return
	}
	res, err := h.sponsor.TopUpForIntent(c.Request.Context(), user, intent, h.sellIntents)
	if err != nil {
		if errors.Is(err, services.ErrSellIntentNotFound) || errors.Is(err, services.ErrSellIntentState) {
			h.sellIntentError(c, err, user)
			return
		}
		sponsorshipError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Gas ready", res)
}

func (h *PaycrestHandler) sellIntentCaller(c *gin.Context) (*models.User, bool) {
	if !h.SellIntentsEnabled() {
		utils.ErrorResponse(c, http.StatusNotFound, "Selling this token isn't available")
		return nil, false
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return nil, false
	}
	return user, true
}

func (h *PaycrestHandler) sellIntentTarget(c *gin.Context) (*models.User, uuid.UUID, bool) {
	user, ok := h.sellIntentCaller(c)
	if !ok {
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid sale id")
		return nil, uuid.Nil, false
	}
	return user, id, true
}

// The machine codes a sale's outcome carries. The app decides "wait and ask
// again" versus "this sale is over, and how" on these, never on the message,
// which is copy and free to change.
const (
	sellCodeSwapPending   = "swap_pending"   // LiFi has not reported the swap done: wait
	sellCodePayoutOpening = "payout_opening" // another request is opening the payout: wait
	sellCodeSwapFailed    = "swap_failed"    // terminal: the swap failed, her tokens never left
	sellCodeSwapRefunded  = "swap_refunded"  // terminal: refunded, her tokens are coming back
	sellCodeSwapPartial   = "swap_partial"   // terminal: she got a different token than quoted
)

// swapEndedCode names how a terminal swap ended. A FAILED status is failed
// whatever its qualifier; DONE ends short only as REFUNDED or PARTIAL.
func swapEndedCode(err error) string {
	var ended *services.SwapEndedShortError
	if errors.As(err, &ended) && ended.Status == services.LiFiStatusDone {
		switch ended.Substatus {
		case services.LiFiSubstatusRefunded:
			return sellCodeSwapRefunded
		case services.LiFiSubstatusPartial:
			return sellCodeSwapPartial
		}
	}
	return sellCodeSwapFailed
}

// sellIntentError maps the service's sentinels. The distinctions matter to the
// app's screen: "still swapping" is a wait, "doesn't match" is a dead end she
// needs help with, and "already selling" comes with the sale she is already in
// so the app can resume it rather than ask her to start over.
func (h *PaycrestHandler) sellIntentError(c *gin.Context, err error, user *models.User) {
	switch {
	case errors.Is(err, services.ErrSellIntentOpen):
		open, _ := h.sellIntents.Open(c.Request.Context(), user.ID)
		c.JSON(http.StatusConflict, gin.H{
			"success": false, "message": "You already have a sale in progress",
			"data": gin.H{"intent": open},
		})
	case errors.Is(err, services.ErrSwapNotLanded):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Still swapping — try again in a moment", sellCodeSwapPending)
	case errors.Is(err, services.ErrSellIntentOrdering):
		// Another request is opening this payout: a wait, like "still swapping".
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Opening your payout — try again in a moment", sellCodePayoutOpening)
	case errors.Is(err, services.ErrSwapFailed):
		// Terminal (§5.5), with LiFi's own reason: whatever did not land is
		// in or back in her wallet, and this sale can be abandoned.
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error()+" — nothing was sold; check your wallet", swapEndedCode(err))
	case errors.Is(err, services.ErrSwapMismatch):
		utils.ErrorResponse(c, http.StatusConflict, "This swap doesn't match your sale")
	case errors.Is(err, services.ErrSellIntentNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "That sale can't be found")
	case errors.Is(err, services.ErrSellIntentState):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	default:
		sellSwapError(c, err)
	}
}
