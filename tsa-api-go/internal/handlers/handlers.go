package handlers

import (
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Handlers holds references to all services needed by route handlers.
type Handlers struct {
	PriceService      *services.PriceService
	BlockchainService *services.BlockchainService
	OTCService        *services.OTCService
	P2PService        *services.P2PService
	Config            *config.Config
	EventBus          *events.Bus
	EmailService      *services.EmailService
}

// NewHandlers creates a new Handlers instance with the given services.
func NewHandlers(ps *services.PriceService, bs *services.BlockchainService, cfg *config.Config, bus *events.Bus, es *services.EmailService, otc *services.OTCService, p2p *services.P2PService) *Handlers {
	return &Handlers{
		PriceService:      ps,
		BlockchainService: bs,
		OTCService:        otc,
		P2PService:        p2p,
		Config:            cfg,
		EventBus:          bus,
		EmailService:      es,
	}
}

// Handler methods are defined in their respective files:
// - auth_handler.go: Signup, Login, SendOTP, VerifyOTP, ResendOTP
// - user_handler.go: GetProfile, UpdateProfile, GetUsers, GetUserByID, etc.
// - kyc_handler.go: InitiateKYC, VerifyDocument, GetKYCStatus, etc.
// - asset_handler.go: GetAssets, GetAssetByID, CreateAsset, etc.
// - transaction_handler.go: GetTransactions, GetTransactionByID, CreateTransaction, etc.
// - portfolio_handler.go: GetPortfolio, GetPortfolioSummary, etc.
// - market_handler.go: GetMarketOverview, GetWatchlist, AddToWatchlist, etc.
// - product_handler.go: CreateProduct, GetProductByID, etc.
// - category_handler.go: GetCategories, CreateCategory, etc.
// - cart_handler.go: GetOrCreateCart, AddToCart, etc.
// - upload_handler.go: UploadFile, etc.
