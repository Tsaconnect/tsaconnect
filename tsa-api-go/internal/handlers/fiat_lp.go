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

// FiatLiquidity is the fiat LP side (plan 2026-10-20-fiat-lp-desk); the user
// calls are scoped to the caller, the admin calls to P2P admins.
// *services.FiatLPService satisfies it.
type FiatLiquidity interface {
	Position(ctx context.Context, userID uuid.UUID) (*services.FiatLPPosition, error)
	ConfirmDeposit(ctx context.Context, userID uuid.UUID, wallet, txHash string) (*models.FiatLPDeposit, error)
	RequestWithdrawal(ctx context.Context, userID uuid.UUID, wallet, kind, amount string) (*models.FiatLPWithdrawal, error)
	AdminOverview(ctx context.Context, status string) (*services.FiatLPSummary, []services.FiatLPRequest, error)
	MarkPaid(ctx context.Context, adminID, id uuid.UUID, txHash string) (*models.FiatLPWithdrawal, error)
	Reject(ctx context.Context, adminID, id uuid.UUID, note string) (*models.FiatLPWithdrawal, error)
}

var _ FiatLiquidity = (*services.FiatLPService)(nil)

// SetFiatLiquidity wires the fiat LP side. Nil leaves its routes unregistered.
func (h *P2PHandler) SetFiatLiquidity(f FiatLiquidity) {
	if isNilIface(f) {
		h.fiatLP = nil
		return
	}
	h.fiatLP = f
}

// HasFiatLiquidity reports whether the fiat LP routes register.
func (h *P2PHandler) HasFiatLiquidity() bool { return h != nil && h.fiatLP != nil }

func fiatLPError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, services.ErrLPNotEligible), errors.Is(err, services.ErrKYCVerificationRequired):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, services.ErrLPNotEligible.Error(), "kyc_verification_required")
	case errors.Is(err, services.ErrCompanyUserCannotBeLP):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "The company account cannot provide liquidity", services.CompanyAccountNotEligibleCode)
	case errors.Is(err, services.ErrFiatLPUnproven):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "transfer_unproven")
	case errors.Is(err, services.ErrFiatLPTaken), errors.Is(err, services.ErrFiatLPState):
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrFiatLPTxHash), errors.Is(err, services.ErrFiatLPRefuted), errors.Is(err, services.ErrFiatLPTooSmall),
		errors.Is(err, services.ErrFiatLPAmount), errors.Is(err, services.ErrFiatLPShort), errors.Is(err, services.ErrFiatLPStale):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrFiatLPNotFound):
		utils.ErrorResponse(c, http.StatusNotFound, "Not found")
	case errors.Is(err, services.ErrFiatLPUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, err.Error(), "fiat_unavailable")
	default:
		return false
	}
	return true
}

func (h *P2PHandler) fiatLPFail(c *gin.Context, what string, err error) {
	if fiatLPError(c, err) {
		return
	}
	log.Printf("[fiat-lp] %s: %s", what, services.RedactAlertText(err.Error()))
	utils.ErrorResponse(c, http.StatusInternalServerError, "Could not "+what+" right now")
}

// GET /api/liquidity/fiat
func (h *P2PHandler) FiatLiquidityPosition(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	pos, err := h.fiatLP.Position(c.Request.Context(), user.ID)
	if err != nil {
		h.fiatLPFail(c, "load your naira position", err)
		return
	}
	pos.Wallet = user.WalletAddress
	utils.SuccessResponse(c, http.StatusOK, "Naira liquidity", pos)
}

type fiatDepositRequest struct {
	TxHash string `json:"txHash"`
}

// POST /api/liquidity/fiat/deposits {txHash}: record USDT the caller's proven
// wallet sent to the desk, locked in naira at today's rate.
func (h *P2PHandler) FiatLiquidityDeposit(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	var req fiatDepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}
	wallet, ok := requireProvenWallet(c, user)
	if !ok {
		return
	}
	d, err := h.fiatLP.ConfirmDeposit(c.Request.Context(), user.ID, wallet, req.TxHash)
	if err != nil {
		h.fiatLPFail(c, "confirm your deposit", err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Deposit recorded", d)
}

type fiatWithdrawalRequest struct {
	Kind   string `json:"kind"`   // principal | earnings
	Amount string `json:"amount"` // naira for principal, USDT for earnings
}

// POST /api/liquidity/fiat/withdrawals {kind, amount}
func (h *P2PHandler) FiatLiquidityWithdraw(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	var req fiatWithdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "kind and amount are required")
		return
	}
	wallet, ok := requireProvenWallet(c, user)
	if !ok {
		return
	}
	w, err := h.fiatLP.RequestWithdrawal(c.Request.Context(), user.ID, wallet, strings.ToLower(strings.TrimSpace(req.Kind)), req.Amount)
	if err != nil {
		h.fiatLPFail(c, "request your withdrawal", err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Withdrawal requested", w)
}

// GET /api/admin/liquidity/fiat?status=requested|paid|rejected
func (h *P2PHandler) AdminFiatLiquidity(c *gin.Context) {
	sum, reqs, err := h.fiatLP.AdminOverview(c.Request.Context(), strings.TrimSpace(c.Query("status")))
	if err != nil {
		h.fiatLPFail(c, "load naira liquidity", err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Naira liquidity", gin.H{"summary": sum, "requests": reqs})
}

type fiatPaidRequest struct {
	TxHash string `json:"txHash"`
}

// POST /api/admin/liquidity/fiat/withdrawals/:id/paid {txHash} (super admin)
func (h *P2PHandler) AdminFiatLiquidityPaid(c *gin.Context) {
	user := getUserFromContext(c)
	id, err := uuid.Parse(c.Param("id"))
	var req fiatPaidRequest
	if user == nil || err != nil || c.ShouldBindJSON(&req) != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "A request id and txHash are required")
		return
	}
	w, err := h.fiatLP.MarkPaid(c.Request.Context(), user.ID, id, req.TxHash)
	if err != nil {
		h.fiatLPFail(c, "mark it paid", err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Marked paid", w)
}

type fiatRejectRequest struct {
	Note string `json:"note"`
}

// POST /api/admin/liquidity/fiat/withdrawals/:id/reject {note} (super admin)
func (h *P2PHandler) AdminFiatLiquidityReject(c *gin.Context) {
	user := getUserFromContext(c)
	id, err := uuid.Parse(c.Param("id"))
	var req fiatRejectRequest
	_ = c.ShouldBindJSON(&req)
	if user == nil || err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "A request id is required")
		return
	}
	w, err := h.fiatLP.Reject(c.Request.Context(), user.ID, id, req.Note)
	if err != nil {
		h.fiatLPFail(c, "reject it", err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Rejected", w)
}
