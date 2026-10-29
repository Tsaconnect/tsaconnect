package routes

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
)

// RegisterCoreFees mounts fee authoring on the core set's FeeConfig, behind the same boundary as
// /api/admin/fees (true admin roles): GET /api/admin/fees/core, GET .../:chainId,
// POST .../:chainId/plan, POST .../:chainId/verify. Mounted with no set selected (503).
func RegisterCoreFees(router *gin.Engine, cfg *config.Config, h *handlers.CoreFeeHandler) {
	g := router.Group("/api/admin/fees/core")
	g.Use(middleware.RateLimiter(500, 15*time.Minute), middleware.AdminAuth(cfg), middleware.RequireP2PAdmin())
	g.GET("", h.ListChains)
	g.GET("/:chainId", h.State)
	g.POST("/:chainId/plan", h.Plan)
	g.POST("/:chainId/verify", h.Verify)
}
