package routes

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
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
// Params carry both the Phase-2 (osh/rch/tph) and Flutterwave (fwh/fqh)
// handlers — the two FW params are appended after main's signature.
func SetupRoutes(router *gin.Engine, cfg *config.Config, h *handlers.Handlers, ch *handlers.CheckoutHandler, mrh *handlers.MerchantRequestHandler, sch *handlers.ServiceContactHandler, swh *handlers.SwapHandler, slh *handlers.SwapLiFiHandler, psh *handlers.PrivateSaleHandler, wsHub *ws.Hub, eh *handlers.ExchangeHandler, p2p *handlers.P2PHandler, fch *handlers.FeeConfigHandler, rfh *handlers.RewardFlagHandler, rdh *handlers.RewardDistributionHandler, rsh *handlers.RewardSummaryHandler, fah *handlers.FeeAdminHandler, rph *handlers.RewardPayoutHandler, pmh *handlers.PMHandler, nwh *handlers.NombaWebhookHandler, msh *handlers.MCGPSwapHandler, ith *handlers.ITHandler, osh *handlers.OTCSettingsHandler, rch *handlers.RewardClaimHandler, tph *handlers.TPAdminHandler, fwh *handlers.FlutterwaveWebhookHandler, fqh *handlers.FeeQuoteHandler, fwlh *handlers.FeeWalletHandler, bh *handlers.BillHandler, pch *handlers.PaycrestHandler, pcw *handlers.PaycrestWebhookHandler, dh *handlers.DisputeHandler, iah *handlers.InboundAdminHandler, payh *handlers.PayHandler) {
	// First, before any rate limit: a request through the web app's signed
	// Netlify proxy counts as coming from its visitor, not from Netlify.
	router.Use(middleware.NetlifyClientIP(cfg.NetlifyProxySecret))

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
		authGroup.POST("/reactivate", h.ReactivateAccount)
	}

	// TSA Connect web app sign-in (cookie session). Off unless WEB_APP_ORIGIN
	// is set. Every route needs the CSRF header and the app's own origin, and
	// no answer is ever cached (they set and read the session).
	if cfg.WebAuthEnabled() {
		web := api.Group("/auth/web", middleware.NoStore(), middleware.WebAuthGuard(cfg))
		// Per visitor address (NetlifyClientIP above makes that the real one),
		// loose because an office signs in from one address; and per address
		// and account, so one person's failures don't block a colleague. The
		// per-account lockout is what protects each password.
		web.POST("/login",
			middleware.RateLimiter(300, 15*time.Minute),
			middleware.RateLimiterBy(10, 15*time.Minute, middleware.WebLoginRateKey),
			h.WebLogin)
		// Per pending sign-in, under a per-address ceiling (a made-up challenge
		// cookie gets its own bucket, so the ceiling is what bounds those).
		// Guessing is bounded per challenge anyway (services.EmailCodeAttempts).
		web.POST("/verify",
			middleware.RateLimiter(60, 15*time.Minute),
			middleware.RateLimiterBy(20, 15*time.Minute, middleware.WebChallengeRateKey),
			h.WebVerify)
		// Per session (every tab refreshes on its own), under a per-address
		// ceiling for the same reason.
		web.POST("/refresh",
			middleware.RateLimiter(300, 15*time.Minute),
			middleware.RateLimiterBy(60, 15*time.Minute, middleware.WebRefreshRateKey),
			h.WebRefresh)
		web.POST("/logout", h.WebLogout)
		web.GET("/me", auth, h.WebMe)
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
		userGroup.GET("/cashback-summary", h.GetCashbackSummary)
		userGroup.GET("", h.GetAllUsers)
		userGroup.GET("/:id", h.GetUserByID)
		userGroup.PATCH("/:id/role", adminAuth, h.UpdateUserRole)

		// Self-service account deletion (Apple 5.1.1(v)). Soft-deletes the
		// caller immediately and files an admin review row — see
		// account_deletion_handler.go.
		userGroup.POST("/account/deletion-request", h.RequestAccountDeletion)

		// Name change by re-verification (verified users). NAME_CHANGE_ENABLED
		// gates starting one (create); a change already running
		// can always be paid, verified and cancelled. The create takes a fee
		// and checks the password, so a retried tap replays the first answer
		// (Idempotency-Key) and every write that can spend a fee, a Didit
		// session or a treasury refund has a tight per-user limit. A payment
		// confirm is polled while a transfer mines, so it gets its own looser one.
		nameChangeLimit := middleware.RateLimiterBy(10, 15*time.Minute, perUserRateKey)
		userGroup.GET("/me/name-change", h.GetNameChange)
		userGroup.POST("/me/name-change", nameChangeLimit, middleware.Idempotency(config.DB), h.CreateNameChange)
		userGroup.POST("/me/name-change/confirm-payment", middleware.RateLimiterBy(60, 15*time.Minute, perUserRateKey), h.ConfirmNameChangePayment)
		userGroup.POST("/me/name-change/start-verification", nameChangeLimit, h.StartNameChangeVerification)
		userGroup.POST("/me/name-change/cancel", nameChangeLimit, h.CancelNameChange)
	}

	// Held name changes — any true admin (not merchant/support) reviews them,
	// never their own. Recording a refund's on-chain outcome, and clearing a
	// named payment that will never pay, move money bookkeeping, so they are
	// super_admin only.
	adminNameChanges := api.Group("/admin/name-changes")
	adminNameChanges.Use(adminAuth, middleware.RequireP2PAdmin())
	{
		adminNameChanges.GET("", h.AdminListNameChanges)
		adminNameChanges.POST("/:id/approve", h.AdminApproveNameChange)
		adminNameChanges.POST("/:id/reject", h.AdminRejectNameChange)
		adminNameChanges.POST("/:id/refund-outcome", middleware.RequireSuperAdmin(), h.AdminRecordNameChangeRefund)
		adminNameChanges.POST("/:id/clear-payment", middleware.RequireSuperAdmin(), h.AdminClearNameChangePayment)
	}

	// Admin routes
	adminGroup := api.Group("/admin")
	adminGroup.Use(adminAuth)
	{
		adminGroup.GET("/stats", h.GetAdminStats)

		// Account-deletion review queue. The handlers re-check for a true admin
		// role on top of adminAuth (which also admits merchant/support) because
		// restoring an account is a security-sensitive action.
		adminGroup.GET("/account-deletion-requests", h.ListAccountDeletionRequests)
		adminGroup.POST("/account-deletion-requests/:id/complete", h.CompleteAccountDeletionRequest)
		adminGroup.POST("/account-deletion-requests/:id/restore", h.RestoreAccountDeletionRequest)
	}

	// Admin fee-config routes — unified, tunable fee-rate table. These mutate the
	// live fee rates the quote/charge path reads, so they are funds-adjacent and
	// restricted to true admin roles (RequireP2PAdmin) on top of AdminAuth — same
	// boundary as /admin/fees and /admin/reward-distributions (merchant/support
	// roles must not be able to edit fee rates).
	adminFeeConfig := api.Group("/admin/fee-config")
	adminFeeConfig.Use(adminAuth, middleware.RequireP2PAdmin())
	{
		adminFeeConfig.GET("", fch.ListFeeConfigs)
		adminFeeConfig.PUT("/:id", fch.UpdateFeeConfig)
	}

	// Admin reward-payout routes — the (reward kind, payment token) → (payout
	// token, chain) mapping. Lets admins repoint cashback / TP-level payouts to
	// USDC / USDT / MCGP on any chain without a code change.
	if rph != nil {
		adminRewardPayout := api.Group("/admin/reward-payouts")
		adminRewardPayout.Use(adminAuth)
		{
			adminRewardPayout.GET("", rph.ListRewardPayouts)
			adminRewardPayout.PUT("", rph.UpsertRewardPayout)
		}
	}

	// Admin configurable-FeeManager set-fee flow (design §5) — author a desired-
	// rate intent, plan the unsigned FeeManager.setRate tx(s), confirm a broadcast
	// tx (verify receipt + read-back THEN refresh the replica + advance the intent
	// state machine), cancel an unapplied intent. On-chain-rate authority +
	// funds-adjacent, so restricted to true admin roles (RequireP2PAdmin) on top of
	// AdminAuth, like reward-distributions.
	if fah != nil {
		adminFees := api.Group("/admin/fees")
		adminFees.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminFees.GET("/intents", fah.ListIntents)
			adminFees.POST("/intent", fah.CreateIntent)
			adminFees.POST("/plan", fah.Plan)
			adminFees.POST("/plan-all", fah.PlanAll)
			adminFees.POST("/confirm", fah.Confirm)
			adminFees.DELETE("/intent/:id", fah.CancelIntent)
		}
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
		// Repairs users verified before the Didit legal name was stored; run by
		// hand, one bounded page at a time.
		//
		// RequireP2PAdmin on top of adminAuth because AdminAuth also admits
		// `merchant` and `support`. This pulls identity-document names for up
		// to a page of users from the KYC provider and writes them to `users` —
		// a PII-processing action, not an inventory one, and it is the input to
		// deciding whose bank transfer is whose.
		kycGroup.POST("/admin/backfill-legal-names", adminAuth, middleware.RequireP2PAdmin(), h.AdminBackfillVerifiedNames)
	}

	// Nomba webhook — signature-verified (no auth middleware), mounted OUTSIDE
	// the auth-required groups like the KYC webhook. The path is ALWAYS present so
	// Nomba's configured URL never 404s: when PM is dormant (nwh == nil) the stub
	// returns a clear 503 "PM automation not configured" instead of the handler.
	api.POST("/webhooks/nomba", func(c *gin.Context) {
		if nwh == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "PM automation not configured"})
			return
		}
		nwh.Handle(c)
	})

	// Flutterwave webhook — same contract as the Nomba route: signature-verified
	// (no auth middleware), path always present so the dashboard-configured URL
	// never 404s; a clear 503 when the FW stack is dormant.
	api.POST("/webhooks/flutterwave", func(c *gin.Context) {
		if fwh == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Flutterwave webhook not configured"})
			return
		}
		fwh.Handle(c)
	})

	// Paycrest webhook — same contract as the Nomba and Flutterwave routes:
	// signature-verified (no auth middleware), and the path is ALWAYS present so
	// a dashboard-configured URL never 404s. It 404'd for days while the handler
	// did not exist, which is silent from our side and looks like an outage from
	// theirs.
	api.POST("/webhooks/paycrest", func(c *gin.Context) {
		if pcw == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Paycrest webhook not configured"})
			return
		}
		pcw.Handle(c)
	})

	// Admin PM operator routes (manual-release queue + release/decline/payout).
	// Funds-adjacent, so AdminAuth + RequireP2PAdmin, matching the set-fee group.
	// Registered only when PM automation is configured (pmh != nil); dormant
	// otherwise (the routes simply don't exist).
	if pmh != nil {
		adminPM := api.Group("/admin/pm")
		adminPM.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminPM.GET("", pmh.ListPMs)
			adminPM.GET("/manual-queue", pmh.GetManualQueue)
			adminPM.POST("/trades/:id/release", pmh.ReleaseTrade)
			adminPM.POST("/trades/:id/decline", pmh.DeclineTrade)
			adminPM.POST("/trades/:id/payout", pmh.InitiatePayout)
			adminPM.POST("/:id/withdraw", pmh.Withdraw)
			// PM lifecycle: approve (provision + activate), reject, pause, resume.
			adminPM.POST("/:id/approve", pmh.ApprovePM)
			adminPM.POST("/:id/reject", pmh.RejectPM)
			adminPM.POST("/:id/pause", pmh.PausePM)
			adminPM.POST("/:id/resume", pmh.ResumePM)
		}
	}

	// Upload routes
	uploadGroup := api.Group("/upload")
	uploadGroup.Use(auth)
	{
		uploadGroup.POST("", h.UploadFile)
		uploadGroup.POST("/base64", h.UploadBase64)
	}

	// Transaction routes. Read-only: the write paths debited a DB-held balance
	// and were removed with the assets table — crypto balances live on-chain.
	transactionGroup := api.Group("/transactions")
	transactionGroup.Use(auth)
	{
		transactionGroup.GET("", h.GetTransactions)
		transactionGroup.GET("/:id", h.GetTransactionByID)
	}

	// Market routes (mix of public and authenticated)
	marketGroup := api.Group("/market")
	{
		marketGroup.GET("/overview", h.GetMarketOverview)
		marketGroup.GET("/history/:symbol", h.GetAssetPriceHistory)
		marketGroup.GET("/ohlc/:symbol", h.GetAssetOHLC)
		marketGroup.GET("/assets/:symbol", h.GetAssetMarketDetails)
		marketGroup.GET("/search/:query", h.SearchAssets)
	}

	// Fee configuration (public)
	api.GET("/fees", h.GetFeeConfig)

	// Category routes (must be registered before /products/:id to avoid conflicts)
	categoryGroup := api.Group("/products/category")
	{
		categoryGroup.GET("/all", h.GetCategories)
		categoryGroup.GET("/tree", h.GetCategoryTree)
		categoryGroup.GET("/:categoryId", h.GetCategoryByID)
		// Public read of a booking-mode category's per-area fare schedule (buyer checkout).
		categoryGroup.GET("/:categoryId/booking-fees", h.GetCategoryBookingFees)
		categoryGroup.POST("", adminAuth, h.CreateCategory)
		categoryGroup.PUT("/:categoryId", adminAuth, h.UpdateCategory)
		categoryGroup.DELETE("/:categoryId", adminAuth, h.DeleteCategory)
		categoryGroup.PATCH("/reorder", adminAuth, h.ReorderCategories)
		// Admin upsert of the booking fare schedule.
		categoryGroup.PUT("/:categoryId/booking-fees", adminAuth, h.UpsertCategoryBookingFees)
	}

	// Spot Trade listings — admin-curated pairs shown on the Spot tab. Reads are
	// auth-only (enriched with price + 24h change); CRUD is admin-gated.
	spotGroup := api.Group("/spot-listings")
	spotGroup.Use(auth)
	{
		spotGroup.GET("", h.GetSpotListings)
	}
	adminSpotGroup := api.Group("/admin/spot-listings")
	adminSpotGroup.Use(adminAuth)
	{
		adminSpotGroup.GET("", h.ListSpotListingsAdmin)
		adminSpotGroup.POST("", h.CreateSpotListing)
		adminSpotGroup.PUT("/:id", h.UpdateSpotListing)
		adminSpotGroup.DELETE("/:id", h.DeleteSpotListing)
		adminSpotGroup.POST("/reorder", h.ReorderSpotListings)
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

	// Admin product moderation. Separate from /products, which stays owner-scoped:
	// widening those handlers to admins would put moderation and the merchant's
	// own edit path on the same query.
	adminProductGroup := api.Group("/admin/products")
	adminProductGroup.Use(adminAuth)
	{
		adminProductGroup.GET("", h.AdminListProducts)
		adminProductGroup.GET("/:id", h.AdminGetProduct)
		adminProductGroup.POST("/:id/suspend", h.SuspendProduct)
		adminProductGroup.POST("/:id/unsuspend", h.UnsuspendProduct)
		adminProductGroup.DELETE("/:id", h.AdminDeleteProduct)
		adminProductGroup.POST("/:id/restore", h.RestoreProduct)
	}

	// Admin account moderation. AccountStatus was already enforced by
	// middleware.Auth on every request; these are the endpoints that set it.
	adminUserGroup := api.Group("/admin/users")
	adminUserGroup.Use(adminAuth)
	{
		adminUserGroup.GET("/:id", h.AdminGetUser)
		adminUserGroup.POST("/:id/suspend", h.SuspendUser)
		adminUserGroup.POST("/:id/unsuspend", h.UnsuspendUser)
	}

	// Deposit routes (admin)
	depositGroup := api.Group("/deposits")
	depositGroup.Use(adminAuth)
	{
		depositGroup.GET("", h.GetDeposits)
		depositGroup.PATCH("/:id/status", h.UpdateDepositStatus)
	}

	// Saved delivery addresses the buyer picks from at checkout
	addressGroup := api.Group("/delivery-addresses")
	addressGroup.Use(auth)
	{
		addressGroup.GET("", h.ListDeliveryAddresses)
		addressGroup.POST("", h.CreateDeliveryAddress)
		addressGroup.PUT("/:id", h.UpdateDeliveryAddress)
		addressGroup.DELETE("/:id", h.DeleteDeliveryAddress)
	}

	// Order/checkout routes (authenticated)
	orderGroup := api.Group("/orders")
	orderGroup.Use(auth)
	if ch != nil {
		orderGroup.POST("", ch.CreateOrderFromCart)
		// Read-only price of the active cart for a pay token — what the confirm
		// screen shows, computed by the same code that creates the orders.
		orderGroup.POST("/quote", ch.QuoteCart)
		// Single-service "Pay now" (no cart) for order/booking/contact flows that
		// carry menu selections / booking slots the cart route can't thread.
		orderGroup.POST("/service", ch.CreateServiceOrder)
		orderGroup.GET("", ch.GetUserOrders)
		orderGroup.GET("/shipping-estimate", ch.GetShippingEstimate)
		orderGroup.GET("/:id", ch.GetOrderDetail)
		orderGroup.POST("/:id/prepare-approve", ch.PrepareApprove)
		orderGroup.POST("/:id/prepare-escrow", ch.PrepareEscrow)
		orderGroup.POST("/:id/submit-escrow", ch.SubmitEscrow)
		// MCGP orders: paid from, and released out of, Spendable MCGP (the
		// entitlement contract's operator debit) instead of the escrow.
		orderGroup.POST("/:id/pay-spendable", middleware.RateLimiterBy(20, time.Minute, perUserRateKey), ch.PaySpendable)
		// The endpoints that resolve a Spendable MCGP hold (release or refund)
		// each send operator transactions under the signing lock bills and IT
		// share, at no cost to the caller. One per-user budget across all four.
		orderResolveLimit := middleware.RateLimiterBy(20, time.Minute, perUserRateKey)
		orderGroup.POST("/:id/confirm-receipt", orderResolveLimit, ch.ConfirmReceipt)
		orderGroup.POST("/:id/ship", ch.MarkShipped)
		orderGroup.POST("/:id/deliver", ch.MarkDelivered)
		orderGroup.POST("/:id/approve-refund", ch.ApproveRefund)
		orderGroup.POST("/:id/reject-refund", ch.RejectRefund)
		orderGroup.POST("/:id/prepare-confirm", ch.PrepareConfirm)
		orderGroup.POST("/:id/submit-confirm", ch.SubmitConfirm)
		orderGroup.POST("/:id/request-refund", orderResolveLimit, ch.RequestRefund)
		orderGroup.POST("/:id/cancel", orderResolveLimit, ch.CancelOrder)
		orderGroup.POST("/:id/request-cancel", ch.RequestCancelOrder)
		orderGroup.POST("/:id/approve-cancel", orderResolveLimit, ch.ApproveCancelOrder)
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
		cartGroup.DELETE("/clear", auth, h.ClearCart)
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
		// Per-IP and service-wide ceilings on top of the per-account cap on open
		// challenges inside the handler: each challenge is a DB row.
		walletGroup.POST("/challenge",
			middleware.RateLimiter(30, time.Minute),
			middleware.RateLimiterBy(600, time.Minute, func(*gin.Context) string { return "wallet-challenge" }),
			h.IssueWalletChallenge)
		walletGroup.POST("/register", h.RegisterWalletAddress)
		walletGroup.GET("/balances", h.GetWalletBalances)
		walletGroup.GET("/discovered-tokens", h.GetDiscoveredTokens)
		walletGroup.POST("/prepare-tx", h.PrepareSendTransaction)
		walletGroup.POST("/submit-tx", h.SubmitTransaction)
		walletGroup.GET("/transactions", h.GetTransactionHistory)
		walletGroup.POST("/seed-phrase-backed-up", h.ConfirmSeedPhraseBackup)
		walletGroup.GET("/resolve/:username", h.ResolveUsername)
		// Non-EVM (family) wallet endpoints — dormant-safe: each handler returns
		// 503 "chain unavailable" when ChainRegistry is nil or ForKey misses.
		//
		// The read routes each hit a shared upstream RPC/indexer whose free-tier
		// quota one account could exhaust from a single IP, so throttle them
		// per authenticated user (not just per IP). 60/min is generous for the
		// app's usage (a wallet load fires a handful) but caps abuse well under
		// the gateway budget. Registration/prepare/submit are naturally bounded
		// by user action and keep the global IP limiter only.
		nonEVMReadLimit := middleware.RateLimiterBy(60, time.Minute, perUserRateKey)
		walletGroup.POST("/nonevm/register", h.RegisterNonEVMAddress)
		walletGroup.GET("/nonevm/balances", nonEVMReadLimit, h.GetNonEVMBalances)
		walletGroup.POST("/nonevm/prepare-tx", h.PrepareNonEVMTx)
		walletGroup.POST("/nonevm/submit-tx", h.SubmitNonEVMTx)
		walletGroup.GET("/nonevm/tx-status", nonEVMReadLimit, h.GetNonEVMTxStatus)
		walletGroup.GET("/nonevm/history", nonEVMReadLimit, h.GetNonEVMHistory)
		walletGroup.GET("/nonevm/token-info", nonEVMReadLimit, h.GetNonEVMTokenInfo)
	}

	// Swap / OTC marketplace routes
	if swh != nil {
		swapGroup := api.Group("/swap")
		{
			swapGroup.GET("/price", swh.GetSwapPrice)
			swapGroup.POST("/prepare", auth, swh.PrepareSwap)
		}
		// What the caller's wallet may sell back: its own OTC purchases.
		api.GET("/otc/position", auth, swh.GetOTCPosition)
	}

	// OTC listing controls — PM kill-switches per direction + listing metadata
	// for the app's Easyswap "OTC" tab. The app read is auth'd and fails OPEN
	// client-side if unreachable; the write pauses/unpauses a trading side, so
	// it is restricted to true admin roles (RequireP2PAdmin) on top of
	// AdminAuth, same boundary as /admin/fees.
	if osh != nil {
		api.GET("/otc/settings", auth, osh.GetOTCSettings)

		adminOTC := api.Group("/admin/otc")
		adminOTC.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminOTC.PUT("/settings", osh.UpdateOTCSettings)
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
			// Follows a broadcast swap to the destination chain: a cross-chain
			// route is not done when its source tx confirms.
			lifiGroup.GET("/status", slh.GetSwapStatus)
			// AllSwap server-verified settle (design §8, I8): authenticated user,
			// NOT super-admin. Backend re-queries LiFi + accrues the verified fee.
			lifiGroup.POST("/settle", slh.SettleAllSwap)
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

	// Service listing config routes (three service types): per-service profile,
	// menu items (order), availability + computed slots (booking). Reads are
	// public; writes are gated by auth and the handler's owner check.
	{
		svc := api.Group("/services")
		svc.GET("/booking-catalog", h.GetBookingCatalog)
		svc.GET("/:id/profile", h.GetServiceProfile)
		svc.GET("/:id/form-schema", h.GetBookingFormSchema)
		svc.GET("/:id/menu", h.ListServiceMenu)
		svc.GET("/:id/availability", h.GetServiceAvailability)
		svc.GET("/:id/slots", h.GetServiceSlots)
		svc.PUT("/:id/profile", auth, h.UpsertServiceProfile)
		svc.POST("/:id/menu", auth, h.CreateServiceMenuItem)
		svc.PUT("/:id/menu/:itemId", auth, h.UpdateServiceMenuItem)
		svc.DELETE("/:id/menu/:itemId", auth, h.DeleteServiceMenuItem)
		svc.PUT("/:id/availability", auth, h.ReplaceServiceAvailability)

		// Booking requests (unified booking engine, Phase 2 — request & approval).
		// The literal "requests" paths coexist with the "/:id/..." param routes
		// (same pattern as /contact-payments above).
		svc.POST("/:id/requests", auth, ch.CreateBookingRequest)
		svc.GET("/requests", auth, ch.ListBookingRequests)
		svc.POST("/requests/:reqId/approve", auth, ch.ApproveBookingRequest)
		svc.POST("/requests/:reqId/reject", auth, ch.RejectBookingRequest)
		// Cancel is open to either party (buyer withdraws, provider backs out).
		svc.POST("/requests/:reqId/cancel", auth, ch.CancelBookingRequest)
		svc.POST("/requests/:reqId/pay", auth, ch.PayBookingRequest)
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

	// System Fees & TradePoints: admin split config + distribution tracker,
	// plus the user-facing leaderboard. Read-only over what the fee engine
	// already writes, except the split PUT which retunes future distributions.
	if tph != nil {
		tpUser := api.Group("/tp")
		tpUser.Use(auth)
		{
			tpUser.GET("/leaderboard", tph.GetTPLeaderboard)
		}

		adminTP := api.Group("/admin/tp")
		adminTP.Use(adminAuth)
		{
			adminTP.GET("/splits", tph.ListFeeSplits)
			// Changing a split changes how every future fee is carved, so it
			// takes the stricter gate on top of adminAuth.
			adminTP.PUT("/splits/:feature", middleware.RequireP2PAdmin(), tph.UpsertFeeSplit)
			adminTP.GET("/distributions", tph.ListFeeDistributions)
			adminTP.GET("/distributions/:sourceType/:sourceId", tph.GetFeeDistributionDetail)
			adminTP.GET("/fee-summary", tph.GetFeeSummary)
			adminTP.GET("/global-pool", tph.GetGlobalPool)
		}
	}

	// Fee wallet — which wallet system fees are assigned to. Any admin may read
	// the panel; reassigning it re-points where every section's fees are meant
	// to land, so the write is super_admin only (same boundary as IT money
	// actions).
	if fwlh != nil {
		adminFeeWallet := api.Group("/admin/fees")
		adminFeeWallet.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminFeeWallet.GET("/wallet", fwlh.GetFeeWallet)
			adminFeeWallet.PUT("/wallet", middleware.RequireSuperAdmin(), fwlh.UpdateFeeWallet)
		}
	}

	// Reward programme (PM signup/onboarding enrolment forms). Distinct from
	// the TP-ladder /rewards endpoints above — see models/reward_claim.go.
	if rch != nil {
		rewardClaims := api.Group("/reward-claims")
		rewardClaims.Use(auth)
		{
			rewardClaims.POST("", rch.SubmitRewardClaim)
			rewardClaims.GET("/me", rch.GetMyRewardClaims)
		}

		adminRewardClaims := api.Group("/admin/reward-claims")
		adminRewardClaims.Use(adminAuth)
		{
			adminRewardClaims.GET("", rch.ListRewardClaimsAdmin)
			adminRewardClaims.GET("/downlines", rch.GetDownlinesAdmin)
			// Approval is funds-adjacent (it authorises a payout), so it takes
			// the stricter gate on top of adminAuth.
			adminRewardClaims.PATCH("/:id", middleware.RequireP2PAdmin(), rch.UpdateRewardClaimStatus)
		}
	}

	// User PM-status read — dormant-safe, always registered so the app can
	// gate "Become a PM" CTAs even when the PM/Nomba stack is unconfigured.
	// When pmh is nil (dormant) the inline wrapper returns {isPm:false} 200.
	{
		p2pUserGroup := api.Group("/p2p")
		p2pUserGroup.Use(auth)
		p2pUserGroup.GET("/pm/me", func(c *gin.Context) {
			if pmh == nil {
				// Dormant path handled inline so the route exists even when the
				// PM handler was never constructed.
				c.JSON(http.StatusOK, gin.H{"success": true, "message": "PM status", "data": gin.H{"isPm": false, "status": ""}})
				return
			}
			pmh.GetMyPMStatus(c)
		})
		// Merchant self-activation: supply BVN/NIN to mint the NGN virtual account
		// and go live. Dormant path returns 503 so the app shows a clear message.
		p2pUserGroup.POST("/pm/activate", func(c *gin.Context) {
			if pmh == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "PM activation not configured"})
				return
			}
			pmh.ActivatePM(c)
		})
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

	// P2P routes — every endpoint requires JWT auth + Didit-verified KYC
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

	// MCGP Swap Contract B (entitlement-ledger swap) — premium buy/spend/sell,
	// legacy redeem, withdraw. Registered only when MCGP_SWAP_B_ADDRESS is set
	// (msh != nil); dormant otherwise. All endpoints require JWT auth; the
	// prepare→sign→confirm pattern means the app signs + broadcasts the returned
	// unsigned txs.
	if msh != nil {
		mcgp := api.Group("/mcgp")
		mcgp.Use(auth)
		{
			mcgp.GET("/balance", msh.GetBalance)
			mcgp.GET("/quote", msh.GetQuote)
			mcgp.GET("/ledger", msh.GetLedger)
			mcgp.POST("/approve/prepare", msh.PrepareApprove)
			mcgp.POST("/buy/prepare", msh.PrepareBuy)
			mcgp.POST("/sell/prepare", msh.PrepareSell)
			mcgp.POST("/legacy/sell/prepare", msh.PrepareLegacySell)
			mcgp.POST("/withdraw/prepare", msh.PrepareWithdraw)
			// Generic confirm for buy/sell/legacy/withdraw (writes the audit row
			// after verifying the on-chain event).
			mcgp.POST("/confirm", msh.ConfirmSwap)
			mcgp.POST("/pay/prepare", msh.PreparePay)
			mcgp.POST("/pay/confirm", msh.ConfirmPay)
			// Gasless relaying. Registered unconditionally: /relay/status must be
			// answerable even when relaying is off, because that answer is how the
			// app knows to self-pay.
			mcgp.GET("/relay/status", msh.RelayStatus)
			mcgp.POST("/relay", msh.SubmitRelay)
		}
	} else {
		log.Printf("MCGP swap routes dormant: MCGP_SWAP_B_ADDRESS not set")
	}

	// IT (Instant Transfer) — buy/sell MCGP with Naira via the company PM.
	// Registered only when IT is configured (operator signer present); admin
	// endpoints (rate, approve credit, settle/reverse) gated to P2P admins.
	if ith != nil {
		// Idempotency is applied to the write paths so retries are safe — a
		// stablecoin sell intent is a claim on a deposit, so a retried
		// create must not mint a second one. Follows the same wiring as the
		// P2P group above.
		itIdem := middleware.Idempotency(config.DB)

		// Paycrest ramp — dormant unless the API key is configured, so a missing
		// credential leaves the routes unregistered rather than 500ing.
		if pch.Enabled() {
			pc := api.Group("/paycrest")
			// Quoting stays public: it is a price, it is Paycrest's own public
			// endpoint behind our cache, and the app shows rates before login.
			pc.GET("/quote", pch.Quote)
			pc.Use(auth)
			{
				// Behind auth: every caller is a signed-in screen (the app
				// sends a token on this call already), and for a non-NGN
				// currency it reaches Paycrest, so leaving it open let anyone
				// drive one upstream request per HTTP request.
				pc.GET("/banks", pch.Banks)
				// The converted price is behind auth AND a per-user limit:
				// each call quotes a LiFi route for a token the caller
				// chooses, which is a live request to a paid API. Thirty a
				// minute is well above a user re-quoting as they type, and
				// far below anything worth pointing at LiFi.
				pc.GET("/converted-quote", middleware.RateLimiterBy(30, time.Minute, perUserRateKey), pch.ConvertedQuote)
				// The sell side of the same round trip, and the same reasons
				// for the same limit: one LiFi call per request, for a token
				// the caller names. Registered only when RAMP_CONVERSION_SELL
				// built a quoter, so a deploy that leaves the flag unset has
				// no new route at all.
				if pch.SellQuoteEnabled() {
					pc.GET("/sell-swap-quote", middleware.RateLimiterBy(30, time.Minute, perUserRateKey), pch.SellSwapQuote)
				}
				// Per user, not per IP. Order creation is the most sensitive
				// write here and the only one that spends a /v2/rates call —
				// slow and concurrency-throttled at the provider. One order at
				// a time is already enforced by assertNoOpenOrder, and an
				// idempotency key only collapses IDENTICAL retries, so a
				// single account can otherwise loop failing creates and burn
				// provider quota for everyone. Ten a minute is far above any
				// real user's pace. Mirrors the dispute and bill write limits.
				paycrestWriteLimit := middleware.RateLimiterBy(10, time.Minute, perUserRateKey)
				pc.POST("/buy", itIdem, paycrestWriteLimit, pch.Buy)
				pc.POST("/sell", itIdem, paycrestWriteLimit, pch.Sell)
				pc.GET("/orders", pch.ListOrders)
				pc.GET("/orders/:id", pch.GetOrder)
				pc.POST("/orders/:id/claim-payment", pch.ClaimPayment)
				pc.POST("/orders/:id/cancel", itIdem, pch.Cancel)

				// One swapped sale at a time, driven step by step by the
				// app. Idempotency on the writes because each one is a claim:
				// a retried create must not open a second sale, and a
				// retried order must not open a second payout (both are also
				// idempotent server-side — this just stops the retry
				// reaching them). Registered only when RAMP_CONVERSION_SELL
				// built the service.
				if pch.SellIntentsEnabled() {
					pc.POST("/sell-intents", itIdem, paycrestWriteLimit, pch.CreateSellIntent)
					pc.GET("/sell-intents/open", pch.OpenSellIntent)
					pc.POST("/sell-intents/:id/swap", itIdem, paycrestWriteLimit, pch.RecordSellIntentSwap)
					pc.POST("/sell-intents/:id/order", itIdem, paycrestWriteLimit, pch.OpenSellIntentOrder)
					pc.POST("/sell-intents/:id/abandon", itIdem, paycrestWriteLimit, pch.AbandonSellIntent)
				}

				// Gas sponsorship. The status route is always registered so the
				// app can tell "off" from "old server"; the rest exist only
				// when RAMP_GAS_SPONSORSHIP built at least one lane. No itIdem:
				// both writes are idempotent server-side (one row per order and
				// kind), and a cached replay would hide a top-up that has since
				// confirmed.
				pc.GET("/sponsorship", pch.SponsorshipRoutes)
				if pch.SponsorshipEnabled() {
					pc.GET("/orders/:id/sponsorship", pch.SponsorshipOffer)
					pc.POST("/orders/:id/relay-authorization", paycrestWriteLimit, pch.RelayAuthorization)
					pc.POST("/orders/:id/gas-topup", paycrestWriteLimit, pch.GasTopUp)
				}
				// Source-chain gas for a sale's swap: only when BOTH
				// RAMP_CONVERSION_SELL and RAMP_GAS_SPONSORSHIP are on. No
				// itIdem for the same reason as the order top-up: the claim on
				// the sale's row makes it once-per-sale server-side.
				if pch.SellIntentGasTopUpEnabled() {
					pc.POST("/sell-intents/:id/gas-topup", paycrestWriteLimit, pch.SellIntentGasTopUp)
				}
			}
		}

		// One dispute queue for every order type that cannot resolve itself.
		// Registered only when at least one order stack is wired — see
		// DisputeService.RegisterSubject.
		if dh.Enabled() {
			// The unique index bounds how many disputes one user can hold, but
			// nothing bounds the RATE of attempts, and each carries up to four
			// URLs and a kilobyte of text. The bills report route this
			// supersedes was rate limited; keep that.
			disputeWriteLimit := middleware.RateLimiterBy(10, time.Minute, perUserRateKey)
			d := api.Group("/disputes")
			d.Use(auth)
			{
				d.POST("", disputeWriteLimit, dh.File)
				d.GET("", dh.List)
				d.GET("/:id", dh.Get)
				d.GET("/subject/:type/:id", dh.ForSubject)
			}

			// Disputes carry the reporter's identity and, for bills, customer
			// phone numbers and meter ids. The bill-reports queue this replaces
			// was gated adminAuth + RequireP2PAdmin, so anything looser would
			// widen who can read that data — match it rather than inherit the
			// weaker default.
			adminDisputes := api.Group("/admin/disputes")
			adminDisputes.Use(auth, adminAuth, middleware.RequireP2PAdmin())
			{
				adminDisputes.GET("", dh.ListAdmin)
				adminDisputes.GET("/:id", dh.GetAdmin)
				adminDisputes.POST("/:id/escalate", dh.EscalateAdmin)
				adminDisputes.POST("/:id/resolve", dh.ResolveAdmin)
			}

			// The treasury refund tooling: what a converted buy owes, computed
			// from its own leg ledger, and the admin action that pays it.
			// Funds-moving, so the same boundary as the dispute admin group
			// above — AdminAuth + RequireP2PAdmin. Dormant (RampAdminEnabled
			// false) unless RAMP_CONVERSION_BUY actually built a treasury.
			if dh.RampAdminEnabled() {
				adminRamp := api.Group("/admin/ramp/orders")
				adminRamp.Use(auth, adminAuth, middleware.RequireP2PAdmin())
				{
					adminRamp.GET("/:id/legs", dh.RampLegs)
					adminRamp.POST("/:id/refund", dh.RampRefund)
				}
			}
		}

		// The sell quote is priced live as the user types, and one naira-mode
		// quote runs a dozen fee evaluations — each a possible Flutterwave /fees
		// call — so it is limited per user, not only by the global IP limiter.
		// 30/min is several times a typing user's debounced pace.
		sellQuoteLimit := middleware.RateLimiterBy(30, time.Minute, perUserRateKey)
		it := api.Group("/it")
		it.Use(auth)
		{
			it.GET("/rate", ith.GetRate)
			it.GET("/currencies", ith.Currencies)
			it.GET("/agreement", ith.GetAgreement)
			it.POST("/agreement/accept", itIdem, ith.AcceptAgreement)
			it.GET("/withdraw/quote", ith.QuoteWithdrawal)
			it.GET("/withdraw/destinations", ith.WithdrawalDestinations)
			it.GET("/balance", ith.GetBalance)
			it.GET("/va", ith.GetVA)
			it.POST("/va/activate", itIdem, ith.ActivateVA)
			it.POST("/buy", itIdem, ith.Buy)
			it.POST("/buy/:id/cancel", itIdem, ith.PostCancelBuy)
			it.POST("/buy/:id/check-payment", ith.PostCheckBuyPayment)
			it.GET("/sell/quote", sellQuoteLimit, ith.SellQuote)
			it.POST("/sell", itIdem, ith.Sell)
			it.GET("/intents", ith.ListIntents)
			it.GET("/intents/:id", ith.GetIntent)
			it.POST("/stable/quote", ith.PostStableQuote)
			it.POST("/stable/sell", itIdem, ith.PostStableSell)
			it.POST("/stable/sell/:id/deposit", itIdem, ith.PostStableDeposit)
			it.POST("/stable/buy/:id/cancel", itIdem, ith.PostCancelStableBuy)
			it.POST("/stable/buy/quote", ith.PostStableBuyQuote)
			it.POST("/stable/buy", itIdem, ith.PostStableBuy)
			it.GET("/stable/buy/chains", ith.GetStableBuyChains)
		}
		adminIT := api.Group("/it/admin")
		adminIT.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminIT.GET("/rate", ith.AdminGetRate)
			adminIT.POST("/rate", ith.AdminSetRate)
			adminIT.GET("/intents", ith.AdminListIntents)
			adminIT.GET("/stable/stuck", ith.GetStuckStableSells)
			adminIT.GET("/stable/buy/stuck", ith.GetStuckStableBuys)
			adminIT.GET("/sell/stuck", ith.GetStuckMCGPSells)
			// Money-movement actions are super_admin only — a plain admin may
			// view the orders console but not credit/reprice/settle/reverse.
			// The admin UI hides these buttons for non-super_admins; this is the
			// enforcing gate behind that UI restriction.
			superIT := adminIT.Group("", middleware.RequireSuperAdmin())
			superIT.POST("/credit/:id", ith.AdminApproveCredit)
			superIT.POST("/reprice/:id", ith.AdminRepriceCredit)
			superIT.POST("/settle/:id", ith.AdminSettle)
			superIT.POST("/reverse/:id", ith.AdminReverse)
			superIT.POST("/sell/:id/retry-payout", ith.AdminRetrySellPayout)
			superIT.POST("/sell/:id/reevaluate", ith.AdminReevaluateSell)
			superIT.POST("/stable/:id/repay", ith.AdminRepayStableSell)
			// RepriceParkedStableBuy resumes a purchase still holding the
			// user's naira, same class of action as AdminRepriceCredit above —
			// it belongs behind the same super_admin gate, not plain admin.
			superIT.POST("/stable/buy/:id/reprice", ith.PostRepriceParkedStableBuy)
		}
	} else {
		log.Printf("IT routes dormant: IT_OPERATOR_PRIVATE_KEY / MCGP_SWAP_B_ADDRESS not set")
	}

	// Utility bills — airtime/data/electricity/cable/betting/exam e-PINs paid
	// with spendable MCGP (operator debit path) and vended via ClubKonnect.
	// Registered only when the bill stack is fully wired (bh != nil).
	if bh != nil {
		bills := api.Group("/bills")
		bills.Use(auth)
		// Per-USER limiter on the endpoints that hit the paid vendor API or
		// write rows. The global IP limiter is weak cover here: mobile users
		// share carrier NAT IPs, and /validate is a name-lookup oracle for
		// meter/smartcard numbers if left open to enumeration.
		billWriteLimit := middleware.RateLimiterBy(20, time.Minute, perUserRateKey)
		{
			bills.GET("/catalog/:category", bh.GetProviders)
			bills.GET("/catalog/:category/:provider/plans", bh.GetPlans)
			bills.POST("/validate", billWriteLimit, bh.Validate)
			bills.POST("/quote", billWriteLimit, bh.Quote)
			bills.GET("/orders", bh.ListOrders)
			bills.GET("/orders/:id", bh.GetOrder)
			bills.POST("/orders/:id/pay", billWriteLimit, bh.Pay)
			bills.POST("/orders/:id/report", billWriteLimit, bh.ReportOrder)
			bills.GET("/orders/:id/report", bh.GetOrderReport)
		}

		// Admin review of user-reported bill orders. Inside the same gate:
		// reports can only exist where the bill stack is wired.
		adminBills := api.Group("/admin/bills")
		adminBills.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminBills.GET("/reports", bh.ListReportsAdmin)
			adminBills.POST("/reports/:id/resolve", bh.ResolveReportAdmin)
			adminBills.GET("/payments/stuck", bh.ListStuckPaymentsAdmin)
			// Moving a bill's MCGP is super_admin only, like the IT
			// settle/reverse actions.
			superBills := adminBills.Group("", middleware.RequireSuperAdmin())
			superBills.POST("/orders/:id/reverse-payment", bh.ReversePaymentAdmin)
			superBills.POST("/orders/:id/settle-payment", bh.SettlePaymentAdmin)
		}
	} else {
		log.Printf("bill routes dormant: CLUBKONNECT_USER_ID/CLUBKONNECT_API_KEY or MCGP operator not set")
	}

	// Deposit/withdrawal fee quotes — ALWAYS registered, independent of the IT
	// gate above (the calculator has a config fallback for every external
	// dependency, so dormant Flutterwave/IT must not hide the fee preview).
	if fqh != nil {
		quote := api.Group("/it")
		quote.Use(auth)
		quote.GET("/quote", fqh.GetQuote)
	}

	// Held inbound credits — the operator queue for money that landed in the
	// central account and could not be attributed (central-account design §8).
	//
	// ALWAYS registered, independent of INBOUND_MODE and of whether Flutterwave
	// is configured: the ingestor records credits in either mode, and a held
	// credit has no automatic exit, so a queue that disappears with the poller
	// is a queue nobody can drain after an outage.
	//
	// Funds-adjacent, so AdminAuth + RequireP2PAdmin, matching /admin/fees and
	// /admin/bills. The three actions that decide where real money ends up take
	// the stricter super_admin gate on top, exactly like the IT orders console:
	// a plain admin may read the queue and the evidence, only a super_admin may
	// move the money.
	if iah != nil {
		adminInbound := api.Group("/admin/inbound")
		adminInbound.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminInbound.GET("/credits", iah.ListCredits)
			adminInbound.GET("/credits/:id/candidates", iah.GetCandidates)

			superInbound := adminInbound.Group("", middleware.RequireSuperAdmin())
			superInbound.POST("/credits/:id/assign", iah.AssignCredit)
			superInbound.POST("/credits/:id/refund", iah.MarkRefunded)
			superInbound.POST("/credits/:id/ignore", iah.MarkIgnored)
		}
	}

	// TSA Pay merchant administration. Enabled() is false until buildTSAPay
	// wires a payment service (TSA_PAY_ENABLED unset, or a bad master key),
	// so no route here is reachable even though the handler always exists —
	// see cmd/server/tsa_pay.go.
	//
	// List/get/update are P2P-admin-gated like the rest of the money-adjacent
	// consoles; changing where a merchant is paid or notified is checked
	// in-handler (AdminUpdateMerchant) since only SOME fields on the same PUT
	// need the stricter gate. Key rotation is unconditionally super_admin —
	// there's no "safe" field on that endpoint.
	if payh.Enabled() {
		adminPay := api.Group("/admin/pay")
		adminPay.Use(adminAuth, middleware.RequireP2PAdmin())
		{
			adminPay.GET("/merchants", payh.AdminListMerchants)
			adminPay.GET("/merchants/:id", payh.AdminGetMerchant)
			adminPay.PUT("/merchants/:id", payh.AdminUpdateMerchant) // sensitive fields need super admin, checked in-handler
			// Per merchant: a revoke breaks the merchant's integration and sends them an
			// unmutable alert, so a burst of them is refused rather than repeated.
			adminPay.POST("/merchants/:id/revoke-keys", middleware.RequireSuperAdmin(),
				middleware.RateLimiterBy(6, time.Hour, func(c *gin.Context) string { return "pay-revoke:" + c.Param("id") }),
				payh.AdminRevokeKeys)
			// Owners' business rename requests: approving one renames the
			// merchant. Any true admin decides the queue (never for their own
			// merchant); renaming directly on the merchant PUT is super_admin.
			adminPay.GET("/merchant-name-changes", payh.AdminListBusinessNameChanges)
			adminPay.POST("/merchant-name-changes/:id/approve", payh.AdminApproveBusinessNameChange)
			adminPay.POST("/merchant-name-changes/:id/reject", payh.AdminRejectBusinessNameChange)

			// Payments + webhook-delivery console (Task 8): read-only across
			// every merchant, plus the one write — requeueing a delivery —
			// which only redrives an already-signed, already-stored payload
			// and so needs no stricter gate than the rest of this group.
			adminPay.GET("/payments", payh.AdminListPayments)
			adminPay.GET("/payments/:id/deliveries", payh.AdminPaymentDeliveries)
			adminPay.POST("/deliveries/:id/resend", payh.AdminResendWebhook)
		}

		// TSA Pay merchant API — its own top-level group, deliberately NOT
		// nested under `api` (whose 500/15min limiter is shared by every
		// unauthenticated caller behind one egress IP and would starve a
		// merchant's integration traffic long before a real abuser). An IP
		// limiter still runs first, before any DB work or key lookup, then
		// auth, then a tighter per-merchant limiter. Both limiters are
		// in-memory per process — correct while prod runs one API process.
		payRoot := router.Group("/api/pay")
		merchantAPI := payRoot.Group("/v1")
		merchantAPI.Use(middleware.RateLimiter(600, time.Minute),
			middleware.PayMerchantAuth(payh.MerchantService().Authenticate, payh.LiveEnabled()),
			middleware.RateLimiterBy(120, time.Minute, middleware.PayMerchantRateKey))
		{
			merchantAPI.POST("/payments", payh.CreatePayment)
			merchantAPI.GET("/payments/:id", payh.GetPayment)
			merchantAPI.POST("/payments/:id/cancel", payh.CancelPayment)
		}

		// The public status page — no auth, IP rate-limited on its own route
		// (not the merchant-key limiter above, which a customer's browser
		// never authenticates against).
		payRoot.GET("/public/:id", middleware.RateLimiter(120, time.Minute), payh.Public)

		// An open link's payer page: no auth. A :slug no link could have is
		// answered not found before anything keyed on it or any lookup.
		//
		// Starting a payment makes a new one every time (no Idempotency-Key —
		// each customer is a new payment), so it is limited three ways,
		// narrowest first: 20 a minute per visitor, then 300 a minute per link
		// whoever is asking (a busy shop's QR, not a bot minting payments
		// against one address), then 2000 a minute across every link. Narrowest
		// first because a limiter counts every request it sees: a visitor past
		// their own 20 must be stopped before spending anyone else's budget. 20
		// per visitor, not less: behind carrier-grade NAT a whole market can
		// share one address.
		if payh.OpenLinksEnabled() {
			payRoot.GET("/public/open/:slug", middleware.RateLimiter(120, time.Minute), handlers.PayOpenLinkSlugGuard, payh.PublicOpenLink)
			payRoot.POST("/public/open/:slug/payments", middleware.RateLimiter(20, time.Minute), handlers.PayOpenLinkSlugGuard,
				middleware.RateLimiterBy(300, time.Minute, payOpenLinkRateKey),
				middleware.RateLimiterBy(2000, time.Minute, payOpenPaymentsRateKey), payh.PublicOpenPayment)
		}

		// The app's checkout screen: JWT user auth, per-user rate limit.
		// test-outcome is idempotency-protected like every other
		// POST-with-side-effects endpoint.
		checkout := api.Group("/pay/checkout")
		checkout.Use(auth, middleware.RateLimiterBy(60, time.Minute, perUserRateKey))
		{
			checkout.GET("/:id", payh.Checkout)
			checkout.POST("/:id/test-outcome", middleware.Idempotency(config.DB), payh.TestOutcome)

			// The naira start path. With no Paycrest order service wired these
			// answer 503 "Live payments aren't available right now". The quote
			// asks LiFi (a paid API), so it is limited like the other quotes;
			// the two POSTs open a payout or a swap, so they share the ramp's
			// write budget and are idempotency-protected.
			payStartLimit := middleware.RateLimiterBy(10, time.Minute, perUserRateKey)
			checkout.GET("/:id/quote", middleware.RateLimiterBy(30, time.Minute, perUserRateKey), payh.PayQuote)
			checkout.POST("/:id/direct", middleware.Idempotency(config.DB), payStartLimit, payh.PayDirect)
			checkout.POST("/:id/converted", middleware.Idempotency(config.DB), payStartLimit, payh.PayConverted)
			// The crypto-merchant twins: the same limits, the same 503 when unwired.
			checkout.GET("/:id/crypto-quote", middleware.RateLimiterBy(30, time.Minute, perUserRateKey), payh.PayCryptoQuote)
			checkout.POST("/:id/crypto", middleware.Idempotency(config.DB), payStartLimit, payh.PayCrypto)
		}

		// A payer's own payments, for the app's activity feed. Her record, so
		// never cached.
		api.GET("/pay/history", middleware.NoStore(), auth, middleware.RateLimiterBy(60, time.Minute, perUserRateKey), payh.History)

		// A merchant's own account, from the app and the web: JWT user auth.
		// Every guarded request (payouts, keys, webhooks, the webhook secret)
		// re-checks the password in-handler and has its own tight limit — a
		// separate store per route, so a password cannot be guessed through any
		// of them and using up one never spends another. No answer is ever
		// cached: each is one owner's account, keys and secrets included.
		owner := api.Group("/pay/merchant")
		owner.Use(middleware.NoStore(), auth, middleware.RateLimiterBy(120, time.Minute, perUserRateKey))
		{
			owner.GET("", payh.OwnerMerchant)
			owner.GET("/payments", payh.OwnerPayments)
			owner.GET("/payments/:id", payh.OwnerPayment)
			// Payment links. A create resent with the same Idempotency-Key (a
			// retry after a lost answer) replays the first link rather than
			// making a second; it does not hold back a second request already in
			// flight, so the web and app also disable Send while one is. Creates
			// have their own limit; listing and cancelling are ordinary owner
			// requests.
			owner.POST("/links", middleware.Idempotency(config.DB), middleware.RateLimiterBy(30, time.Minute, perUserRateKey), payh.OwnerCreateLink)
			owner.GET("/links", payh.OwnerLinks)
			owner.POST("/links/:id/cancel", payh.OwnerCancelLink)
			if payh.OpenLinksEnabled() {
				// Open links, the reusable kind: made like a one-time link, then
				// read, edited and given a new address when a printed one leaks.
				owner.POST("/open-links", middleware.Idempotency(config.DB), middleware.RateLimiterBy(30, time.Minute, perUserRateKey), payh.OwnerCreateOpenLink)
				owner.GET("/open-links", payh.OwnerOpenLinks)
				owner.GET("/open-links/:id", payh.OwnerOpenLink)
				owner.PUT("/open-links/:id", middleware.RateLimiterBy(20, time.Minute, perUserRateKey), payh.OwnerUpdateOpenLink)
				owner.POST("/open-links/:id/replace", middleware.RateLimiterBy(5, 15*time.Minute, perUserRateKey), payh.OwnerReplaceOpenLink)
			}
			owner.GET("/settlement-options", payh.OwnerSettlementOptions)
			owner.GET("/banks", payh.OwnerBanks)
			// A name enquiry calls the bank provider.
			owner.GET("/banks/resolve", middleware.RateLimiterBy(30, time.Minute, perUserRateKey), payh.OwnerResolveBank)
			owner.PUT("/settlement", middleware.RateLimiterBy(5, 15*time.Minute, perUserRateKey), payh.OwnerUpdateSettlement)
			if payh.BusinessNameChangeEnabled() {
				owner.PUT("/business-name", middleware.RateLimiterBy(5, 15*time.Minute, perUserRateKey), payh.OwnerRequestBusinessName)
			}
			owner.POST("/keys", middleware.RateLimiterBy(5, 15*time.Minute, perUserRateKey), payh.OwnerRequestKeys)
			owner.PUT("/endpoints", middleware.RateLimiterBy(5, 15*time.Minute, perUserRateKey), payh.OwnerRequestEndpoints)
			owner.POST("/webhook-secret", middleware.RateLimiterBy(5, 15*time.Minute, perUserRateKey), payh.OwnerRequestWebhookSecret)
			// The emailed code for any guarded owner action; wrong codes also end
			// the waiting action after a handful (see ConfirmOwnerAction).
			owner.POST("/actions/:id/confirm", middleware.RateLimiterBy(10, 15*time.Minute, perUserRateKey), payh.OwnerConfirmAction)
		}
	}
}

// payOpenPaymentsRateKey is one bucket for every open-link payment start.
func payOpenPaymentsRateKey(*gin.Context) string { return "open-payments" }

// payOpenLinkRateKey keys a limiter on the open link a request names, whoever
// sends it.
func payOpenLinkRateKey(c *gin.Context) string {
	return "open:" + strings.ToLower(strings.TrimSpace(c.Param("slug")))
}

// perUserRateKey keys a per-route limiter on the authenticated user, so users
// behind one carrier NAT do not share a budget. Each RateLimiterBy owns its own
// store, so the key needs no route prefix. "" (no user on the context) makes
// RateLimiterBy fall back to the client IP.
func perUserRateKey(c *gin.Context) string {
	v, _ := c.Get("user")
	switch u := v.(type) {
	case models.User:
		return u.ID.String()
	case *models.User:
		return u.ID.String()
	}
	return ""
}
