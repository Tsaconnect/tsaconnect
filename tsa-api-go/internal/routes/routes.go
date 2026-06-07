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
func SetupRoutes(router *gin.Engine, cfg *config.Config, h *handlers.Handlers, ch *handlers.CheckoutHandler, mrh *handlers.MerchantRequestHandler, sch *handlers.ServiceContactHandler, swh *handlers.SwapHandler, slh *handlers.SwapLiFiHandler, psh *handlers.PrivateSaleHandler, wsHub *ws.Hub, eh *handlers.ExchangeHandler, p2p *handlers.P2PHandler, fch *handlers.FeeConfigHandler, rfh *handlers.RewardFlagHandler, rdh *handlers.RewardDistributionHandler, rsh *handlers.RewardSummaryHandler) {
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
		authGroup.POST("/refresh", h.RefreshToken)
		authGroup.POST("/revoke", auth, h.RevokeToken)
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
		userGroup.GET("/cashback-balance", h.GetCashbackBalance)
		userGroup.GET("/cashback-earnings", h.GetCashbackEarnings)
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

	// Admin fee-config routes — unified, tunable fee-rate table.
	adminFeeConfig := api.Group("/admin/fee-config")
	adminFeeConfig.Use(adminAuth)
	{
		adminFeeConfig.GET("", fch.ListFeeConfigs)
		adminFeeConfig.PUT("/:id", fch.UpdateFeeConfig)
	}

	// Admin claim-eligibility routes (fee/TP overhaul SP7) — anti-sybil reward
	// flags + per-user eligibility review. Funds-/payout-adjacent (a flag gates
	// MCGP claims), so restricted to true admin roles on top of AdminAuth.
	if rfh != nil {
		adminRewards := api.Group("/admin")
		adminRewards.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminRewards.GET("/reward-flags", rfh.ListRewardFlags)
			adminRewards.POST("/reward-flags", rfh.CreateRewardFlag)
			adminRewards.POST("/reward-flags/:id/clear", rfh.ClearRewardFlag)
			adminRewards.GET("/reward-eligibility/:userId", rfh.GetRewardEligibility)
		}
	}

	// Admin reward-distribution routes (fee/TP overhaul SP6) — build + publish
	// cumulative merkle roots. Funds-adjacent (the root authorizes MCGP claims),
	// so restricted to true admin roles on top of AdminAuth, like reward-flags.
	if rdh != nil {
		adminDist := api.Group("/admin/reward-distributions")
		adminDist.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminDist.GET("", rdh.ListDistributions)
			adminDist.POST("/publish", rdh.PublishDistribution)
			adminDist.POST("/confirm", rdh.ConfirmPublish)
		}
	}

	// User reward routes (fee/TP overhaul SP6 + SP8) — all auth'd, all returning
	// empty/ineligible cleanly rather than erroring:
	//   - /claim-data    (SP6): the authed user's proof + cumulative amount for the
	//     latest published epoch — the CANONICAL claim source.
	//   - /summary       (SP8): the full TP-screen dashboard payload.
	//   - /prepare-claim (SP8): the UNSIGNED claim(account, cumulative, proof) tx,
	//     a convenience over /claim-data matching the prepared-unsigned-tx pattern.
	if rdh != nil || rsh != nil {
		rewardsGroup := api.Group("/rewards")
		rewardsGroup.Use(auth)
		{
			if rdh != nil {
				rewardsGroup.GET("/claim-data", rdh.GetClaimData)
			}
			if rsh != nil {
				rewardsGroup.GET("/summary", rsh.GetRewardSummary)
				rewardsGroup.GET("/prepare-claim", rsh.PrepareClaim)
			}
		}
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
		uploadGroup.POST("/base64", h.UploadBase64)
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
		productGroup.PATCH("/:id/status", auth, h.UpdateStatus)
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
		orderGroup.POST("/:id/ship", ch.MarkShipped)
		orderGroup.POST("/:id/deliver", ch.MarkDelivered)
		orderGroup.POST("/:id/approve-refund", ch.ApproveRefund)
		orderGroup.POST("/:id/reject-refund", ch.RejectRefund)
		orderGroup.POST("/:id/prepare-confirm", ch.PrepareConfirm)
		orderGroup.POST("/:id/submit-confirm", ch.SubmitConfirm)
		orderGroup.POST("/:id/request-refund", ch.RequestRefund)
		orderGroup.POST("/:id/cancel", ch.CancelOrder)
		orderGroup.POST("/:id/request-cancel", ch.RequestCancelOrder)
		orderGroup.POST("/:id/approve-cancel", ch.ApproveCancelOrder)
		orderGroup.POST("/:id/submit-cancel", ch.SubmitCancelOrder)
		orderGroup.POST("/:id/reject-cancel", ch.RejectCancelOrder)
		orderGroup.POST("/:id/dispute", ch.RaiseDispute)
	}

	// Admin order routes
	adminOrderGroup := api.Group("/admin/orders")
	adminOrderGroup.Use(adminAuth)
	if ch != nil {
		adminOrderGroup.GET("", ch.GetAllOrders)
		adminOrderGroup.GET("/disputed", ch.GetDisputedOrders)
		adminOrderGroup.POST("/:id/resolve", ch.AdminResolveDispute)
		adminOrderGroup.POST("/:id/submit-resolve", ch.SubmitAdminResolve)
		adminOrderGroup.POST("/:id/dismiss-request", ch.DismissRequest)
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
		cartGroup.GET("/count", auth, h.GetCartCount)
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
		walletGroup.GET("/discovered-tokens", h.GetDiscoveredTokens)
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

	if slh != nil {
		lifiGroup := api.Group("/swap/lifi")
		lifiGroup.Use(auth)
		{
			lifiGroup.GET("/chains", slh.GetChains)
			lifiGroup.GET("/tokens", slh.GetTokens)
			lifiGroup.GET("/tokens/all", slh.GetAllChainTokens)
			lifiGroup.GET("/token", slh.GetToken)
			lifiGroup.GET("/quote", slh.GetQuote)
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
			// List the caller's contact-fee history (default: as provider).
			// Defined on the same group so it shares the auth middleware;
			// the literal path keeps it from colliding with /:id routes.
			serviceGroup.GET("/contact-payments", sch.ListContactPayments)
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
		// Expo push device-token registry (per device).
		notifGroup.POST("/device-tokens", h.RegisterDevice)
		notifGroup.DELETE("/device-tokens/:token", h.UnregisterDevice)
	}

	// Private sale routes
	if psh := psh; psh != nil {
		// Public endpoint - no auth required (called from landing page)
		api.POST("/private-sale/submit", psh.SubmitPrivateSale)

		// Admin endpoints
		adminPrivateSale := api.Group("/admin/private-sale")
		adminPrivateSale.Use(adminAuth)
		{
			adminPrivateSale.GET("/submissions", psh.ListPrivateSaleSubmissions)
			adminPrivateSale.PATCH("/submissions/:id/status", psh.UpdatePrivateSaleStatus)
		}
	}

	// Nomba-backed Nigerian bank directory + name enquiry. Registered as
	// long as Nomba credentials are configured, even when the P2P escrow
	// contract isn't deployed — the form for adding a payment method
	// should still work in dev/sandbox environments.
	if p2p != nil && p2p.HasNomba() {
		banksGroup := api.Group("/p2p/banks")
		banksGroup.Use(auth)
		{
			banksGroup.GET("", p2p.ListNigerianBanks)
			banksGroup.GET("/resolve", p2p.ResolveAccountName)
		}
	}

	// P2P routes — every endpoint requires JWT auth + Persona-verified KYC
	// (the handler itself enforces verification via the require() helper).
	// Idempotency is applied to the write paths so retries are safe.
	if p2p != nil && p2p.IsConfigured() {
		idem := middleware.Idempotency(config.DB)

		p2pGroup := api.Group("/p2p")
		p2pGroup.Use(auth)
		{
			p2pGroup.GET("/chains", p2p.GetChains)
			p2pGroup.GET("/payment-methods", p2p.ListPaymentMethods)
			p2pGroup.POST("/payment-methods", idem, p2p.CreatePaymentMethod)
			p2pGroup.DELETE("/payment-methods/:id", p2p.DeletePaymentMethod)
			p2pGroup.GET("/payment-providers", p2p.ListPaymentProviders)

			// Express auto-match: one-shot best-price quote (open via /ads/:id/trades).
			p2pGroup.POST("/express/quote", p2p.ExpressQuote)

			// Seller deposit / withdraw — pool-model capital movement.
			p2pGroup.GET("/balance", p2p.GetPoolBalance)
			p2pGroup.GET("/allowance", p2p.GetAllowance)
			p2pGroup.POST("/deposits/approve", idem, p2p.PrepareApprove)
			p2pGroup.POST("/deposits", idem, p2p.PrepareDeposit)
			p2pGroup.POST("/deposits/confirm", idem, p2p.ConfirmDeposit)
			p2pGroup.POST("/withdrawals", idem, p2p.PrepareWithdraw)
			p2pGroup.POST("/withdrawals/confirm", idem, p2p.ConfirmWithdraw)

			p2pGroup.GET("/ads", p2p.ListAds)
			p2pGroup.GET("/ads/my", p2p.ListMyAds)
			p2pGroup.GET("/ads/:id", p2p.GetAd)
			p2pGroup.POST("/ads", idem, p2p.CreateAd)
			// Replaces the old /confirm tx-hash flow. Seller signs the EIP-712
			// voucher returned by CreateAd off-chain and submits the signature.
			p2pGroup.POST("/ads/:id/sign", idem, p2p.SignAd)
			p2pGroup.POST("/ads/:id/pause", p2p.PauseAd)
			p2pGroup.POST("/ads/:id/resume", p2p.ResumeAd)
			// Cancel replaces close — on-chain voucher revocation.
			p2pGroup.POST("/ads/:id/cancel", idem, p2p.PrepareCancelAd)
			p2pGroup.POST("/ads/:id/cancel/confirm", idem, p2p.ConfirmCancelAd)
			p2pGroup.POST("/ads/:id/trades", idem, p2p.OpenTrade)

			p2pGroup.GET("/trades", p2p.ListTrades)
			p2pGroup.GET("/trades/:id", p2p.GetTrade)
			p2pGroup.POST("/trades/:id/open/confirm", idem, p2p.ConfirmOpenTrade)
			p2pGroup.POST("/trades/:id/mark-paid", idem, p2p.MarkPaid)
			p2pGroup.POST("/trades/:id/release", idem, p2p.PrepareRelease)
			p2pGroup.POST("/trades/:id/release/confirm", idem, p2p.ConfirmRelease)
			p2pGroup.POST("/trades/:id/cancel", idem, p2p.PrepareCancel)
			p2pGroup.POST("/trades/:id/cancel/confirm", idem, p2p.ConfirmCancel)
			p2pGroup.POST("/trades/:id/dispute", idem, p2p.RaiseDispute)
			p2pGroup.POST("/trades/:id/dispute/confirm", idem, p2p.ConfirmRaiseDispute)
			p2pGroup.GET("/trades/:id/dispute", p2p.GetDispute)

			// Chat messages — text and image-URL posts; List paginates by
			// `since` query param.
			p2pGroup.GET("/trades/:id/messages", p2p.ListMessages)
			p2pGroup.POST("/trades/:id/messages", idem, p2p.SendMessage)

			p2pGroup.GET("/sellers/:id", p2p.GetSellerProfile)
			p2pGroup.POST("/trades/:id/rate", idem, p2p.RateTrade)
		}

		// Admin dispute resolution — funds-moving, audit-writing. AdminAuth
		// admits merchants/support too, so we layer RequireP2PAdmin on top
		// to restrict to admin / super_admin.
		adminP2P := api.Group("/admin/p2p")
		adminP2P.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminP2P.GET("/disputes", p2p.AdminListDisputes)
			adminP2P.GET("/disputes/:id", p2p.AdminGetDispute)
			adminP2P.POST("/disputes/:id/resolve", idem, p2p.AdminResolveDispute)
			adminP2P.POST("/disputes/:id/resolve/confirm", idem, p2p.ConfirmAdminResolveDispute)
		}
	}

	// Exchange rate routes (public — no auth required)
	if eh != nil {
		exchangeGroup := api.Group("/exchange")
		{
			exchangeGroup.GET("/rates", eh.GetRates)
			exchangeGroup.POST("/convert", eh.ConvertPrice)
		}
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
