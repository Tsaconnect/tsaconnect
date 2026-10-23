package routes

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
)

// RegisterSellerPayout mounts a marketplace seller's payout destination
// (bank / mobile money; none = wallet). Institutions come from
// GET /api/paycrest/banks.
func RegisterSellerPayout(router *gin.Engine, cfg *config.Config, h *handlers.SellerPayoutHandler) {
	if h == nil {
		return
	}
	g := router.Group("/api/seller")
	g.Use(middleware.RateLimiter(500, 15*time.Minute), middleware.Auth(cfg))
	limit := middleware.RateLimiterBy(20, time.Minute, perUserRateKey)
	// Each PUT mails a fresh code, and a new request resets the attempt count, so
	// code requests and guesses share one tight per-user budget.
	guarded := middleware.RateLimiterBy(10, 15*time.Minute, perUserRateKey)
	g.GET("/payout-method", h.Get)
	g.PUT("/payout-method", guarded, h.Set)
	g.POST("/payout-method/:id/confirm", guarded, h.Confirm)
	g.DELETE("/payout-method", limit, h.Clear)
}
