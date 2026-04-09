package routes

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/ws"
	"gorm.io/gorm"
)

// wsUpgrader is the WebSocket upgrader with permissive origin check (auth is via JWT token).
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// SetupRoutes registers all route groups and endpoints on the router.
func SetupRoutes(router *gin.Engine, cfg *config.Config, h *handlers.Handlers, ch *handlers.CheckoutHandler, mrh *handlers.MerchantRequestHandler, sch *handlers.ServiceContactHandler, swh *handlers.SwapHandler, wsHub *ws.Hub) {
	// API info
	router.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"name":    "TSA API",
			"version": "1.0.0",
			"status":  "running",
		})
	})

	// API group with rate limiter
	api := router.Group("/api")
	api.Use(middleware.RateLimiter(500, 15*time.Minute))

	auth := middleware.Auth(cfg)
	adminAuth := middleware.AdminAuth(cfg)

	// Auth routes
	authGroup := api.Group("/auth")
	{
		authGroup.POST("/signup", h.Signup)
		authGroup.POST("/login", h.Login)
		authGroup.POST("/send-otp", auth, h.SendOTP)
		authGroup.POST("/verify-otp", auth, h.VerifyOTP)
		authGroup.POST("/resend-otp", auth, h.ResendOTP)
		authGroup.POST("/forgot-password", h.ForgotPassword)
		authGroup.POST("/reset-password", h.ResetPassword)
	}

	// User routes
	userGroup := api.Group("/users")
	userGroup.Use(auth)
	{
		userGroup.GET("/profile", h.GetProfile)
		userGroup.PUT("/profile", h.UpdateProfile)
		userGroup.GET("/referral-stats", h.GetReferralStats)
		userGroup.GET("/referrals", h.GetReferralsWithTP)
		userGroup.GET("/tp-balance", h.GetTPBalance)
		userGroup.GET("/tp-earnings", h.GetTPEarnings)
		userGroup.GET("", h.GetAllUsers)
		userGroup.GET("/:id", h.GetUserByID)
		userGroup.PATCH("/:id/role", adminAuth, h.UpdateUserRole)
	}

	// Admin routes
	adminGroup := api.Group("/admin")
	adminGroup.Use(adminAuth)
	{
		adminGroup.GET("/stats", h.GetAdminStats)
	}

	// KYC routes
	kycGroup := api.Group("/kyc")
	{
		kycGroup.POST("/session", auth, h.CreateKYCSession)
		kycGroup.POST("/webhook", h.KYCWebhook)
		kycGroup.GET("/status", auth, h.GetKYCStatus)
		kycGroup.GET("/admin/verifications", adminAuth, h.GetAllVerifications)
		kycGroup.POST("/admin/override/:id", adminAuth, h.AdminOverrideKYC)
		kycGroup.POST("/admin/reject/:id", adminAuth, h.AdminRejectKYC)
	}

	// Upload routes
	uploadGroup := api.Group("/upload")
	uploadGroup.Use(auth)
	{
		uploadGroup.POST("", h.UploadFile)
	}

	// Asset routes
	assetGroup := api.Group("/assets")
	assetGroup.Use(auth)
	{
		assetGroup.GET("", h.GetAssets)
		assetGroup.GET("/:id", h.GetAssetByID)
		assetGroup.POST("", h.CreateAsset)
	}

	// Transaction routes
	transactionGroup := api.Group("/transactions")
	transactionGroup.Use(auth)
	{
		transactionGroup.GET("", h.GetTransactions)
		transactionGroup.GET("/:id", h.GetTransactionByID)
		transactionGroup.POST("", h.CreateTransaction)
	}

	// Portfolio routes
	portfolioGroup := api.Group("/portfolio")
	portfolioGroup.Use(auth)
	{
		portfolioGroup.GET("", h.GetPortfolio)
		portfolioGroup.GET("/summary", h.GetPortfolioSummary)
	}

	// Market routes (mix of public and authenticated)
	marketGroup := api.Group("/market")
	{
		marketGroup.GET("/overview", h.GetMarketOverview)
		marketGroup.GET("/history/:symbol", h.GetAssetPriceHistory)
		marketGroup.GET("/assets/:symbol", h.GetAssetMarketDetails)
		marketGroup.GET("/search/:query", h.SearchAssets)
		marketGroup.GET("/watchlist", auth, h.GetWatchlist)
		marketGroup.POST("/watchlist", auth, h.AddToWatchlist)
	}

	// Fee configuration (public)
	api.GET("/fees", h.GetFeeConfig)

	// Category routes (must be registered before /products/:id to avoid conflicts)
	categoryGroup := api.Group("/products/category")
	{
		categoryGroup.GET("/all", h.GetCategories)
		categoryGroup.GET("/tree", h.GetCategoryTree)
		categoryGroup.GET("/:categoryId", h.GetCategoryByID)
		categoryGroup.POST("", adminAuth, h.CreateCategory)
		categoryGroup.PUT("/:categoryId", adminAuth, h.UpdateCategory)
		categoryGroup.DELETE("/:categoryId", adminAuth, h.DeleteCategory)
		categoryGroup.PATCH("/reorder", adminAuth, h.ReorderCategories)
	}

	// Product routes (public + auth + admin)
	productGroup := api.Group("/products")
	{
		productGroup.GET("", h.GetMarketplaceProducts)
		productGroup.GET("/public/category", h.GetProductsByCategory)
		productGroup.GET("/public/category/tree/:categoryId", h.GetProductsByCategoryTree)
		productGroup.GET("/non-featured", adminAuth, h.GetNonFeaturedProducts)
		productGroup.GET("/user", auth, h.GetUserProducts)
		productGroup.GET("/:id", h.GetProductByID)
		productGroup.POST("", adminAuth, h.CreateProduct)
		productGroup.PUT("/:id", adminAuth, h.UpdateProduct)
		productGroup.DELETE("/:id", adminAuth, h.DeleteProduct)
		productGroup.PATCH("/:id/featured", adminAuth, h.ToggleFeatured)
	}

	// Deposit routes (admin)
	depositGroup := api.Group("/deposits")
	depositGroup.Use(adminAuth)
	{
		depositGroup.GET("", h.GetDeposits)
		depositGroup.PATCH("/:id/status", h.UpdateDepositStatus)
	}

	// Order/checkout routes (authenticated)
	orderGroup := api.Group("/orders")
	orderGroup.Use(auth)
	if ch != nil {
		orderGroup.POST("", ch.CreateOrderFromCart)
		orderGroup.GET("", ch.GetUserOrders)
		orderGroup.GET("/shipping-estimate", ch.GetShippingEstimate)
		orderGroup.GET("/:id", ch.GetOrderDetail)
		orderGroup.POST("/:id/prepare-approve", ch.PrepareApprove)
		orderGroup.POST("/:id/prepare-escrow", ch.PrepareEscrow)
		orderGroup.POST("/:id/submit-escrow", ch.SubmitEscrow)
		orderGroup.POST("/:id/deliver", ch.MarkDelivered)
		orderGroup.POST("/:id/prepare-confirm", ch.PrepareConfirm)
		orderGroup.POST("/:id/submit-confirm", ch.SubmitConfirm)
		orderGroup.POST("/:id/request-refund", ch.RequestRefund)
		orderGroup.POST("/:id/cancel", ch.CancelOrder)
	}

	// Admin order routes
	adminOrderGroup := api.Group("/admin/orders")
	adminOrderGroup.Use(adminAuth)
	if ch != nil {
		adminOrderGroup.GET("", ch.GetAllOrders)
		adminOrderGroup.POST("/:id/resolve", ch.AdminResolveDispute)
	}

	// Cart routes
	cartGroup := api.Group("/cart")
	{
		cartGroup.GET("", auth, h.GetOrCreateCart)
		cartGroup.POST("/items", auth, h.AddToCart)
		cartGroup.PUT("/items/:id", auth, h.UpdateCartItem)
		cartGroup.DELETE("/items/:id", auth, h.RemoveFromCart)
		cartGroup.POST("/checkout", auth, h.ConvertToOrder)
		cartGroup.GET("/summary", auth, h.GetCartSummary)
		cartGroup.POST("/validate", auth, h.ValidateCart)
		cartGroup.GET("/abandoned", adminAuth, h.GetAbandonedCarts)
		cartGroup.POST("/cleanup", adminAuth, h.CleanupExpiredCarts)
	}

	// Wallet routes
	walletGroup := api.Group("/wallet")
	walletGroup.Use(auth)
	{
		walletGroup.GET("/supported-tokens", h.GetSupportedTokens)
		walletGroup.POST("/register", h.RegisterWalletAddress)
		walletGroup.GET("/balances", h.GetWalletBalances)
		walletGroup.POST("/prepare-tx", h.PrepareSendTransaction)
		walletGroup.POST("/submit-tx", h.SubmitTransaction)
		walletGroup.GET("/transactions", h.GetTransactionHistory)
		walletGroup.POST("/seed-phrase-backed-up", h.ConfirmSeedPhraseBackup)
		walletGroup.GET("/resolve/:username", h.ResolveUsername)
	}

	// Swap / OTC marketplace routes
	if swh != nil {
		swapGroup := api.Group("/swap")
		{
			swapGroup.GET("/price", swh.GetSwapPrice)
			swapGroup.POST("/prepare", auth, swh.PrepareSwap)
		}
	}

	// Merchant request routes (authenticated user)
	if mrh != nil {
		merchantReqGroup := api.Group("/merchant-requests")
		merchantReqGroup.Use(auth)
		{
			merchantReqGroup.POST("", mrh.SubmitMerchantRequest)
			merchantReqGroup.GET("/my-request", mrh.GetMyMerchantRequest)
		}

		// Admin merchant request routes
		adminMerchantReqGroup := api.Group("/admin/merchant-requests")
		adminMerchantReqGroup.Use(adminAuth)
		{
			adminMerchantReqGroup.GET("", mrh.ListMerchantRequests)
			adminMerchantReqGroup.POST("/:id/approve", mrh.ApproveMerchantRequest)
			adminMerchantReqGroup.POST("/:id/reject", mrh.RejectMerchantRequest)
		}
	}

	// Service contact fee routes (authenticated)
	if sch != nil {
		serviceGroup := api.Group("/services")
		serviceGroup.Use(auth)
		{
			serviceGroup.POST("/:id/prepare-contact-fee", sch.PrepareContactFee)
			serviceGroup.POST("/:id/submit-contact-fee", sch.SubmitContactFee)
			serviceGroup.GET("/:id/contact", sch.GetServiceContact)
		}
	}

	// Notification routes (authenticated)
	notifGroup := api.Group("/notifications")
	notifGroup.Use(auth)
	{
		notifGroup.GET("", h.GetNotifications)
		notifGroup.PATCH("/:id/read", h.MarkAsRead)
		notifGroup.PATCH("/read-all", h.MarkAllAsRead)
		notifGroup.GET("/unread-count", h.GetUnreadCount)
		notifGroup.GET("/preferences", h.GetNotificationPreferences)
		notifGroup.PATCH("/preferences", h.UpdateNotificationPreferences)
	}

	// WebSocket route for real-time notifications (JWT auth via query param)
	if wsHub != nil {
		router.GET("/ws/notifications", func(c *gin.Context) {
			tokenString := c.Query("token")
			if tokenString == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Missing token"})
				return
			}

			// Parse and validate JWT
			token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(cfg.JWTSecret), nil
			})
			if err != nil || !token.Valid {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid token"})
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid token claims"})
				return
			}

			userIDStr, ok := claims["userId"].(string)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid token claims"})
				return
			}

			userID, err := uuid.Parse(userIDStr)
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid user ID"})
				return
			}

			// Verify user exists and is active
			var user models.User
			result := config.DB.First(&user, "id = ?", userID)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "User not found"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Database error"})
				return
			}
			if user.AccountStatus != models.AccountStatusActive {
				c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Account is not active"})
				return
			}

			// Upgrade to WebSocket
			conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
			if err != nil {
				log.Printf("[WS] upgrade error for user %s: %v", userID, err)
				return
			}

			client := &ws.Client{
				Hub:    wsHub,
				UserID: userID,
				Conn:   conn,
				Send:   make(chan []byte, 256),
			}

			wsHub.Register <- client
			go client.WritePump()
			go client.ReadPump()
		})
	}
}
