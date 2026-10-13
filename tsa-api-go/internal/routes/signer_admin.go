package routes

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
)

// RegisterSignerAdmin mounts the super-admin signer lane routes (signer
// consolidation runbook): GET /api/admin/signer/lanes, POST .../fill-gap,
// POST .../retry, POST .../raise-price-cap, POST .../clear-hold.
func RegisterSignerAdmin(router *gin.Engine, cfg *config.Config, h *handlers.SignerAdminHandler) {
	if h == nil || h.Registry == nil {
		return
	}
	g := router.Group("/api/admin/signer/lanes")
	g.Use(middleware.RateLimiter(500, 15*time.Minute), middleware.AdminAuth(cfg), middleware.RequireSuperAdmin())
	g.GET("", h.Lanes)
	g.POST("/fill-gap", h.FillGap)
	g.POST("/retry", h.Retry)
	g.POST("/raise-price-cap", h.RaisePriceCap)
	g.POST("/clear-hold", h.ClearHold)
}
