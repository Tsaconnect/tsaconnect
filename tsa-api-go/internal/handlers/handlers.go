package handlers

import (
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
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

// Handler methods are defined in their respective files:
// - auth_handler.go: Signup, Login, SubmitIdentity, SubmitFacial
// - user_handler.go: GetProfile, UpdateProfile, GetUsers, GetUserByID, etc.
// - verification_handler.go: GetVerificationStatus, SubmitVerification, etc.
// - asset_handler.go: GetAssets, GetAssetByID, CreateAsset, etc.
// - transaction_handler.go: GetTransactions, GetTransactionByID, CreateTransaction, etc.
// - portfolio_handler.go: GetPortfolio, GetPortfolioSummary, etc.
// - market_handler.go: GetMarketOverview, GetWatchlist, AddToWatchlist, etc.
// - product_handler.go: CreateProduct, GetProductByID, etc.
// - category_handler.go: GetCategories, CreateCategory, etc.
// - cart_handler.go: GetOrCreateCart, AddToCart, etc.
// - upload_handler.go: UploadFile, etc.
