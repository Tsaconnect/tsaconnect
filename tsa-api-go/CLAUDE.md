# TSA API (Go)

Go (Gin) REST API with MongoDB backend for a trading/asset management platform. Converted from Node.js/Express.

## Commands

```bash
go build ./cmd/server          # Build server binary
go run ./cmd/server            # Run server, default port 5000
go build ./...                 # Build all packages
go mod tidy                    # Sync dependencies
```

No test runner configured yet.

## Architecture

```
cmd/server/main.go             # Entry point - Gin setup, graceful shutdown
internal/
  config/
    config.go                  # Config struct from env vars
    database.go                # MongoDB connection (ConnectDB, GetCollection)
    cloudinary.go              # Cloudinary client init
  models/                      # MongoDB document structs (User, Product, Asset, Cart, Category, Portfolio, Transaction, Wallet, VerificationLog)
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
    common.go                  # getUserFromContext helper
  services/
    price_service.go           # CoinGecko API with 60s cache
    blockchain_service.go      # Mock blockchain service
  utils/
    validators.go              # BVN, phone, password, email validators
    response.go                # SuccessResponse, ErrorResponse helpers
  routes/
    routes.go                  # All route definitions with middleware
```

## API Routes

All routes prefixed with `/api/`:
- `/auth` - signup (3-step), login
- `/users` - user profile management
- `/verification` - document/identity verification
- `/upload` - image uploads via Cloudinary
- `/assets`, `/transactions`, `/portfolio`, `/market` - trading/asset features
- `/products`, `/cart` - marketplace/e-commerce features
- `/health` - health check (root level)

## Key Patterns

- **Response format**: `{"success": bool, "message": string, "data": object}` or `{"success": false, "errors": array}`
- **Auth**: JWT tokens with 7-day expiry. `Authorization: Bearer <token>` header
- **Rate limiting**: 100 requests per 15 minutes per IP on `/api/` routes
- **Handler structure**: Most handlers are methods on `*Handlers` struct; some specialized handlers (AssetHandler, TransactionHandler, etc.) have their own structs
- **MongoDB driver**: `go.mongodb.org/mongo-driver` (not an ORM)

## Environment Variables

Required (set in `.env`):
- `MONGODB_URI` - MongoDB connection string
- `JWT_SECRET` - JWT signing secret
- `FRONTEND_URL` - CORS allowed origin (defaults to `http://localhost:3000`)
- `PORT` - Server port (defaults to `5000`)
- `ENV` - `development` or `production`
- `CLOUDINARY_CLOUD_NAME`, `CLOUDINARY_API_KEY`, `CLOUDINARY_API_SECRET`

## Dependencies

Key packages:
- `github.com/gin-gonic/gin` - HTTP framework
- `go.mongodb.org/mongo-driver` - MongoDB driver
- `github.com/golang-jwt/jwt/v5` - JWT auth
- `golang.org/x/crypto/bcrypt` - Password hashing
- `github.com/cloudinary/cloudinary-go/v2` - Image uploads
- `github.com/joho/godotenv` - Env file loading
