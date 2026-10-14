package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// VaultPoolAdmin is the collateral provider's console on the shared pool
// (spec 2026-10-14 §7); *services.ITService satisfies it.
type VaultPoolAdmin interface {
	VaultPoolSummary(ctx context.Context) (*services.VaultPoolSummary, error)
	SettlePoolFromCollateral(ctx context.Context, adminID, operationID uuid.UUID, amount, note string) (string, error)
	RetryPoolFill(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error)
}

var _ VaultPoolAdmin = (*services.ITService)(nil)

// SetVaultPoolAdmin wires the console. Nil leaves the routes unregistered.
func (h *ITHandler) SetVaultPoolAdmin(a VaultPoolAdmin) {
	if isNilIface(a) {
		h.vaultAdmin = nil
		return
	}
	h.vaultAdmin = a
}

// VaultPoolEnabled reports whether the shared-pool console routes register.
func (h *ITHandler) VaultPoolEnabled() bool { return h != nil && h.vaultAdmin != nil }

// GET /api/admin/liquidity/pool
func (h *ITHandler) AdminVaultPool(c *gin.Context) {
	sum, err := h.vaultAdmin.VaultPoolSummary(c.Request.Context())
	if err != nil {
		poolAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Liquidity pool", gin.H{"pool": sum})
}

type settleFromCollateralBody struct {
	OperationID string `json:"operationId"`
	Amount      string `json:"amount"`
	Note        string `json:"note"`
}

// POST /api/admin/liquidity/pool/settle-from-collateral {operationId, amount, note}
// operationId is the console's UUID for this one settle: a retry with the same
// id is answered with the first send's hash, never a second settle.
func (h *ITHandler) AdminSettlePoolFromCollateral(c *gin.Context) {
	var body settleFromCollateralBody
	_ = c.ShouldBindJSON(&body)
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	opID, err := uuid.Parse(strings.TrimSpace(body.OperationID))
	if err != nil || opID == uuid.Nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "operationId (a UUID) is required")
		return
	}
	if strings.TrimSpace(body.Amount) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount is required")
		return
	}
	hash, err := h.vaultAdmin.SettlePoolFromCollateral(c.Request.Context(), user.ID, opID, body.Amount, body.Note)
	if err != nil {
		poolAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Settle sent", gin.H{"txHash": hash})
}

// POST /api/admin/liquidity/orders/:id/retry-fill {note}
func (h *ITHandler) AdminRetryPoolFill(c *gin.Context) {
	h.companyWalletAction(c, h.vaultAdmin.RetryPoolFill)
}
