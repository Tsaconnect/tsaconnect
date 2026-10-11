package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// P2PGasSponsor is what the gas route needs from services.P2PGasSponsor.
type P2PGasSponsor interface {
	TopUp(ctx context.Context, user *models.User, wallet string, req services.P2PGasRequest) (services.GasTopUpResult, error)
}

// The codes the app keys on. Every refusal this route makes carries one of
// these, sponsorship_in_progress, WALLET_PROOF_REQUIRED or
// kyc_verification_required / kyc_legal_name_missing (require(), or the
// open_trade KYC tier) — or is a 400 (bad request) / 429 (daily cap).
const (
	p2pGasUnavailableCode = "gas_unavailable"
	p2pGasNotNeededCode   = "gas_not_needed"
	p2pGasUnusedCode      = "gas_topup_unused"
)

// SetGasSponsor wires the Sonic gas top-up, from main.go, only when the shared
// sponsor lane is live.
func (h *P2PHandler) SetGasSponsor(g P2PGasSponsor) { h.gas = g }

// p2pGasTopUpRequest: tradeId for mark_paid / release / cancel_trade /
// dispute; adId for open_trade / cancel_ad; neither for approve / deposit /
// withdraw.
type p2pGasTopUpRequest struct {
	Action  string `json:"action" binding:"required"`
	TradeID string `json:"tradeId"`
	AdID    string `json:"adId"`
}

// GasTopUp is POST /api/p2p/gas/top-up {action, tradeId?, adId?}: S for the
// caller's next P2P tx, sent to her proven wallet only, and only when that tx
// is really hers to sign now. {skipped: true} when she already holds enough;
// otherwise the confirmed top-up's {txHash, amountWei}.
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
	tradeID, okTrade := optionalUUID(req.TradeID)
	adID, okAd := optionalUUID(req.AdID)
	if !okTrade || !okAd {
		utils.ErrorResponse(c, http.StatusBadRequest, "tradeId and adId must be valid ids")
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
	res, err := h.gas.TopUp(c.Request.Context(), user, wallet, services.P2PGasRequest{Action: action, TradeID: tradeID, AdID: adID})
	if err != nil {
		p2pGasError(c, res, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Gas ready", res)
}

// optionalUUID parses an id the request may omit: "" is uuid.Nil and fine.
func optionalUUID(s string) (uuid.UUID, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return uuid.Nil, true
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// p2pGasError maps a top-up refusal. Anything not named here is ours (a chain
// read, a send that failed) and reads as gas_unavailable, so the app only
// ever branches on the contract's codes.
func p2pGasError(c *gin.Context, res services.GasTopUpResult, err error) {
	// An unavailable error never matches its cause (services.gasUnavailableError),
	// so the order of these cases does not matter.
	switch {
	case errors.Is(err, services.ErrP2PGasUnavailable):
		p2pGasUnavailable(c, res, err)
	case errors.Is(err, services.ErrInvalidParams):
		utils.ErrorResponse(c, http.StatusBadRequest, "That trade or ad could not be found, or none was given for this step.")
	case services.KYCRefusalCode(err) != "":
		// open_trade gas for a caller OpenTrade would refuse on KYC tier (a
		// verified user with no legal name on a sell-ad): the refusal's own
		// copy and code, so the app shows the real blocker.
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), services.KYCRefusalCode(err))
	case errors.Is(err, services.ErrP2PGasNotNeeded):
		utils.ErrorResponseWithCode(c, http.StatusConflict, services.ErrP2PGasNotNeeded.Error(), p2pGasNotNeededCode)
	case errors.Is(err, services.ErrP2PGasUnused):
		utils.ErrorResponseWithCode(c, http.StatusConflict, services.ErrP2PGasUnused.Error(), p2pGasUnusedCode)
	case errors.Is(err, services.ErrSponsorshipInProgress):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Your network fee is already on its way. Check your balance in a moment.", sponsorshipInProgressCode)
	case errors.Is(err, services.ErrTopUpDailyCap):
		utils.ErrorResponse(c, http.StatusTooManyRequests, services.ErrTopUpDailyCap.Error())
	case errors.Is(err, services.ErrSponsorWallet):
		utils.ErrorResponse(c, http.StatusForbidden, "Gas can only be sent to your own linked wallet.")
	default:
		p2pGasUnavailable(c, res, err)
	}
}

// p2pGasUnavailable answers 503 gas_unavailable with the sentinel's own
// sentence (a wrapped error carries the lane's detail, which is logged).
func p2pGasUnavailable(c *gin.Context, res services.GasTopUpResult, err error) {
	logSponsorshipFailure(c, http.StatusServiceUnavailable, err)
	if res.TxHash != "" {
		// Sent but not confirmed in time: the hash lets the app watch for it.
		utils.ErrorResponseWithCodeData(c, http.StatusServiceUnavailable, services.ErrP2PGasUnavailable.Error(), p2pGasUnavailableCode, res)
		return
	}
	utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, services.ErrP2PGasUnavailable.Error(), p2pGasUnavailableCode)
}
