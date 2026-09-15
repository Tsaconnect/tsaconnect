package handlers

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// rampSponsorshipPort is what the HTTP layer needs from services.RampSponsorship.
type rampSponsorshipPort interface {
	Enabled() bool
	Routes(user *models.User) []services.SponsorRoute
	Offer(ctx context.Context, user *models.User, orderID uuid.UUID) (services.SponsorshipOffer, error)
	RelayAuthorization(ctx context.Context, user *models.User, orderID uuid.UUID, auth services.TransferAuthorization) (services.AuthorizationRelayResult, error)
	TopUp(ctx context.Context, user *models.User, orderID uuid.UUID) (services.GasTopUpResult, error)
}

func (h *PaycrestHandler) WithSponsorship(s rampSponsorshipPort) *PaycrestHandler {
	h.sponsor = s
	return h
}

func (h *PaycrestHandler) SponsorshipEnabled() bool {
	return h != nil && h.sponsor != nil && h.sponsor.Enabled()
}

// GET /api/paycrest/sponsorship — which routes this user sells on gas-free.
// Always answers 200 so the app can treat "off" as a normal state.
func (h *PaycrestHandler) SponsorshipRoutes(c *gin.Context) {
	if !h.SponsorshipEnabled() {
		utils.SuccessResponse(c, http.StatusOK, "Gas sponsorship is off", gin.H{"enabled": false, "routes": []services.SponsorRoute{}})
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sponsored routes", gin.H{"enabled": true, "routes": h.sponsor.Routes(getUserFromContext(c))})
}

func (h *PaycrestHandler) sponsorCaller(c *gin.Context) (*models.User, uuid.UUID, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Not authenticated")
		return nil, uuid.Nil, false
	}
	if !h.SponsorshipEnabled() {
		utils.ErrorResponse(c, http.StatusNotFound, "Gas sponsorship is not available")
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order id")
		return nil, uuid.Nil, false
	}
	return user, id, true
}

// GET /api/paycrest/orders/:id/sponsorship
func (h *PaycrestHandler) SponsorshipOffer(c *gin.Context) {
	user, id, ok := h.sponsorCaller(c)
	if !ok {
		return
	}
	offer, err := h.sponsor.Offer(c.Request.Context(), user, id)
	if err != nil {
		sponsorshipError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sponsorship offer", offer)
}

type relayAuthorizationBody struct {
	ValidAfter  uint64 `json:"validAfter"`
	ValidBefore uint64 `json:"validBefore" binding:"required"`
	Nonce       string `json:"nonce" binding:"required"`
	Signature   string `json:"signature" binding:"required"`
}

// POST /api/paycrest/orders/:id/relay-authorization
func (h *PaycrestHandler) RelayAuthorization(c *gin.Context) {
	user, id, ok := h.sponsorCaller(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var body relayAuthorizationBody
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request")
		return
	}
	nonce, err := decodeFixedHex(body.Nonce, 32)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid nonce")
		return
	}
	sig, err := decodeFixedHex(body.Signature, 65)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid signature")
		return
	}
	auth := services.TransferAuthorization{ValidAfter: body.ValidAfter, ValidBefore: body.ValidBefore, Signature: sig}
	copy(auth.Nonce[:], nonce)

	res, err := h.sponsor.RelayAuthorization(c.Request.Context(), user, id, auth)
	if err != nil {
		sponsorshipError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Deposit sent", res)
}

// POST /api/paycrest/orders/:id/gas-topup — blocks until the gas lands (<=30s),
// so the app's own send that follows is priced against a real balance.
func (h *PaycrestHandler) GasTopUp(c *gin.Context) {
	user, id, ok := h.sponsorCaller(c)
	if !ok {
		return
	}
	res, err := h.sponsor.TopUp(c.Request.Context(), user, id)
	if err != nil {
		sponsorshipError(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Gas ready", res)
}

func decodeFixedHex(s string, n int) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
	if err != nil {
		return nil, err
	}
	if len(b) != n {
		return nil, fmt.Errorf("want %d bytes, got %d", n, len(b))
	}
	return b, nil
}

// sponsorshipError maps sponsorship sentinels to user-facing responses. Order
// and ownership errors fall through to paycrestError so a foreign order is the
// same 404 here as on every other order route.
func sponsorshipError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrPaycrestOrderNotYours), errors.Is(err, services.ErrPaycrestOrderNotFound):
		paycrestError(c, err)
	case errors.Is(err, services.ErrSponsorWallet):
		utils.ErrorResponse(c, http.StatusForbidden, "This order belongs to a different wallet.")
	case errors.Is(err, services.ErrKYCRequired):
		utils.ErrorResponse(c, http.StatusForbidden, "Verify your identity to have the network fee covered for this sale.")
	case errors.Is(err, services.ErrSponsorNotAvailable):
		utils.ErrorResponse(c, http.StatusConflict, "The network fee can't be covered for this order. Send it from your wallet instead.")
	case errors.Is(err, services.ErrSponsorOrderState):
		utils.ErrorResponse(c, http.StatusConflict, "This order is no longer waiting for your deposit.")
	case errors.Is(err, services.ErrSponsorshipInProgress):
		utils.ErrorResponse(c, http.StatusConflict, "Already sending. Check the order in a moment.")
	case errors.Is(err, services.ErrAuthorizationWindow), errors.Is(err, services.ErrAuthorizationSignature):
		utils.ErrorResponse(c, http.StatusBadRequest, "That approval has expired or could not be read. Tap send again.")
	case errors.Is(err, services.ErrSimulationReverted):
		utils.ErrorResponse(c, http.StatusBadRequest, "This transfer would fail on-chain. Check your balance and try again.")
	case errors.Is(err, services.ErrTopUpUnusedCap):
		utils.ErrorResponse(c, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, services.ErrTopUpUnconfirmed):
		utils.ErrorResponse(c, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, services.ErrTopUpTooExpensive), errors.Is(err, services.ErrDailyCapExceeded),
		errors.Is(err, services.ErrRelayerUnderfunded), errors.Is(err, services.ErrSimulationUnavailable),
		errors.Is(err, services.ErrSponsorDomainMismatch):
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Covering the network fee is temporarily unavailable. Please try again shortly.")
	case errors.Is(err, services.ErrTopUpFailed):
		utils.ErrorResponse(c, http.StatusBadGateway, "Sending gas failed. Please try again.")
	default:
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not cover the network fee right now. Please try again.")
	}
}
