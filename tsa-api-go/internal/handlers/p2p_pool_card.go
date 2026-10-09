package handlers

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// PoolCardSource is *services.ITService.
type PoolCardSource interface {
	PoolCard(ctx context.Context) (*services.PoolCard, error)
}

// SetPoolCard wires the pinned card's read. Pass only a non-nil source.
func (h *P2PHandler) SetPoolCard(src PoolCardSource) { h.poolCard = src }

// GetPoolCard handles GET /api/p2p/pool-card?currency=NGN (spec §15): the
// pinned "TSA Connect · Recommended" card's rate and limits without an amount
// (decision 12, S30). Dormant or another currency: {enabled:false}.
func (h *P2PHandler) GetPoolCard(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	cur := strings.ToUpper(strings.TrimSpace(c.DefaultQuery("currency", "NGN")))
	if h.poolCard == nil || cur != "NGN" {
		utils.SuccessResponse(c, http.StatusOK, "Pool card", services.PoolCard{Enabled: false})
		return
	}
	card, err := h.poolCard.PoolCard(c.Request.Context())
	if err != nil || card == nil {
		if err != nil {
			log.Printf("[liquidity] pool card: %s", services.RedactAlertText(err.Error()))
		}
		// The pool exists but cannot say what it offers right now: the card
		// shows, closed, rather than vanishing under her.
		utils.SuccessResponse(c, http.StatusOK, "Pool card", services.PoolCard{Enabled: true, Name: "TSA Connect", Badge: "Recommended"})
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Pool card", card)
}
