package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// FeeTPCutoverHandler serves the one-shot fee/TP v2 cutover-reset endpoint
// (BLOCKER 3). It is the runnable front door to services.ResetForV2Cutover,
// which archives+deletes the legacy tp/cashback ledgers and clears ALL v2
// reward state. This is DESTRUCTIVE (it zeroes balances and wipes v2 state), so
// the route is gated by the strictest admin middleware (RequireP2PAdmin —
// admin / super_admin only) on top of AdminAuth, and the handler additionally
// requires the exact confirm phrase before doing any work.
type FeeTPCutoverHandler struct {
	DB *gorm.DB
}

// NewFeeTPCutoverHandler creates a FeeTPCutoverHandler.
func NewFeeTPCutoverHandler(db *gorm.DB) *FeeTPCutoverHandler {
	return &FeeTPCutoverHandler{DB: db}
}

// cutoverResetRequest is the POST body. Both fields are required.
type cutoverResetRequest struct {
	// Label tags the archive batch (e.g. "2026-06-07-cutover") and is the
	// idempotency key — a re-run with the same label is refused.
	Label string `json:"label"`
	// Confirm MUST equal services.ResetConfirmPhrase or the reset is a no-op
	// error. Forces a deliberate, typed acknowledgement that balances are zeroed.
	Confirm string `json:"confirm"`
}

// CutoverReset handles POST /api/admin/fee-tp/cutover-reset. It runs the guarded
// one-time fee/TP v2 cutover reset and returns the ResetSummary. The cutover
// runbook requires this to run STRICTLY BEFORE flipping FEE_MODEL_V2_ENABLED.
func (h *FeeTPCutoverHandler) CutoverReset(c *gin.Context) {
	if h == nil || h.DB == nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Cutover reset not available")
		return
	}

	var req cutoverResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "A non-empty label is required (e.g. a cutover date)")
		return
	}
	if req.Confirm != services.ResetConfirmPhrase {
		// 403: the caller is authorized but has not provided the destructive-action
		// acknowledgement. Echo the exact phrase so an operator can copy it.
		utils.ErrorResponse(c, http.StatusForbidden,
			"Refusing to run: pass confirm=\""+services.ResetConfirmPhrase+"\" to zero balances and clear v2 state.")
		return
	}

	summary, err := services.ResetForV2Cutover(c.Request.Context(), h.DB, req.Label, req.Confirm)
	if err != nil {
		// A same-label re-run (or any guard refusal) is a client/state conflict.
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Fee/TP v2 cutover reset complete", summary)
}
