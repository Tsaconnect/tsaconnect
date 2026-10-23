package routes

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
)

// RegisterSellerPayoutAdmin mounts the super-admin seller payout routes: the
// queue and detail, the actions on a parked payout, and the switch. They move
// money or decide who is paid, so a plain admin cannot reach them.
func RegisterSellerPayoutAdmin(router *gin.Engine, cfg *config.Config, h *handlers.SellerPayoutAdminHandler) {
	if h == nil || h.Admin == nil || h.Switch == nil {
		return
	}
	g := router.Group("/api/admin/seller-payouts")
	g.Use(middleware.RateLimiter(500, 15*time.Minute), middleware.AdminAuth(cfg), middleware.RequireSuperAdmin())
	g.GET("", h.List)
	g.GET("/health", h.Health)
	g.GET("/company-owed", h.CompanyOwed)
	g.POST("/company-rebalanced", h.MarkRebalanced)
	g.GET("/switch", h.GetSwitch)
	g.PUT("/switch", h.SetSwitch)
	g.GET("/:id", h.Get)
	g.POST("/:id/retry", h.Retry)
	g.POST("/:id/mark-paid", h.MarkPaid)
	g.POST("/:id/cancel", h.Cancel)
	g.POST("/:id/pay-wallet", h.PayWallet)
	g.POST("/:id/reroute", h.Reroute)
}
