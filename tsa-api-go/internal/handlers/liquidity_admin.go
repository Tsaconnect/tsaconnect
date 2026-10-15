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
	"gorm.io/gorm"
)

// PoolOrdersAdmin is the slice of *services.ITService the pool-order console
// uses (plan C2 §15, P20). The routes gate the actions to super admins.
type PoolOrdersAdmin interface {
	PoolOrderDetail(ctx context.Context, intentID uuid.UUID) (*services.PoolOrderDetail, error)
	ResendPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error)
}

// ForwardRechecker is the operator's "recheck by reference" on any settlement
// forward, merchant or LP (re-review I-D).
type ForwardRechecker interface {
	Recheck(ctx context.Context, forwardID uuid.UUID) error
}

// The production implementations (wired in main by Task 12).
var (
	_ PoolOrdersAdmin  = (*services.ITService)(nil)
	_ ForwardRechecker = (*services.P2PSettlementForwarder)(nil)
)

// SetPoolOrdersAdmin wires the console. A nil (or typed-nil) admin leaves the
// routes unregistered.
func (h *ITHandler) SetPoolOrdersAdmin(a PoolOrdersAdmin) {
	if isNilIface(a) {
		h.poolAdmin = nil
		return
	}
	h.poolAdmin = a
}

// PoolOrdersEnabled reports whether the pool-order routes should register.
func (h *ITHandler) PoolOrdersEnabled() bool { return h != nil && h.poolAdmin != nil }

// SetForwardRechecker wires the operator's forward recheck. A nil (or
// typed-nil) rechecker leaves the route unregistered.
func (h *ITHandler) SetForwardRechecker(r ForwardRechecker) {
	if isNilIface(r) {
		h.fwdRecheck = nil
		return
	}
	h.fwdRecheck = r
}

// ForwardRecheckEnabled reports whether the recheck route should register.
func (h *ITHandler) ForwardRecheckEnabled() bool { return h != nil && h.fwdRecheck != nil }

func isNilIface(v any) bool {
	return v == nil || (reflect.ValueOf(v).Kind() == reflect.Ptr && reflect.ValueOf(v).IsNil())
}

// GET /api/admin/liquidity/orders/:id
func (h *ITHandler) AdminPoolOrder(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	d, err := h.poolAdmin.PoolOrderDetail(c.Request.Context(), id)
	if err != nil {
		poolAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Pool order", gin.H{"order": d})
}

// POST …/:id/resend — re-send the sale after the dropped-transaction check.
func (h *ITHandler) AdminResendPoolBuy(c *gin.Context) {
	h.companyWalletAction(c, h.poolAdmin.ResendPoolBuy)
}

type operatorNoteBody struct {
	Note string `json:"note"`
}

// operatorNote reads the action body and refuses an action without a note.
func operatorNote(c *gin.Context) (*models.User, operatorNoteBody, bool) {
	var body operatorNoteBody
	_ = c.ShouldBindJSON(&body) // an empty body reaches the note check below
	if strings.TrimSpace(body.Note) == "" {
		companyWalletAdminError(c, services.ErrITOperatorNoteRequired)
		return nil, body, false
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, body, false
	}
	return user, body, true
}

// POST /api/admin/p2p/forwards/:id/recheck — look the transfer up by its
// reference and resolve the forward; never marks it unpaid (re-review I-D).
func (h *ITHandler) AdminRecheckForward(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	user, body, ok := operatorNote(c)
	if !ok {
		return
	}
	log.Printf("[p2p forward] operator recheck forward=%s admin=%s note=%q", id, user.ID, services.RedactAlertText(body.Note))
	if err := h.fwdRecheck.Recheck(c.Request.Context(), id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.ErrorResponseWithCode(c, http.StatusNotFound, "Forward not found", "not_found")
			return
		}
		msg := services.RedactAlertText(err.Error())
		log.Printf("[p2p forward] operator recheck forward=%s unresolved: %s", id, msg)
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Recheck could not resolve the forward: "+msg, "recheck_unresolved")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Rechecked", nil)
}

// in companyWalletAdminError, which the order actions share.
func poolAdminError(c *gin.Context, err error) {
	if errors.Is(err, services.ErrTradeNotFound) {
		utils.ErrorResponseWithCode(c, http.StatusNotFound, "Slice not found", "not_found")
		return
	}
	if errors.Is(err, services.ErrLPAmount) {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, services.RedactAlertText(err.Error()), "bad_amount")
		return
	}
	if errors.Is(err, services.ErrPoolOperationID) || errors.Is(err, services.ErrPoolPendingWallet) {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if errors.Is(err, services.ErrLiquidityNotVault) {
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "pool_unavailable")
		return
	}
	companyWalletAdminError(c, err)
}
