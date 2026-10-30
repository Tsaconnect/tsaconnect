package handlers

import (
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Handlers holds references to all services needed by route handlers.
type Handlers struct {
	PriceService          *services.PriceService
	BlockchainService     *services.BlockchainService
	OTCService            *services.OTCService
	P2PService            *services.P2PService
	TokenDiscoveryService *services.TokenDiscoveryService
	Config                *config.Config
	EventBus              *events.Bus
	EmailService          *services.EmailService
	// FeeService is the fee/TP engine (SP3). Optional/nil-safe: wired from
	// main.go after construction. Fee-settled sites route through
	// settleFeeEarnings.
	FeeService *services.FeeDistributionService

	// EligibilityService is the SP7 claim-eligibility gate. Optional/nil-safe:
	// wired from main.go. Signup uses it to run the bounded velocity heuristic on
	// the referrer (best-effort, advisory) when a referred user joins.
	EligibilityService *services.RewardEligibilityService

	// MCGPSwap is the Contract B swap service (optional/nil-safe; wired from
	// main.go). Spot-listing reads use its on-chain quote for MCGP's price.
	MCGPSwap *services.MCGPSwapService

	// ChainRegistry is the non-EVM chain registry (optional/nil-safe; wired from
	// main.go after construction). Non-EVM wallet endpoints return 503 when
	// their chain key is absent from the registry.
	ChainRegistry *chains.Registry

	// NameChanges runs verified users' name changes by re-verification
	// (optional/nil-safe; wired from main.go). Nil answers the user routes 503.
	NameChanges *services.NameChangeService

	// Chat runs customer–provider conversations (wired from main.go; nil
	// answers the chat routes 503).
	Chat *services.ChatService

	// WebCodeMailer sends the TSA Connect web app's sign-in codes straight to
	// the account's email. nil = web sign-in from a new browser is refused
	// (a trusted browser still signs in).
	WebCodeMailer services.CodeMailer
}

// NewHandlers creates a new Handlers instance with the given services.
// TokenDiscoveryService is constructed in-place because it has no
// dependencies beyond the price service and reads its own env vars.
func NewHandlers(ps *services.PriceService, bs *services.BlockchainService, cfg *config.Config, bus *events.Bus, es *services.EmailService, otc *services.OTCService, p2p *services.P2PService) *Handlers {
	return &Handlers{
		PriceService:          ps,
		BlockchainService:     bs,
		OTCService:            otc,
		P2PService:            p2p,
		TokenDiscoveryService: services.NewTokenDiscoveryService(ps),
		Config:                cfg,
		EventBus:              bus,
		EmailService:          es,
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
