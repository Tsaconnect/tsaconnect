package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// CompanyWalletAdmin is the slice of *services.ITService the console uses.
type CompanyWalletAdmin interface {
	CompanyWalletStatus(ctx context.Context, lowUSD float64) (*services.CompanyWalletStatus, error)
	ListCompanyWalletOrders(ctx context.Context) ([]services.CompanyWalletOrder, error)
	DeliverParkedTreasuryBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error)
	MarkStableBuyRefunded(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error)
	ResendTreasuryBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error)
}

// SetCompanyWalletAdmin wires the console. A nil (or typed-nil) admin leaves
// the routes unregistered.
func (h *ITHandler) SetCompanyWalletAdmin(a CompanyWalletAdmin, lowUSD float64) {
	if a == nil || (reflect.ValueOf(a).Kind() == reflect.Ptr && reflect.ValueOf(a).IsNil()) {
		h.cwAdmin = nil
		return
	}
	h.cwAdmin, h.cwLowUSD = a, lowUSD
}

// CompanyWalletEnabled reports whether the console routes should register.
func (h *ITHandler) CompanyWalletEnabled() bool { return h != nil && h.cwAdmin != nil }

// GET /api/admin/ramp/company-wallet — balance, reserved, free, low (§13 Q6).
func (h *ITHandler) AdminCompanyWallet(c *gin.Context) {
	st, err := h.cwAdmin.CompanyWalletStatus(c.Request.Context(), h.cwLowUSD)
	if err != nil {
		companyWalletAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Company wallet", gin.H{"wallet": st})
}

// GET /api/admin/ramp/company-wallet/orders — parked and stalled orders.
func (h *ITHandler) AdminCompanyWalletOrders(c *gin.Context) {
	orders, err := h.cwAdmin.ListCompanyWalletOrders(c.Request.Context())
	if err != nil {
		companyWalletAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Company wallet orders", gin.H{"orders": orders})
}

type companyWalletActionBody struct {
	Note string `json:"note"`
}

func (h *ITHandler) companyWalletAction(c *gin.Context, act func(ctx context.Context, adminID, id uuid.UUID, note string) (*models.ITIntent, error)) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	var body companyWalletActionBody
	_ = c.ShouldBindJSON(&body) // an empty body reaches the note check below
	if strings.TrimSpace(body.Note) == "" {
		companyWalletAdminError(c, services.ErrITOperatorNoteRequired)
		return
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	intent, err := act(c.Request.Context(), user.ID, id, body.Note)
	if err != nil {
		companyWalletAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{"intent": intent})
}

// POST …/orders/:id/deliver — deliver a parked buy anyway (§13 Q9).
func (h *ITHandler) AdminDeliverTreasuryBuy(c *gin.Context) {
	h.companyWalletAction(c, h.cwAdmin.DeliverParkedTreasuryBuy)
}

// POST …/orders/:id/refund — record that the naira went back (§5.2).
func (h *ITHandler) AdminRefundStableBuy(c *gin.Context) {
	h.companyWalletAction(c, h.cwAdmin.MarkStableBuyRefunded)
}

// POST …/orders/:id/resend — re-send after the dropped-transaction check (§5.4).
func (h *ITHandler) AdminResendTreasuryBuy(c *gin.Context) {
	h.companyWalletAction(c, h.cwAdmin.ResendTreasuryBuy)
}

func companyWalletAdminError(c *gin.Context, err error) {
	// Wrapped provider errors can carry an RPC URL or key: redact before the
	// text is logged or returned.
	msg := services.RedactAlertText(err.Error())
	switch {
	case errors.Is(err, services.ErrITOperatorNoteRequired):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, msg, "note_required")
	case errors.Is(err, services.ErrTreasuryStockShort):
		utils.ErrorResponseWithCode(c, http.StatusConflict, msg, "stock_short")
	case errors.Is(err, services.ErrTreasuryResendRefused):
		utils.ErrorResponseWithCode(c, http.StatusConflict, msg, "resend_refused")
	case errors.Is(err, services.ErrITBadState):
		utils.ErrorResponseWithCode(c, http.StatusConflict, msg, "bad_state")
	case errors.Is(err, services.ErrStableIntentNotFound), strings.Contains(msg, "intent not found"):
		utils.ErrorResponseWithCode(c, http.StatusNotFound, "Order not found", "not_found")
	default:
		log.Printf("[company wallet admin] %s %s: %s", c.Request.Method, c.FullPath(), msg)
		utils.ErrorResponse(c, http.StatusInternalServerError, msg)
	}
}
