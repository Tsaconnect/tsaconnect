package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// A TSA Pay merchant's own account, from the app: where they are paid, read and
// changed by the user who owns the merchant. The change is the one a stolen
// login would make to redirect a merchant's money, so it asks for the account
// password again (counted toward the login lockout), refuses a bank account in
// anyone else's name, holds a crypto wallet until the owner confirms a code
// emailed to them (UpdateSettlementByOwner), and announces every change to the
// owner on every channel.

// WithEvents lets the owner routes tell the owner what changed. Nil leaves the
// change silent, which only a test should ever do.
func (h *PayHandler) WithEvents(bus *events.Bus) *PayHandler {
	h.events = bus
	return h
}

// payOwnerMerchantView is what the owner reads of their own merchant: where
// they are paid, with the account number masked (they recognise it by its last
// four digits; the full number is no use on a screen), and the version a change
// from this screen is made against.
type payOwnerMerchantView struct {
	ID             uuid.UUID `json:"id"`
	BusinessName   string    `json:"businessName"`
	Status         string    `json:"status"`
	Version        int       `json:"version"`
	SettlementMode string    `json:"settlementMode"`
	BankCode       string    `json:"bankCode,omitempty"`
	AccountNumber  string    `json:"accountNumber,omitempty"`
	AccountName    string    `json:"accountName,omitempty"`
	ChainID        int64     `json:"chainId,omitempty"`
	TokenAddress   string    `json:"tokenAddress,omitempty"`
	TokenSymbol    string    `json:"tokenSymbol,omitempty"`
	WalletAddress  string    `json:"walletAddress,omitempty"`
}

func payOwnerView(m *models.PayMerchant) payOwnerMerchantView {
	v := payOwnerMerchantView{ID: m.ID, BusinessName: m.BusinessName, Status: m.Status, Version: m.Version, SettlementMode: m.SettlementMode}
	switch m.SettlementMode {
	case models.PaySettlementFiat:
		v.BankCode, v.AccountNumber, v.AccountName = m.BankCode, payMaskAccount(m.AccountNumber), m.AccountName
	case models.PaySettlementCrypto:
		v.ChainID, v.TokenAddress, v.TokenSymbol, v.WalletAddress = m.ChainID, m.TokenAddress, m.TokenSymbol, m.WalletAddress
	}
	return v
}

// payOwnerSettlementAnswer is a change's outcome: the merchant as it now is, or
// — for a crypto wallet — where the confirmation code was sent.
type payOwnerSettlementAnswer struct {
	Merchant     *payOwnerMerchantView          `json:"merchant,omitempty"`
	Confirmation *services.PaySettlementPending `json:"confirmation,omitempty"`
}

// GET /api/pay/merchant — the caller's own merchant.
func (h *PayHandler) OwnerMerchant(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	m, err := h.merchants.GetByOwner(c.Request.Context(), user.ID)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Merchant", payOwnerView(m))
}

type payOwnerSettlementRequest struct {
	services.PaySettlementChange
	Password string `json:"password"`
}

// PUT /api/pay/merchant/settlement — the owner changes where they are paid. A
// bank account is applied at once; a crypto wallet answers 202 and waits for
// the emailed code (POST /api/pay/merchant/settlement/confirm).
func (h *PayHandler) OwnerUpdateSettlement(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req payOwnerSettlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	// A session is not enough: a stolen or borrowed phone must not be able to
	// redirect where the merchant is paid, nor guess the password here unseen.
	switch err := checkPasswordWithLockout(config.DB, h.events, user, req.Password); {
	case errors.Is(err, errAccountLocked):
		utils.ErrorResponseWithCode(c, http.StatusLocked, "Your account is temporarily locked. Try again later.", "account_locked")
		return
	case err != nil:
		utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "Incorrect password", "password_incorrect")
		return
	}
	res, err := h.merchants.UpdateSettlementByOwner(c.Request.Context(), user.ID, req.PaySettlementChange)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	if res.Pending != nil {
		c.JSON(http.StatusAccepted, gin.H{
			"success": true,
			"message": "We've emailed you a code to confirm this change",
			"data":    payOwnerSettlementAnswer{Confirmation: res.Pending},
		})
		return
	}
	if res.Changed {
		h.announceSettlement(user.ID, res.Merchant)
	}
	view := payOwnerView(res.Merchant)
	utils.SuccessResponse(c, http.StatusOK, "Settlement updated", payOwnerSettlementAnswer{Merchant: &view})
}

type payOwnerConfirmRequest struct {
	Code string `json:"code"`
}

// POST /api/pay/merchant/settlement/confirm — the emailed code that applies a
// waiting crypto payout change.
func (h *PayHandler) OwnerConfirmSettlement(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req payOwnerConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Enter the code we emailed you")
		return
	}
	m, err := h.merchants.ConfirmSettlementByOwner(c.Request.Context(), user.ID, req.Code)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	h.announceSettlement(user.ID, m)
	view := payOwnerView(m)
	utils.SuccessResponse(c, http.StatusOK, "Settlement updated", payOwnerSettlementAnswer{Merchant: &view})
}

// announceSettlement tells the owner, in the app and by email and push, that
// their payout destination changed — so a change they did not make is seen.
func (h *PayHandler) announceSettlement(owner uuid.UUID, m *models.PayMerchant) {
	if h.events == nil {
		return
	}
	h.events.Publish(events.Event{
		Type:   events.SecurityPaySettlementChanged,
		UserID: owner,
		Title:  "Your TSA Pay payout details were changed",
		Message: fmt.Sprintf("Payments to %s will now be paid to %s. If you didn't make this change, contact support immediately.",
			m.BusinessName, paySettlementDestination(m)),
		Data: map[string]interface{}{"merchantId": m.ID.String()},
	})
}

// paySettlementDestination names where the merchant is now paid, in words the
// owner recognises, without printing a full account number — and a wallet by
// its first six and last four characters, so a lookalike that only shares the
// ending is visible.
func paySettlementDestination(m *models.PayMerchant) string {
	if m.SettlementMode == models.PaySettlementFiat {
		return fmt.Sprintf("the bank account %s (%s)", payMaskAccount(m.AccountNumber), m.AccountName)
	}
	return fmt.Sprintf("%s to the wallet %s", m.TokenSymbol, services.PayShortAddress(m.WalletAddress))
}

// payOwnerFail maps an owner-route refusal to its status and a machine code.
// Every message is written for the owner; a bank provider's own words and any
// unrecognised error are logged, never echoed.
func payOwnerFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayMerchantNotFound):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, "You don't have a TSA Pay merchant account", "no_merchant")
	case errors.Is(err, services.ErrPayMerchantAmbiguous):
		log.Printf("[tsa-pay] owner request refused: %v", err)
		utils.ErrorResponseWithCode(c, http.StatusConflict, "We couldn't tell which merchant account to change — contact support", "merchant_ambiguous")
	case errors.Is(err, services.ErrPayMerchantSuspended):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "Your merchant account is suspended — contact support to change where you are paid", "merchant_suspended")
	case errors.Is(err, services.ErrPaySettlementNameMismatch):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That account isn't in your business name or your verified name. Use an account in your own name.", "name_mismatch")
	case errors.Is(err, services.ErrPayAccountUnverifiable):
		log.Printf("[tsa-pay] owner account lookup failed: %v", err)
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "We couldn't verify that account. Check the bank and account number and try again.", "account_unverifiable")
	case errors.Is(err, services.ErrPayMerchantInvalid):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPayMerchantStale):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Your payment settings changed since you opened this screen — reload and try again", "merchant_stale")
	case errors.Is(err, services.ErrPaySettlementConfirmUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "We couldn't send you a confirmation code right now — try again later or contact support", "confirmation_unavailable")
	case errors.Is(err, services.ErrPaySettlementNoPending):
		utils.ErrorResponseWithCode(c, http.StatusGone, "This change has expired or had too many wrong codes — start again", "no_pending_change")
	case errors.Is(err, services.ErrPaySettlementCodeInvalid):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That code isn't right", "code_invalid")
	default:
		log.Printf("[tsa-pay] owner request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}
