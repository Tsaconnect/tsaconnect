package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// P2PGasSponsor is what the gas route needs from services.P2PGasSponsor.
type P2PGasSponsor interface {
	TopUp(ctx context.Context, user *models.User, wallet string, action services.P2PGasAction) (services.GasTopUpResult, error)
}

// p2pGasUnavailableCode is the code the app keys its "add a little S" copy on.
const p2pGasUnavailableCode = "gas_unavailable"

// SetGasSponsor wires the Sonic gas top-up, from main.go, only when the shared
// sponsor lane is live.
func (h *P2PHandler) SetGasSponsor(g P2PGasSponsor) { h.gas = g }

type p2pGasTopUpRequest struct {
	Action string `json:"action" binding:"required"`
}

// GasTopUp is POST /api/p2p/gas/top-up {action}: S for the caller's next P2P
// tx, sent to her proven wallet only. {skipped: true} when she already holds
// enough; otherwise the confirmed top-up's {txHash, amountWei}.
func (h *P2PHandler) GasTopUp(c *gin.Context) {
	user, ok := h.require(c)
	if !ok {
		return
	}
	var req p2pGasTopUpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "action is required")
		return
	}
	action, ok := services.ParseP2PGasAction(req.Action)
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "unknown gas action")
		return
	}
	if h.gas == nil {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrP2PGasUnavailable.Error(), p2pGasUnavailableCode)
		return
	}
	wallet, ok := requireProvenWallet(c, user)
	if !ok {
		return
	}
	res, err := h.gas.TopUp(c.Request.Context(), user, wallet, action)
	if err != nil {
		p2pGasError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Gas ready", res)
}

// p2pGasError maps a top-up refusal; what P2P does not phrase itself goes to
// the shared sponsorship mapping (429 cap, 409 sponsorship_in_progress, 503).
func p2pGasError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrP2PGasUnavailable):
		// The sentinel's own sentence: a wrapped error carries the lane's detail.
		logSponsorshipFailure(c, http.StatusServiceUnavailable, err)
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrP2PGasUnavailable.Error(), p2pGasUnavailableCode)
	case errors.Is(err, services.ErrKYCRequired):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "Verify your identity to have the network fee covered.", services.KYCVerificationRequiredCode)
	case errors.Is(err, services.ErrSponsorWallet):
		utils.ErrorResponse(c, http.StatusForbidden, "Gas can only be sent to your own linked wallet.")
	case errors.Is(err, services.ErrInvalidParams):
		utils.ErrorResponse(c, http.StatusBadRequest, "unknown gas action")
	default:
		sponsorshipError(c, err)
	}
}
