# TSA API (Go)

Go (Gin) REST API with PostgreSQL/GORM backend for a trading/asset management platform. Includes multi-chain wallet functionality (Sonic, BSC).

## Commands

```bash
go build ./cmd/server          # Build server binary
go run ./cmd/server            # Run server, default port 5000
go build ./...                 # Build all packages
go mod tidy                    # Sync dependencies
go test ./... -v -count=1      # Run all tests
go test ./internal/handlers -v # Run handler tests only
go test ./internal/blockchain -v # Run blockchain client tests only
```

## Architecture

```
cmd/server/main.go             # Entry point - Gin setup, graceful shutdown
internal/
  config/
    config.go                  # Config struct from env vars, multi-chain config
    database.go                # PostgreSQL connection (GORM), AutoMigrate
    cloudinary.go              # Cloudinary client init
  models/                      # GORM models (User, Product, Asset, Cart, Category, Portfolio, Transaction, Wallet, WalletTransaction, SupportedToken, VerificationLog)
  blockchain/
    client.go                  # EVMClient - chain-agnostic Ethereum RPC wrapper
    token.go                   # ERC-20 token operations, native transfer prep
    client_test.go             # EVMClient unit tests
    token_test.go              # Token operation tests
  middleware/
    auth.go                    # JWT Bearer token auth
    admin_auth.go              # Admin/merchant role check
    cloudinary.go              # Cloudinary upload helper
    rate_limiter.go            # IP-based rate limiting (100 req/15 min)
    cors.go                    # CORS configuration
    logger.go                  # Request logging
    middleware.go              # Middleware aggregator
  handlers/
    handlers.go                # Handlers struct + stub methods (central aggregator)
    auth_handler.go            # Signup, Login, Identity, Facial
    user_handler.go            # Profile, password, referrals, admin user mgmt
    verification_handler.go    # BVN/document/facial verification, admin approval
    product_handler.go         # Product CRUD, marketplace, category filtering
    category_handler.go        # Category CRUD with tree structure
    cart_handler.go            # Cart operations, checkout, coupon, shipping
    asset_handler.go           # Asset CRUD with real-time pricing
    transaction_handler.go     # Deposit/withdrawal/swap with fee calculation
    portfolio_handler.go       # Portfolio analytics, goals
    market_handler.go          # Market overview, price history, watchlist
    upload_handler.go          # File upload handlers
    wallet_handler.go          # Multi-chain wallet: register, balances, send, history
    wallet_handler_test.go     # Wallet handler unit tests (SQLite in-memory)
    common.go                  # getUserFromContext helper
  services/
    price_service.go           # CoinGecko API with 60s cache
    blockchain_service.go      # Multi-chain BlockchainService registry
  utils/
    validators.go              # BVN, phone, password, email validators
    response.go                # SuccessResponse, ErrorResponse helpers
  routes/
    routes.go                  # All route definitions with middleware
```

## Multi-Chain Wallet Architecture

- **EVMClient** (`blockchain/client.go`): Chain-agnostic Ethereum RPC wrapper. Constructor: `NewEVMClient(rpcURL, chainID)`.
- **BlockchainService** (`services/blockchain_service.go`): Registry of `map[string]*EVMClient` keyed by chain name. Methods: `ClientForChain(name)`, `ClientForChainID(id)`, `TokenAddress(chain, symbol)`.
- **ChainConfig** (`config/config.go`): `Name`, `RPCURL`, `ChainID`, `NativeCurrency`. Stored in `Config.Chains` map.
- **Token addresses**: Keyed as `"chain:symbol"` (e.g., `"sonic:MCGP"`). Native tokens (S, tBNB) use `PrepareNativeTransfer`; ERC-20 tokens use `PrepareERC20Transfer`.
- **Supported tokens**: DB-driven via `SupportedToken` model. Auto-seeded from hardcoded defaults if table is empty.

## API Routes

All routes prefixed with `/api/`:
- `/auth` - signup (3-step), login
- `/users` - user profile management
- `/verification` - document/identity verification
- `/upload` - image uploads via Cloudinary
- `/assets`, `/transactions`, `/portfolio`, `/market` - trading/asset features
- `/products`, `/cart` - marketplace/e-commerce features
- `/wallet` - multi-chain wallet (register address, balances, send, history, supported tokens)
- `/health` - health check (root level)

## Key Patterns

- **Response format**: `{"success": bool, "message": string, "data": object}` or `{"success": false, "errors": array}`
- **Auth**: JWT tokens with 7-day expiry. `Authorization: Bearer <token>` header
- **Rate limiting**: 100 requests per 15 minutes per IP on `/api/` routes
- **Handler structure**: `WalletHandler` has its own struct with `DB`, `Config`, `BlockchainService` fields. Other handlers use `*Handlers` struct.
- **ORM**: GORM with PostgreSQL driver
- **Wallet tests**: Use SQLite in-memory DB (raw DDL, not AutoMigrate) to avoid PostgreSQL-specific defaults

## Environment Variables

Required (set in `.env`):
- `DATABASE_URL` - PostgreSQL connection string
- `JWT_SECRET` - JWT signing secret
- `FRONTEND_URL` - CORS allowed origin (defaults to `http://localhost:3000`)
- `PORT` - Server port (defaults to `5000`)
- `ENV` - `development` or `production`
- `CLOUDINARY_CLOUD_NAME`, `CLOUDINARY_API_KEY`, `CLOUDINARY_API_SECRET`

Blockchain (wallet feature):
- `SONIC_RPC_URL` - Sonic testnet RPC (default: `https://rpc.testnet.soniclabs.com`)
- `SONIC_CHAIN_ID` - Sonic chain ID (default: `14601`)
- `BSC_RPC_URL` - BSC testnet RPC (default: `https://data-seed-prebsc-1-s1.binance.org:8545`)
- `BSC_CHAIN_ID` - BSC chain ID (default: `97`)
- `MCGP_TOKEN_ADDRESS` - MCGP token contract (Sonic)
- `USDT_TOKEN_ADDRESS` - USDT token contract (Sonic)
- `USDC_TOKEN_ADDRESS` - USDC token contract (Sonic)
- `BSC_USDT_TOKEN_ADDRESS` - USDT token contract (BSC)
- `BSC_USDC_TOKEN_ADDRESS` - USDC token contract (BSC)

## Dependencies

Key packages:
- `github.com/gin-gonic/gin` - HTTP framework
- `gorm.io/gorm` + `gorm.io/driver/postgres` - ORM + PostgreSQL driver
- `gorm.io/driver/sqlite` - SQLite driver (test only)
- `github.com/ethereum/go-ethereum` - Ethereum client library
- `github.com/golang-jwt/jwt/v5` - JWT auth
- `golang.org/x/crypto/bcrypt` - Password hashing
- `github.com/cloudinary/cloudinary-go/v2` - Image uploads
- `github.com/joho/godotenv` - Env file loading
- `github.com/google/uuid` - UUID generation
