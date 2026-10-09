package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// AdminGetTrade returns the full operator view of a trade (parties, payee,
// dispute). Route is gated by adminAuth + RequireP2PAdmin.
func (h *P2PHandler) AdminGetTrade(c *gin.Context) {
	if _, ok := h.adminRequire(c); !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	view, err := h.trade.AdminTradeDetail(c.Request.Context(), id)
	if mapServiceError(c, err) {
		return
	}
	h.enrichTradeWithPM(c.Request.Context(), view.Trade, uuid.Nil)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}

// AdminListTradeMessages returns a trade's chat without a participant check.
func (h *P2PHandler) AdminListTradeMessages(c *gin.Context) {
	if _, ok := h.adminRequire(c); !ok {
		return
	}
	tradeID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if h.messages == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Chat not configured"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	var since *time.Time
	if s := c.Query("since"); s != "" {
		if parsed, err := time.Parse(time.RFC3339, s); err == nil {
			since = &parsed
		}
	}
	rows, err := h.messages.AdminList(c.Request.Context(), tradeID, services.MessageFilter{Since: since, Limit: limit})
	if mapMessageError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}
