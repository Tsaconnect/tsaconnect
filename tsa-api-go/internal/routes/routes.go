package routes

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
)

// SetupRoutes registers all route groups and endpoints on the router.
func SetupRoutes(router *gin.Engine, cfg *config.Config, h *handlers.Handlers) {
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
	api.Use(middleware.RateLimiter(100, 15*time.Minute))

	auth := middleware.Auth(cfg)
	adminAuth := middleware.AdminAuth(cfg)

	// Auth routes
	authGroup := api.Group("/auth")
	{
		authGroup.POST("/signup", h.Signup)
		authGroup.POST("/login", h.Login)
		authGroup.POST("/identity", auth, h.UpdateIdentity)
		authGroup.POST("/facial", auth, h.UpdateFacial)
	}

	// User routes
	userGroup := api.Group("/users")
	userGroup.Use(auth)
	{
		userGroup.GET("/profile", h.GetProfile)
		userGroup.PUT("/profile", h.UpdateProfile)
		userGroup.GET("/", h.GetAllUsers)
		userGroup.GET("/:id", h.GetUserByID)
	}

	// Verification routes
	verificationGroup := api.Group("/verification")
	{
		verificationGroup.GET("/status", auth, h.GetVerificationStatus)
		verificationGroup.POST("/submit", auth, h.SubmitForVerification)
		verificationGroup.POST("/approve/:id", adminAuth, h.ApproveVerification)
		verificationGroup.POST("/reject/:id", adminAuth, h.RejectVerification)
	}

	// Upload routes
	uploadGroup := api.Group("/upload")
	uploadGroup.Use(auth)
	{
		uploadGroup.POST("/", h.UploadFile)
	}

	// Asset routes
	assetGroup := api.Group("/assets")
	assetGroup.Use(auth)
	{
		assetGroup.GET("/", h.GetAssets)
		assetGroup.GET("/:id", h.GetAssetByID)
		assetGroup.POST("/", h.CreateAsset)
	}

	// Transaction routes
	transactionGroup := api.Group("/transactions")
	transactionGroup.Use(auth)
	{
		transactionGroup.GET("/", h.GetTransactions)
		transactionGroup.GET("/:id", h.GetTransactionByID)
		transactionGroup.POST("/", h.CreateTransaction)
	}

	// Portfolio routes
	portfolioGroup := api.Group("/portfolio")
	portfolioGroup.Use(auth)
	{
		portfolioGroup.GET("/", h.GetPortfolio)
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

	// Category routes (must be registered before /products/:id to avoid conflicts)
	categoryGroup := api.Group("/products/category")
	{
		categoryGroup.GET("/all", h.GetCategories)
		categoryGroup.GET("/tree", h.GetCategoryTree)
		categoryGroup.GET("/:categoryId", h.GetCategoryByID)
		categoryGroup.POST("/", adminAuth, h.CreateCategory)
		categoryGroup.PUT("/:categoryId", adminAuth, h.UpdateCategory)
		categoryGroup.DELETE("/:categoryId", adminAuth, h.DeleteCategory)
	}

	// Product routes (public + auth + admin)
	productGroup := api.Group("/products")
	{
		productGroup.GET("/", h.GetMarketplaceProducts)
		productGroup.GET("/user", auth, h.GetUserProducts)
		productGroup.GET("/:id", h.GetProductByID)
		productGroup.POST("/", adminAuth, h.CreateProduct)
		productGroup.PUT("/:id", adminAuth, h.UpdateProduct)
		productGroup.DELETE("/:id", adminAuth, h.DeleteProduct)
	}

	// Cart routes
	cartGroup := api.Group("/cart")
	{
		cartGroup.GET("/", auth, h.GetOrCreateCart)
		cartGroup.POST("/items", auth, h.AddToCart)
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
	}
}
