package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// LiquidityReader is what the LP endpoints need (spec §15).
// *services.LiquidityService satisfies it.
type LiquidityReader interface {
	Me(ctx context.Context, userID uuid.UUID) (*services.LiquidityMe, error)
	Activity(ctx context.Context, userID uuid.UUID, typ, cursor string, limit int) (*services.LiquidityActivityPage, error)
}

// SetLiquidity wires the LP read side. Pass only a non-nil service.
func (h *P2PHandler) SetLiquidity(l LiquidityReader) { h.liquidity = l }

// HasLiquidity reports whether the /liquidity routes should exist.
func (h *P2PHandler) HasLiquidity() bool { return h != nil && h.liquidity != nil }

// GetLiquidityMe handles GET /api/liquidity/me: the caller's own LP state.
// Any signed-in user gets 200 (status "none" when not an LP). No id is ever
// read from the request.
func (h *P2PHandler) GetLiquidityMe(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	me, err := h.liquidity.Me(c.Request.Context(), user.ID)
	if err != nil {
		log.Printf("[liquidity] me user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load your liquidity")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Liquidity", me)
}

// ListLiquidityActivity handles GET /api/liquidity/me/activity?type=&cursor=&limit=.
func (h *P2PHandler) ListLiquidityActivity(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	page, err := h.liquidity.Activity(c.Request.Context(), user.ID, c.Query("type"), c.Query("cursor"), limit)
	switch {
	case errors.Is(err, services.ErrLiquidityActivityType):
		utils.ErrorResponse(c, http.StatusBadRequest, "type must be deposits, withdrawals or payouts")
	case errors.Is(err, services.ErrLiquidityActivityCursor):
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid cursor")
	case errors.Is(err, services.ErrNotLiquidityProvider):
		utils.ErrorResponse(c, http.StatusNotFound, "You are not a liquidity provider")
	case err != nil:
		log.Printf("[liquidity] activity user=%s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Could not load your activity")
	default:
		utils.SuccessResponse(c, http.StatusOK, "Liquidity activity", page)
	}
}
