package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Handlers holds references to all services needed by route handlers.
type Handlers struct {
	PriceService      *services.PriceService
	BlockchainService *services.BlockchainService
	Config            *config.Config
}

// NewHandlers creates a new Handlers instance with the given services.
func NewHandlers(ps *services.PriceService, bs *services.BlockchainService, cfg *config.Config) *Handlers {
	return &Handlers{
		PriceService:      ps,
		BlockchainService: bs,
		Config:            cfg,
	}
}

// --- Auth handlers ---

// Signup handles user registration.
func (h *Handlers) Signup(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Signup endpoint", nil)
}

// Login handles user authentication.
func (h *Handlers) Login(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Login endpoint", nil)
}

// SubmitIdentity handles identity document submission.
func (h *Handlers) SubmitIdentity(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Identity submission endpoint", nil)
}

// SubmitFacial handles facial verification submission.
func (h *Handlers) SubmitFacial(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Facial submission endpoint", nil)
}

// --- User handlers ---

// GetProfile returns the current user's profile.
func (h *Handlers) GetProfile(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get profile endpoint", nil)
}

// UpdateProfile updates the current user's profile.
func (h *Handlers) UpdateProfile(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Update profile endpoint", nil)
}

// GetUsers returns a list of users (admin).
func (h *Handlers) GetUsers(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get users endpoint", nil)
}

// GetUserByID returns a user by ID.
func (h *Handlers) GetUserByID(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get user by ID endpoint", nil)
}

// --- Verification handlers ---

// GetVerificationStatus returns the verification status.
func (h *Handlers) GetVerificationStatus(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Verification status endpoint", nil)
}

// SubmitVerification submits a verification request.
func (h *Handlers) SubmitVerification(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Submit verification endpoint", nil)
}

// ApproveVerification approves a verification request (admin).
func (h *Handlers) ApproveVerification(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Approve verification endpoint", nil)
}

// RejectVerification rejects a verification request (admin).
func (h *Handlers) RejectVerification(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Reject verification endpoint", nil)
}

// --- Upload handlers ---

// UploadFile handles file upload.
func (h *Handlers) UploadFile(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Upload file endpoint", nil)
}

// --- Asset handlers ---

// GetAssets returns user assets.
func (h *Handlers) GetAssets(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get assets endpoint", nil)
}

// GetAssetByID returns a specific asset.
func (h *Handlers) GetAssetByID(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get asset by ID endpoint", nil)
}

// CreateAsset creates a new asset.
func (h *Handlers) CreateAsset(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Create asset endpoint", nil)
}

// --- Transaction handlers ---

// GetTransactions returns user transactions.
func (h *Handlers) GetTransactions(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get transactions endpoint", nil)
}

// GetTransactionByID returns a specific transaction.
func (h *Handlers) GetTransactionByID(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get transaction by ID endpoint", nil)
}

// CreateTransaction creates a new transaction.
func (h *Handlers) CreateTransaction(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Create transaction endpoint", nil)
}

// --- Portfolio handlers ---

// GetPortfolio returns the user's portfolio.
func (h *Handlers) GetPortfolio(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get portfolio endpoint", nil)
}

// GetPortfolioSummary returns a portfolio summary.
func (h *Handlers) GetPortfolioSummary(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get portfolio summary endpoint", nil)
}

// --- Market handlers ---

// GetMarketPrices returns current market prices (public).
func (h *Handlers) GetMarketPrices(c *gin.Context) {
	symbols := c.QueryArray("symbols")
	if len(symbols) == 0 {
		symbols = []string{"BTC", "ETH", "USDT", "BNB", "SOL"}
	}

	prices, err := h.PriceService.GetPrices(symbols)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, err.Error())
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Market prices retrieved", prices)
}

// GetMarketHistory returns historical market data.
func (h *Handlers) GetMarketHistory(c *gin.Context) {
	symbol := c.Param("symbol")
	data, err := h.PriceService.GetHistoricalPrice(symbol, 30)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, err.Error())
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Market history retrieved", data)
}

// GetMarketDataBySymbol returns comprehensive market data for a symbol.
func (h *Handlers) GetMarketDataBySymbol(c *gin.Context) {
	symbol := c.Param("symbol")
	data, err := h.PriceService.GetMarketData(symbol)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, err.Error())
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Market data retrieved", data)
}

// GetWatchlist returns the user's watchlist.
func (h *Handlers) GetWatchlist(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Get watchlist endpoint", nil)
}

// AddToWatchlist adds a symbol to the user's watchlist.
func (h *Handlers) AddToWatchlist(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "Add to watchlist endpoint", nil)
}

// Product, Category, and Cart handlers are in their respective files:
// - product_handler.go
// - category_handler.go
// - cart_handler.go
