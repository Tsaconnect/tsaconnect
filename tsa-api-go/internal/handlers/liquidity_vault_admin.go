package handlers

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// VaultPoolAdmin is the console on the shared pool (spec 2026-10-14 §7,
// no-collateral amendment); *services.ITService satisfies it.
type VaultPoolAdmin interface {
	VaultPoolSummary(ctx context.Context) (*services.VaultPoolSummary, error)
	PayPoolPending(ctx context.Context, adminID, operationID uuid.UUID, wallet, note string) (*services.PayPendingResult, error)
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

type payPendingBody struct {
	OperationID string `json:"operationId"`
	Wallet      string `json:"wallet"`
	Note        string `json:"note"`
}

// POST /api/admin/liquidity/pool/pay-pending {operationId, wallet?, note}
// Sends payPending for one wallet's pending withdrawal, or every pending
// wallet when wallet is omitted, budgeted against the vault's balance: a
// wallet the balance cannot reach is skipped, not sent. operationId is the
// console's UUID for this one action: a retry with the same id is answered
// with the first sends' hashes, never a second send.
// → {paid: [{wallet, txHash, amount}], skipped: [{wallet, pending, reason}]}
// (amount: what the send expects to pay; sent, not yet proven paid).
func (h *ITHandler) AdminPayPoolPending(c *gin.Context) {
	var body payPendingBody
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
	res, err := h.vaultAdmin.PayPoolPending(c.Request.Context(), user.ID, opID, body.Wallet, body.Note)
	if err != nil && res != nil && len(res.Paid)+len(res.Skipped) > 0 {
		// Some wallets were already sent before the failure: the console must
		// see them (a retry with the same operationId answers them again).
		msg := services.RedactAlertText(err.Error())
		log.Printf("[liquidity admin] pay-pending op=%s partial (%d sent): %s", opID, len(res.Paid), msg)
		utils.ErrorResponseWithCodeData(c, http.StatusInternalServerError, msg, "pay_pending_partial",
			gin.H{"paid": res.Paid, "skipped": res.Skipped})
		return
	}
	if err != nil {
		poolAdminError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Pending withdrawals sent", gin.H{"paid": res.Paid, "skipped": res.Skipped})
}

// POST /api/admin/liquidity/orders/:id/retry-fill {note}
func (h *ITHandler) AdminRetryPoolFill(c *gin.Context) {
	h.companyWalletAction(c, h.vaultAdmin.RetryPoolFill)
}
