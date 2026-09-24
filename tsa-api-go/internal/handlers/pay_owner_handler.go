package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// A TSA Pay merchant's own account, from the app: where they are paid, read and
// changed by the user who owns the merchant. The change is the one a stolen
// login would make to redirect a merchant's money, so it asks for the account
// password again, is refused for a bank account in anyone else's name
// (UpdateSettlementByOwner), and is announced to the owner on every channel.

// WithEvents lets the owner routes tell the owner what changed. Nil leaves the
// change silent, which only a test should ever do.
func (h *PayHandler) WithEvents(bus *events.Bus) *PayHandler {
	h.events = bus
	return h
}

// payOwnerMerchantView is what the owner reads of their own merchant: where
// they are paid, with the account number masked (they recognise it by its last
// four digits; the full number is no use on a screen).
type payOwnerMerchantView struct {
	ID             uuid.UUID `json:"id"`
	BusinessName   string    `json:"businessName"`
	Status         string    `json:"status"`
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
	v := payOwnerMerchantView{ID: m.ID, BusinessName: m.BusinessName, Status: m.Status, SettlementMode: m.SettlementMode}
	switch m.SettlementMode {
	case models.PaySettlementFiat:
		v.BankCode, v.AccountNumber, v.AccountName = m.BankCode, payMaskAccount(m.AccountNumber), m.AccountName
	case models.PaySettlementCrypto:
		v.ChainID, v.TokenAddress, v.TokenSymbol, v.WalletAddress = m.ChainID, m.TokenAddress, m.TokenSymbol, m.WalletAddress
	}
	return v
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

// PUT /api/pay/merchant/settlement — the owner changes where they are paid.
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
	// redirect where the merchant is paid.
	if req.Password == "" || user.ComparePassword(req.Password) != nil {
		utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "Incorrect password", "password_incorrect")
		return
	}
	m, err := h.merchants.UpdateSettlementByOwner(c.Request.Context(), user.ID, req.PaySettlementChange)
	if err != nil {
		payOwnerFail(c, err)
		return
	}
	h.announceSettlement(user.ID, m)
	utils.SuccessResponse(c, http.StatusOK, "Settlement updated", payOwnerView(m))
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
// owner recognises, without printing a full account number or address.
func paySettlementDestination(m *models.PayMerchant) string {
	if m.SettlementMode == models.PaySettlementFiat {
		return fmt.Sprintf("the bank account ending %s (%s)", lastN(m.AccountNumber, 4), m.AccountName)
	}
	return fmt.Sprintf("%s to the wallet ending %s", m.TokenSymbol, lastN(m.WalletAddress, 4))
}

func lastN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// payOwnerFail maps an owner-route refusal to its status. Every message here is
// already written for the owner; anything unrecognised is logged, not echoed.
func payOwnerFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPayMerchantNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "You don't have a TSA Pay merchant account")
	case errors.Is(err, services.ErrPayMerchantSuspended):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "Your merchant account is suspended — contact support to change where you are paid", "merchant_suspended")
	case errors.Is(err, services.ErrPaySettlementNameMismatch):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That account isn't in your business name or your verified name. Use an account in your own name.", "name_mismatch")
	case errors.Is(err, services.ErrPayMerchantInvalid):
		utils.ErrorResponse(c, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPayMerchantStale):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Your merchant settings changed while you were editing — reload and try again", "merchant_stale")
	default:
		log.Printf("[tsa-pay] owner request failed: %s %s: %v", c.Request.Method, c.FullPath(), err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
	}
}
