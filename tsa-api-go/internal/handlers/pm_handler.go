package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// PMAutomation is the narrow interface PMHandler depends on. The real
// *services.PMAutomationService satisfies it — the interface boundary exists
// so handler tests can inject a fake without the full service graph.
type PMAutomation interface {
	ListBuyManualQueue(ctx context.Context) ([]services.ManualQueueItem, error)
	ManualRelease(ctx context.Context, tradeID, adminID uuid.UUID) (*models.P2PTrade, error)
	InitiateSellPayout(ctx context.Context, tradeID uuid.UUID) (string, error)
	WithdrawToSettlement(ctx context.Context, pmID uuid.UUID, amount string) (string, error)
}

// PMHandler serves the PM operator endpoints (manual-release queue, release
// action, sell-payout kick).
type PMHandler struct {
	auto PMAutomation
}

// NewPMHandler creates a PMHandler backed by the given PMAutomation.
func NewPMHandler(auto PMAutomation) *PMHandler {
	return &PMHandler{auto: auto}
}

// GetManualQueue handles GET /api/admin/pm/manual-queue — lists PM buy trades
// stuck awaiting manual review (no matched inbound, on-chain open confirmed).
func (h *PMHandler) GetManualQueue(c *gin.Context) {
	items, err := h.auto.ListBuyManualQueue(c.Request.Context())
	if err != nil {
		log.Printf("[pm handler] ListBuyManualQueue: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load manual queue")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Manual queue", items)
}

// ReleaseTrade handles POST /api/admin/pm/trades/:id/release — operator
// manually verifies a PM buy trade and triggers the system markPaid + release.
func (h *PMHandler) ReleaseTrade(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	// Admin identity comes from the authenticated user in context (set by JWT
	// middleware, same pattern as getUserFromContext in all handlers).
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	adminID := user.ID

	trade, err := h.auto.ManualRelease(c.Request.Context(), id, adminID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrTradeNotFound):
			utils.ErrorResponse(c, http.StatusNotFound, err.Error())
		case errors.Is(err, services.ErrInvalidTransition):
			utils.ErrorResponse(c, http.StatusConflict, err.Error())
		default:
			log.Printf("[pm handler] ManualRelease trade=%s: %v", id, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Release failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Trade released", trade)
}

// InitiatePayout handles POST /api/admin/pm/trades/:id/payout — operator
// kicks the sell-payout for a confirmed deposited sell trade.
func (h *PMHandler) InitiatePayout(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	payoutID, err := h.auto.InitiateSellPayout(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrTradeNotFound):
			utils.ErrorResponse(c, http.StatusNotFound, err.Error())
		case errors.Is(err, services.ErrInvalidTransition):
			utils.ErrorResponse(c, http.StatusConflict, err.Error())
		default:
			log.Printf("[pm handler] InitiateSellPayout trade=%s: %v", id, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Payout initiation failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Payout initiated", gin.H{"payoutId": payoutID})
}

// DeclineTrade handles POST /api/admin/pm/trades/:id/decline — operator marks
// a manual-queue trade as declined. For this phase we audit-log the decision
// and return immediately; the timeout watchdog cancels+refunds on expiry.
//
// TODO(pm-phase2): explicit operator-driven cancel/refund
func (h *PMHandler) DeclineTrade(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	user := getUserFromContext(c)
	adminID := uuid.Nil
	if user != nil {
		adminID = user.ID
	}

	log.Printf("[pm handler] DeclineTrade: operator=%s declined trade=%s; timeout watchdog will cancel+refund on expiry", adminID, id)
	utils.SuccessResponse(c, http.StatusOK, "declined; will be cancelled by timeout", gin.H{"tradeId": id})
}

// withdrawRequest is the JSON body for the Withdraw endpoint.
type withdrawRequest struct {
	Amount string `json:"amount"`
}

// Withdraw handles POST /api/admin/pm/:id/withdraw — admin withdraws a PM's
// available NGN (balance minus open sell reservations) to its settlement bank
// via Nomba. The debit is atomic: never dips below open exposure.
//
// Responses:
//   - 200 OK: payout initiated successfully → {"payoutId": "..."}
//   - 400 Bad Request: invalid PM UUID, missing/empty amount, or PM not
//     configured (no sub-account or settlement bank)
//   - 409 Conflict: amount exceeds the PM's free (withdrawable) balance
//   - 500 Internal Server Error: gateway or DB failure
func (h *PMHandler) Withdraw(c *gin.Context) {
	pmID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req withdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Amount) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount is required")
		return
	}

	payoutID, err := h.auto.WithdrawToSettlement(c.Request.Context(), pmID, req.Amount)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInsufficientWithdrawable):
			utils.ErrorResponse(c, http.StatusConflict, "Amount exceeds withdrawable balance (available minus open sell reservations)")
		case errors.Is(err, services.ErrPMNotFound):
			utils.ErrorResponse(c, http.StatusBadRequest, "Payment merchant not found")
		default:
			// Surface configuration errors (missing sub-account, settlement bank)
			// as 400 — they are operator-fixable and not a 500.
			msg := err.Error()
			if strings.Contains(msg, "no Nomba sub-account configured") ||
				strings.Contains(msg, "no settlement bank configured") {
				utils.ErrorResponse(c, http.StatusBadRequest, "PM not configured for withdrawal: "+msg)
				return
			}
			log.Printf("[pm handler] Withdraw pm=%s: %v", pmID, err)
			utils.ErrorResponse(c, http.StatusInternalServerError, "Withdrawal failed")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Withdrawal initiated", gin.H{"payoutId": payoutID})
}
