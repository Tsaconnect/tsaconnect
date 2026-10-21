package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// SellerPayoutHandler is a marketplace seller's own payout destination: the
// bank or mobile money account they are paid into when a buyer confirms an
// order. No destination set means they are paid to their wallet.
type SellerPayoutHandler struct {
	svc    *services.SellerPayoutService
	events *events.Bus
}

func NewSellerPayoutHandler(svc *services.SellerPayoutService, bus *events.Bus) *SellerPayoutHandler {
	return &SellerPayoutHandler{svc: svc, events: bus}
}

type sellerPayoutView struct {
	Kind            string `json:"kind"`
	Currency        string `json:"currency,omitempty"`
	InstitutionCode string `json:"institutionCode,omitempty"`
	InstitutionName string `json:"institutionName,omitempty"`
	AccountNumber   string `json:"accountNumber,omitempty"`
	AccountName     string `json:"accountName,omitempty"`
	Verified        bool   `json:"verified,omitempty"`
}

func newSellerPayoutView(p services.SellerPayout) sellerPayoutView {
	if p.Method == nil {
		return sellerPayoutView{Kind: models.SellerPayoutWallet}
	}
	m := p.Method
	return sellerPayoutView{
		Kind: m.Kind, Currency: m.Currency, InstitutionCode: m.InstitutionCode,
		InstitutionName: m.InstitutionName, AccountNumber: m.AccountNumber,
		AccountName: m.AccountName, Verified: m.Verified,
	}
}

// GET /api/seller/payout-method
func (h *SellerPayoutHandler) Get(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	p, err := h.svc.Resolve(c.Request.Context(), user.ID)
	if err != nil {
		log.Printf("[seller-payout] resolve failed user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Couldn't load your payout method")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payout method", newSellerPayoutView(p))
}

type setSellerPayoutRequest struct {
	Currency        string `json:"currency"`
	InstitutionCode string `json:"institutionCode" binding:"required"`
	AccountNumber   string `json:"accountNumber" binding:"required"`
	AccountName     string `json:"accountName"`
}

// PUT /api/seller/payout-method
func (h *SellerPayoutHandler) Set(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	var req setSellerPayoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Choose a bank or provider and enter the account number")
		return
	}
	m, err := h.svc.Set(c.Request.Context(), user.ID, services.SetSellerPayoutInput{
		Currency: req.Currency, InstitutionCode: req.InstitutionCode,
		AccountNumber: req.AccountNumber, AccountName: req.AccountName,
	})
	if err != nil {
		sellerPayoutFail(c, err)
		return
	}
	h.announce(user.ID, fmt.Sprintf("Your marketplace payouts will now go to %s (%s).", m.InstitutionName, m.AccountName))
	utils.SuccessResponse(c, http.StatusOK, "Payout method saved", newSellerPayoutView(services.SellerPayout{Kind: m.Kind, Method: m}))
}

// DELETE /api/seller/payout-method — back to wallet payouts.
func (h *SellerPayoutHandler) Clear(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return
	}
	if err := h.svc.Clear(c.Request.Context(), user.ID); err != nil {
		log.Printf("[seller-payout] clear failed user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Couldn't change your payout method")
		return
	}
	h.announce(user.ID, "Your marketplace payouts will now go to your wallet.")
	utils.SuccessResponse(c, http.StatusOK, "Payout method cleared", newSellerPayoutView(services.SellerPayout{Kind: models.SellerPayoutWallet}))
}

func (h *SellerPayoutHandler) announce(userID uuid.UUID, msg string) {
	if h.events == nil {
		return
	}
	h.events.Publish(events.Event{
		Type:    events.SecuritySellerPayoutChanged,
		UserID:  userID,
		Title:   "Your marketplace payout details were changed",
		Message: msg + " If you didn't make this change, contact support immediately.",
	})
}

func sellerPayoutFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrSellerPayoutInstitution):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "institution_unsupported")
	case errors.Is(err, services.ErrSellerPayoutAccount):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "We couldn't verify that account. Check the details and try again.", "account_unverifiable")
	case errors.Is(err, services.ErrPaycrestAccountShape):
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, err.Error(), "account_shape")
	case errors.Is(err, services.ErrSellerPayoutUnavailable):
		log.Printf("[seller-payout] unavailable: %v", err)
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "Payout setup isn't available right now — try again later", "payout_unavailable")
	default:
		log.Printf("[seller-payout] set failed: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Couldn't save your payout method")
	}
}
